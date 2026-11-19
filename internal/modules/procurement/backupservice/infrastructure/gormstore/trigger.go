package gormstore

import (
	"context"
	"errors"
	"time"

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
	if t == nil || t.DB == nil || t.Queue == nil || orderID == 0 || adminID == 0 {
		return 0, errTriggerUnavailable
	}
	now := time.Now()
	if t.Now != nil {
		now = t.Now()
	}
	var claimID uint
	err := t.DB.Transaction(func(tx *gorm.DB) error {
		var existing backupstate.Claim
		findErr := tx.Where("local_order_id = ? AND product = ?", orderID, backupstate.Plus).First(&existing).Error
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
		claim, err := claimstore.ClaimPlus(tx, orderID, adminID)
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
		// Claim+snapshot remain durable; a later TriggerPlus re-enqueues.
		return 0, errTriggerUnavailable
	}
	return claimID, nil
}
