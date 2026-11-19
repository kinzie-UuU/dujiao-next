package gormstore

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	backupservice "github.com/dujiao-next/internal/modules/procurement/backupservice"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/upstream"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var boundIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// ErrGateClosed means the durable claim is still Claimed and may be retried after
// an administrator reopens the singleton purchase switch.
var ErrGateClosed = errors.New("backup procurement gate closed")

// BeginBound must be called only by a queue consumer after a trusted claim and
// immutable snapshot have been prepared. The policy callback must validate the
// live product binding, customer contract, price and FX under this same
// transaction. No callback or no gate always prevents purchase.
// The factory decrypts only the ciphertext read under the connection lock.
// Neither callback may perform network operations while the transaction is open.
func BeginBound(ctx context.Context, db *gorm.DB, claimID uint, decrypt func(string) (string, error), policy func(*gorm.DB, uint) error, now time.Time) (backupservice.Attempt, bool, error) {
	return beginBound(ctx, db, claimID, decrypt, policy, now)
}

func beginBound(ctx context.Context, db *gorm.DB, claimID uint, decrypt func(string) (string, error), policy func(*gorm.DB, uint) error, now time.Time) (backupservice.Attempt, bool, error) {
	var attempt backupservice.Attempt
	denied := errors.New("backup purchase unavailable")
	if db == nil || claimID == 0 || decrypt == nil || policy == nil || now.IsZero() {
		return attempt, false, denied
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		gate, err := lockGate(tx)
		if err != nil {
			return denied
		}
		if !gate.Enabled {
			return ErrGateClosed
		}
		claim, root, order, err := lockClaimOrder(tx, claimID)
		if err != nil || claim.State != backupstate.Claimed || claim.SubmittedAt != nil || claim.RequestSHA256 != "" || claim.ConnectionSHA256 != "" || root.DeletedAt != nil || order.DeletedAt != nil || root.RefundedAmount.IsPositive() || order.RefundedAmount.IsPositive() || (order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling) || (root.Status != constants.OrderStatusPaid && root.Status != constants.OrderStatusFulfilling && root.Status != constants.OrderStatusPartiallyDelivered && root.Status != constants.OrderStatusPartiallyRefunded) {
			return denied
		}
		var snap purchaseSnapshot
		if json.Unmarshal([]byte(claim.SnapshotJSON), &snap) != nil || snap.Version != 1 || snap.ClaimID != claim.ID || snap.LocalProductID != claim.ProductID || snap.LocalSKUID != claim.SKUID || snap.Quantity != 1 || !sha256Hex(snap.ConnectionSHA256) || !boundIdempotencyKey.MatchString(snap.IdempotencyKey) {
			return denied
		}
		limit, err := decimal.NewFromString(snap.PreflightMaxUSD)
		if err != nil || !limit.IsPositive() || strings.TrimSpace(snap.PreflightMaxUSD) != snap.PreflightMaxUSD {
			return denied
		}
		if (claim.Product == backupstate.Muse && snap.UpstreamProductID != backupstate.UpstreamMuseProductID) || (claim.Product == backupstate.Plus && snap.UpstreamProductID != backupstate.UpstreamPlusProductID) || (claim.Product != backupstate.Muse && claim.Product != backupstate.Plus) {
			return denied
		}
		if err := policy(tx, claim.ID); err != nil {
			return denied
		}
		var count int64
		if err := tx.Unscoped().Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count).Error; err != nil || count != 0 {
			return denied
		}
		var conn siteconnectiondomain.Connection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conn, snap.ConnectionID).Error; err != nil || conn.DeletedAt != nil || conn.Status != constants.ConnectionStatusActive || conn.Protocol != constants.ConnectionProtocolFangfang || conn.BaseURL != "https://canboso.com" || conn.AutoSyncPrice || conn.ApiSecret == "" {
			return denied
		}
		identity, err := json.Marshal([]string{fmt.Sprint(conn.ID), conn.Protocol, conn.BaseURL, conn.ApiKey, conn.ApiSecret})
		if err != nil {
			return denied
		}
		digest := sha256.Sum256(identity)
		expected, err := hex.DecodeString(snap.ConnectionSHA256)
		if err != nil || subtle.ConstantTimeCompare(digest[:], expected) != 1 {
			return denied
		}
		secret, err := decrypt(conn.ApiSecret)
		if err != nil {
			return denied
		}
		client, err := upstream.NewFangfangPurchaseClient(&siteconnectiondomain.Connection{ID: conn.ID, Protocol: conn.Protocol, BaseURL: conn.BaseURL, ApiKey: conn.ApiKey, ApiSecret: secret})
		if err != nil {
			return denied
		}
		requestDigest := sha256.Sum256([]byte(claim.SnapshotJSON))
		requestHash := hex.EncodeToString(requestDigest[:])
		updated := tx.Model(&backupstate.Claim{}).Where("id = ? AND state = ? AND submitted_at IS NULL AND request_sha256 = '' AND connection_sha256 = '' AND snapshot_json = ?", claim.ID, backupstate.Claimed, claim.SnapshotJSON).Updates(map[string]interface{}{"state": backupstate.Submitted, "request_sha256": requestHash, "connection_sha256": snap.ConnectionSHA256, "submitted_at": now, "updated_at": now})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return denied
		}
		attempt = backupservice.Attempt{ClaimID: claim.ID, AttemptHash: requestHash, Client: client, Request: upstream.FangfangPurchaseRequest{ProductID: snap.UpstreamProductID, Quantity: snap.Quantity, IdempotencyKey: snap.IdempotencyKey, PreflightMaxUSD: snap.PreflightMaxUSD}}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrGateClosed) {
			return backupservice.Attempt{}, false, ErrGateClosed
		}
		return backupservice.Attempt{}, false, denied
	}
	return attempt, true, nil
}
