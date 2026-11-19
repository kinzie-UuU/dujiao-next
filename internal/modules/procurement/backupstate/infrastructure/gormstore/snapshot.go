package gormstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dujiao-next/internal/constants"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// purchaseSnapshot contains no buyer key. PreflightMaxUSD is an estimate-only
// guard, not a guaranteed transaction limit: the upstream API has no price cap.
type purchaseSnapshot struct {
	Version           int    `json:"version"`
	ClaimID           uint   `json:"claim_id"`
	ConnectionID      uint   `json:"connection_id"`
	ConnectionSHA256  string `json:"connection_sha256"`
	LocalProductID    uint   `json:"local_product_id"`
	LocalSKUID        uint   `json:"local_sku_id"`
	UpstreamProductID string `json:"upstream_product_id"`
	Quantity          int    `json:"quantity"`
	PreflightMaxUSD   string `json:"preflight_max_usd"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// PrepareSnapshot persists trusted binding/order facts only. It does not submit
// or authorize a supplier request; a client identity fence is still required.
func PrepareSnapshot(db *gorm.DB, id uint, now time.Time) error {
	return prepareSnapshot(db, id, now)
}

func prepareSnapshot(db *gorm.DB, id uint, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		c, root, order, err := lockClaimOrder(tx, id)
		if err != nil {
			return errors.New("snapshot unavailable")
		}
		if c.State != backupstate.Claimed || c.SnapshotJSON != "" || c.SubmittedAt != nil || c.RequestSHA256 != "" || c.ConnectionSHA256 != "" || root.DeletedAt != nil || order.DeletedAt != nil || root.RefundedAmount.IsPositive() || order.RefundedAmount.IsPositive() || (order.Status != "paid" && order.Status != "fulfilling") {
			return errors.New("snapshot unavailable")
		}
		var items []orderdomain.OrderItem
		if err := tx.Where("order_id = ? AND deleted_at IS NULL", order.ID).Find(&items).Error; err != nil || len(items) != 1 || items[0].ProductID != c.ProductID || items[0].SKUID != c.SKUID || items[0].Quantity != 1 {
			return errors.New("snapshot unavailable")
		}
		var bindings []mappingdomain.ReadOnlyBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("local_product_id = ? AND local_sku_id = ?", c.ProductID, c.SKUID).Find(&bindings).Error; err != nil || len(bindings) != 1 {
			return errors.New("snapshot unavailable")
		}
		expected, limit := "", ""
		switch c.Product {
		case backupstate.Muse:
			expected, limit = backupstate.UpstreamMuseProductID, backupstate.MusePreflightMaxUSD
		case backupstate.Plus:
			expected, limit = backupstate.UpstreamPlusProductID, backupstate.PlusPreflightMaxUSD
		default:
			return errors.New("snapshot unavailable")
		}
		b := bindings[0]
		if b.UpstreamSKUCode != expected || b.Currency != "USD" {
			return errors.New("snapshot unavailable")
		}
		var conn siteconnectiondomain.Connection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conn, b.ConnectionID).Error; err != nil || conn.DeletedAt != nil || conn.Protocol != constants.ConnectionProtocolFangfang || conn.BaseURL != "https://canboso.com" || conn.ApiSecret == "" || conn.AutoSyncPrice {
			return errors.New("snapshot unavailable")
		}
		// Digest includes the stored ciphertext, not an unkeyed digest of the secret.
		// A later credential rotation changes the connection identity and must stop.
		identity, err := json.Marshal([]string{fmt.Sprint(conn.ID), conn.Protocol, conn.BaseURL, conn.ApiKey, conn.ApiSecret})
		if err != nil {
			return errors.New("snapshot unavailable")
		}
		identityHash := sha256.Sum256(identity)
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return errors.New("snapshot unavailable")
		}
		snap := purchaseSnapshot{Version: 1, ClaimID: c.ID, ConnectionID: conn.ID, ConnectionSHA256: hex.EncodeToString(identityHash[:]), LocalProductID: c.ProductID, LocalSKUID: c.SKUID, UpstreamProductID: expected, Quantity: 1, PreflightMaxUSD: limit, IdempotencyKey: "ff-" + hex.EncodeToString(token)}
		encoded, err := json.Marshal(snap)
		if err != nil {
			return errors.New("snapshot unavailable")
		}
		result := tx.Model(&backupstate.Claim{}).Where("id = ? AND state = ? AND snapshot_json = '' AND submitted_at IS NULL", c.ID, backupstate.Claimed).Updates(map[string]interface{}{"snapshot_json": string(encoded), "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.New("snapshot unavailable")
		}
		return nil
	})
}
