package gormstore

import (
	"context"
	"errors"
	"time"

	backupservice "github.com/dujiao-next/internal/modules/procurement/backupservice"
	claimstore "github.com/dujiao-next/internal/modules/procurement/backupstate/infrastructure/gormstore"
	"gorm.io/gorm"
)

var errSnapshotUnavailable = errors.New("durable backup purchase snapshot is unavailable")

// Controller begins a bound purchase only when decrypt and policy callbacks are
// provided. Policy must re-check binding, contract, price and FX under the
// claim transaction; missing callbacks fail closed.
type Controller struct {
	DB      *gorm.DB
	Decrypt func(string) (string, error)
	Policy  func(*gorm.DB, uint) error
	Now     func() time.Time
}

var _ ClaimController = (*Controller)(nil)

func (c *Controller) Begin(ctx context.Context, claimID uint) (backupservice.Attempt, bool, error) {
	if c == nil || c.DB == nil || claimID == 0 || c.Decrypt == nil || c.Policy == nil {
		return backupservice.Attempt{}, false, errSnapshotUnavailable
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	if now.IsZero() {
		return backupservice.Attempt{}, false, errSnapshotUnavailable
	}
	return claimstore.BeginBound(ctx, c.DB, claimID, c.Decrypt, c.Policy, now)
}

func (c *Controller) MarkUncertain(ctx context.Context, attempt backupservice.Attempt) error {
	if c == nil || c.DB == nil || attempt.ClaimID == 0 {
		return errSnapshotUnavailable
	}
	return claimstore.MarkUncertain(ctx, c.DB, attempt.ClaimID, attempt.AttemptHash)
}

func (c *Controller) Deliver(ctx context.Context, attempt backupservice.Attempt, _ int, _ []byte) error {
	if c == nil || c.DB == nil || attempt.ClaimID == 0 {
		return errSnapshotUnavailable
	}
	// Ignore untrusted caller bytes and reload the exact immutable encrypted
	// response. Delivery still fails closed without a verified product contract.
	return claimstore.CheckSavedDelivery(ctx, c.DB, attempt.ClaimID, attempt.AttemptHash)
}
