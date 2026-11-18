package gormstore

import (
	"context"
	"errors"
	"time"

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
	claim, err := claimstore.ClaimPlus(t.DB, orderID, adminID)
	if err != nil || claim == nil || claim.ID == 0 {
		return 0, errTriggerUnavailable
	}
	if err := claimstore.PrepareSnapshot(t.DB, claim.ID, now); err != nil {
		return 0, errTriggerUnavailable
	}
	if err := t.Queue.EnqueueBackupProcurementSubmit(queue.BackupProcurementSubmitPayload{ClaimID: claim.ID}); err != nil {
		return 0, errTriggerUnavailable
	}
	return claim.ID, nil
}
