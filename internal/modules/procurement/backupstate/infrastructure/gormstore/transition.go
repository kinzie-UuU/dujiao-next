package gormstore

import (
	"encoding/hex"
	"errors"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"time"

	"github.com/dujiao-next/internal/constants"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errTransition = errors.New("invalid backup procurement transition")

func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// transition is intentionally package-private and is not a production entry
// point. The submitted fence must commit before any supplier request, and a
// caller must never retry a sent or uncertain request. backupstate.Completed requires the
// delivery to have already committed; it does not persist supplier credentials.
// Connection identity and exact request hash are recorded before submission.
func transition(db *gorm.DB, claimID uint, from, to backupstate.BackupState, requestSHA, connectionSHA string, now time.Time) error {
	if !((from == backupstate.Claimed && (to == backupstate.Submitted || to == backupstate.NotSent)) || (from == backupstate.Submitted && to == backupstate.Uncertain)) {
		return errTransition
	}
	if to == backupstate.Submitted && (!sha256Hex(requestSHA) || !sha256Hex(connectionSHA)) {
		return errTransition
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if to == backupstate.Submitted {
			gate, err := lockGate(tx)
			if err != nil || !gate.Enabled {
				return errGateClosed
			}
		}
		var snapshot backupstate.Claim
		if err := tx.First(&snapshot, claimID).Error; err != nil {
			return err
		}
		var initial orderdomain.Order
		if err := tx.First(&initial, snapshot.LocalOrderID).Error; err != nil {
			return err
		}
		rootID := initial.ID
		if initial.ParentID != nil {
			rootID = *initial.ParentID
		}
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&orderdomain.Order{}).Where("id = ?", rootID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}
		var root, order orderdomain.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, rootID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, initial.ID).Error; err != nil {
			return err
		}
		var current backupstate.Claim
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, claimID).Error; err != nil {
			return err
		}
		if current.State != from || current.LocalOrderID != order.ID {
			return errTransition
		}
		updates := map[string]interface{}{"state": to, "updated_at": now}
		if to == backupstate.NotSent && (current.SubmittedAt != nil || current.RequestSHA256 != "" || current.ConnectionSHA256 != "") {
			return errTransition
		}
		if to == backupstate.Submitted {
			if root.DeletedAt != nil || order.DeletedAt != nil || root.RefundedAmount.IsPositive() || order.RefundedAmount.IsPositive() || (order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling) ||
				(root.Status != constants.OrderStatusPaid && root.Status != constants.OrderStatusFulfilling && root.Status != constants.OrderStatusPartiallyDelivered && root.Status != constants.OrderStatusPartiallyRefunded) || current.SubmittedAt != nil || current.RequestSHA256 != "" || current.ConnectionSHA256 != "" {
				return errTransition
			}
			var deliveryCount int64
			if err := tx.Unscoped().Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&deliveryCount).Error; err != nil {
				return err
			}
			if deliveryCount != 0 {
				return errTransition
			}
			updates["request_sha256"] = requestSHA
			updates["connection_sha256"] = connectionSHA
			updates["submitted_at"] = now
		}
		result := tx.Model(&backupstate.Claim{}).Where("id = ? AND state = ?", claimID, from).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTransition
		}
		return nil
	})
}
