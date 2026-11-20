package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"

	"github.com/dujiao-next/internal/constants"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	receiptstore "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errReceipt = errors.New("backup receipt invalid or unavailable")
var errDeliveryPolicy = errors.New("backup delivery contract not verified")

// lockClaimOrder uses the same parent-then-child locking order as refunds and
// procurement submissions. SQLite needs an explicit write before reading.
func lockClaimOrder(tx *gorm.DB, id uint) (*backupstate.Claim, *orderdomain.Order, *orderdomain.Order, error) {
	var initial backupstate.Claim
	if err := tx.First(&initial, id).Error; err != nil {
		return nil, nil, nil, err
	}
	var snapshot orderdomain.Order
	if err := tx.First(&snapshot, initial.LocalOrderID).Error; err != nil {
		return nil, nil, nil, err
	}
	rootID := snapshot.ID
	if snapshot.ParentID != nil {
		rootID = *snapshot.ParentID
	}
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&orderdomain.Order{}).Where("id = ?", rootID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return nil, nil, nil, err
		}
	}
	var root, order orderdomain.Order
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, rootID).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, snapshot.ID).Error; err != nil {
		return nil, nil, nil, err
	}
	var c backupstate.Claim
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, id).Error; err != nil {
		return nil, nil, nil, err
	}
	if c.LocalOrderID != order.ID {
		return nil, nil, nil, errReceipt
	}
	return &c, &root, &order, nil
}

// deliverReceipt checks that the immutable response belongs to the exact
// persisted request, verifies the product-specific account contract, then
// creates fulfillment and completes the claim in one transaction.
func deliverReceipt(db *gorm.DB, id uint, attemptHash string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		c, root, order, err := lockClaimOrder(tx, id)
		if err != nil {
			return errReceipt
		}
		if c.State == backupstate.Completed {
			var count int64
			if err := tx.Unscoped().Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count).Error; err != nil || count != 1 {
				return errReceipt
			}
			return nil
		}
		if c.State != backupstate.Submitted || c.SubmittedAt == nil || !sha256Hex(c.RequestSHA256) || !sha256Hex(c.ConnectionSHA256) || attemptHash != c.RequestSHA256 || root.DeletedAt != nil || order.DeletedAt != nil || root.RefundedAmount.IsPositive() || order.RefundedAmount.IsPositive() || (order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling) || (root.Status != constants.OrderStatusPaid && root.Status != constants.OrderStatusFulfilling && root.Status != constants.OrderStatusPartiallyDelivered && root.Status != constants.OrderStatusPartiallyRefunded) {
			return errReceipt
		}
		// The historical misbound Plus purchase is not a Gmail fulfillment.
		// Keep its immutable receipt for reconciliation without delivering it to this order.
		if c.Product == backupstate.Plus && (c.ProductID == backupstate.LocalGmailProductID || c.SKUID == backupstate.LocalGmailSKUID) {
			return errDeliveryPolicy
		}
		raw, status, err := receiptstore.ReadPurchaseReceipt(context.Background(), tx, "backup_claim", c.ID, attemptHash)
		if err != nil || status != 200 || len(raw) == 0 {
			return errReceipt
		}
		var snap purchaseSnapshot
		if json.Unmarshal([]byte(c.SnapshotJSON), &snap) != nil || snap.Version != 1 || snap.ClaimID != c.ID || snap.Quantity != 1 {
			return errDeliveryPolicy
		}
		payload, err := parseVerifiedDeliveryPayload(c.Product, snap.UpstreamProductID, snap.Quantity, snap.PreflightMaxUSD, raw)
		if err != nil {
			return errDeliveryPolicy
		}
		var count int64
		if err := tx.Unscoped().Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count).Error; err != nil || count != 0 {
			return errReceipt
		}
		now := time.Now()
		deliveredAt := now
		if err := tx.Create(&fulfillmentdomain.Fulfillment{
			OrderID:     order.ID,
			Type:        constants.FulfillmentTypeUpstream,
			Status:      constants.FulfillmentStatusDelivered,
			Payload:     payload,
			DeliveredAt: &deliveredAt,
			CreatedAt:   now,
			UpdatedAt:   now,
		}).Error; err != nil {
			return errReceipt
		}
		orderUpdate := tx.Model(&orderdomain.Order{}).Where("id = ? AND status IN ? AND deleted_at IS NULL", order.ID, []string{constants.OrderStatusPaid, constants.OrderStatusFulfilling}).Updates(map[string]interface{}{"status": constants.OrderStatusDelivered, "updated_at": now})
		if orderUpdate.Error != nil || orderUpdate.RowsAffected != 1 {
			return errReceipt
		}
		claimUpdate := tx.Model(&backupstate.Claim{}).Where("id = ? AND state = ? AND request_sha256 = ?", c.ID, backupstate.Submitted, attemptHash).Updates(map[string]interface{}{"state": backupstate.Completed, "updated_at": now})
		if claimUpdate.Error != nil || claimUpdate.RowsAffected != 1 {
			return errReceipt
		}
		return nil
	})
}
