package gormstore

import (
	"context"
	"errors"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	claimstore "github.com/dujiao-next/internal/modules/procurement/backupstate/infrastructure/gormstore"
	"github.com/dujiao-next/internal/queue"
	"gorm.io/gorm"
)

var errTriggerUnavailable = errors.New("backup procurement unavailable")

// Enqueuer queues a durable claim for the backup purchase worker.
type Enqueuer interface {
	EnqueueBackupProcurementSubmit(payload queue.BackupProcurementSubmitPayload) error
}

// PlusTrigger claims a paid Plus order, persists the immutable snapshot, then
// enqueues the bound worker. It never contacts the supplier itself.
type PlusTrigger struct {
	DB    *gorm.DB
	Queue Enqueuer
	Now   func() time.Time
}

func (t *PlusTrigger) TriggerPlusBackup(_ context.Context, orderID, adminID uint) (uint, error) {
	// Keep the historical route fail-closed. Its old pin referred to a Gmail
	// product and must not authorize another Plus purchase after rebinding.
	return 0, errTriggerUnavailable
}

// PaidTrigger claims a paid Muse/Gmail/Plus pin order after payment confirmation,
// persists the immutable snapshot, then enqueues the bound worker.
type PaidTrigger struct {
	DB    *gorm.DB
	Queue Enqueuer
	Now   func() time.Time
}

func (t *PaidTrigger) OnOrderPaid(orderID uint) error {
	if t == nil || t.DB == nil || t.Queue == nil || orderID == 0 {
		return errTriggerUnavailable
	}
	var items []orderdomain.OrderItem
	if err := t.DB.Where("order_id = ? AND deleted_at IS NULL", orderID).Find(&items).Error; err != nil {
		return errTriggerUnavailable
	}
	if len(items) != 1 {
		return nil
	}
	item := items[0]
	var product backupstate.Product
	switch {
	case item.ProductID == backupstate.LocalMuseProductID && item.SKUID == backupstate.LocalMuseSKUID:
		product = backupstate.Muse
	case item.ProductID == backupstate.LocalGmailProductID && item.SKUID == backupstate.LocalGmailSKUID:
		product = backupstate.Gmail
	case item.ProductID == backupstate.LocalPlusProductID && item.SKUID == backupstate.LocalPlusSKUID:
		eligible, err := plusPaymentAfterBinding(t.DB, orderID)
		if err != nil {
			return errTriggerUnavailable
		}
		if !eligible {
			return nil
		}
		product = backupstate.Plus
	default:
		return nil
	}
	_, err := t.claimSnapshotEnqueue(orderID, product, 0)
	return err
}

func (t *PaidTrigger) claimSnapshotEnqueue(orderID uint, product backupstate.Product, adminID uint) (uint, error) {
	return (&PlusTrigger{DB: t.DB, Queue: t.Queue, Now: t.Now}).claimSnapshotEnqueue(orderID, product, adminID)
}

func (t *PlusTrigger) claimSnapshotEnqueue(orderID uint, product backupstate.Product, adminID uint) (uint, error) {
	now := time.Now()
	if t.Now != nil {
		now = t.Now()
	}
	var claimID uint
	err := t.DB.Transaction(func(tx *gorm.DB) error {
		if product == backupstate.Plus {
			eligible, err := plusPaymentAfterBinding(tx, orderID)
			if err != nil || !eligible {
				return errTriggerUnavailable
			}
		}
		var existing backupstate.Claim
		findErr := tx.Where("local_order_id = ? AND product = ?", orderID, product).First(&existing).Error
		if findErr == nil {
			if existing.State != backupstate.Claimed || existing.SubmittedAt != nil || existing.RequestSHA256 != "" {
				return errTriggerUnavailable
			}
			if existing.SnapshotJSON == "" {
				if err := claimstore.PrepareSnapshot(tx, existing.ID, now); err != nil {
					return errTriggerUnavailable
				}
			}
			claimID = existing.ID
			return nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return errTriggerUnavailable
		}
		var claim *backupstate.Claim
		var err error
		switch product {
		case backupstate.Muse:
			claim, err = claimstore.ClaimMuse(tx, orderID)
		case backupstate.Gmail:
			claim, err = claimstore.ClaimGmail(tx, orderID)
		case backupstate.Plus:
			if adminID != 0 {
				claim, err = claimstore.ClaimPlus(tx, orderID, adminID)
			} else {
				claim, err = claimstore.ClaimPlusAutomatic(tx, orderID)
			}
		default:
			return errTriggerUnavailable
		}
		if err != nil || claim == nil || claim.ID == 0 {
			return errTriggerUnavailable
		}
		if err := claimstore.PrepareSnapshot(tx, claim.ID, now); err != nil {
			// Nested claim transaction is a savepoint under this outer TX, so a
			// snapshot failure rolls back the claim and leaves no orphan row.
			return errTriggerUnavailable
		}
		claimID = claim.ID
		return nil
	})
	if err != nil || claimID == 0 {
		return 0, errTriggerUnavailable
	}
	if err := t.Queue.EnqueueBackupProcurementSubmit(queue.BackupProcurementSubmitPayload{ClaimID: claimID}); err != nil {
		// Claim+snapshot remain durable; one immediate re-enqueue covers a blip.
		if retryErr := t.Queue.EnqueueBackupProcurementSubmit(queue.BackupProcurementSubmitPayload{ClaimID: claimID}); retryErr != nil {
			return claimID, errTriggerUnavailable
		}
	}
	return claimID, nil
}
