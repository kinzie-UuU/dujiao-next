package gormstore

import (
	"context"
	"errors"
	"time"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"gorm.io/gorm"
)

// MarkUncertain fences a previously submitted exact request. It cannot reopen,
// retry, cancel, refund or deliver it. No credentials are accepted or logged.
func MarkUncertain(ctx context.Context, db *gorm.DB, id uint, requestSHA string) error {
	if db == nil || !sha256Hex(requestSHA) {
		return errors.New("invalid backup attempt")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c, _, _, err := lockClaimOrder(tx, id)
		if err != nil {
			return errors.New("backup attempt unavailable")
		}
		if c.State != backupstate.Submitted || c.SubmittedAt == nil || c.RequestSHA256 != requestSHA {
			return errors.New("backup attempt state invalid")
		}
		result := tx.Model(&backupstate.Claim{}).Where("id = ? AND state = ? AND request_sha256 = ?", id, backupstate.Submitted, requestSHA).Updates(map[string]interface{}{"state": backupstate.Uncertain, "updated_at": time.Now()})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.New("backup attempt state invalid")
		}
		return nil
	})
}

// CheckSavedDelivery loads the authoritative immutable receipt and continues
// to reject fulfillment until the customer's delivery contract is verified.
func CheckSavedDelivery(ctx context.Context, db *gorm.DB, id uint, requestSHA string) error {
	if db == nil || !sha256Hex(requestSHA) {
		return errReceipt
	}
	return deliverReceipt(db.WithContext(ctx), id, requestSHA)
}
