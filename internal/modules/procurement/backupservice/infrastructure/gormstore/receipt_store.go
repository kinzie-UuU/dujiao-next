package gormstore

import (
	"context"
	"errors"
	"regexp"

	backupservice "github.com/dujiao-next/internal/modules/procurement/backupservice"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	procurementstore "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	"gorm.io/gorm"
)

// ClaimController is deliberately not wired to production here. Implementations
// must enforce the durable claim fence and atomic delivery transaction described
// by Store; Deliver receives private supplier bytes and must not log them.
type ClaimController interface {
	Begin(ctx context.Context, claimID uint) (backupservice.Attempt, bool, error)
	MarkUncertain(ctx context.Context, attempt backupservice.Attempt) error
	Deliver(ctx context.Context, attempt backupservice.Attempt, httpStatus int, raw []byte) error
}

// ReceiptStore adapts encrypted receipts to Worker while delegating lifecycle
// transitions to a separately wired, transactional claim controller.
type ReceiptStore struct {
	DB         *gorm.DB
	Controller ClaimController
}

var _ backupservice.Store = (*ReceiptStore)(nil)
var validAttemptHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *ReceiptStore) Begin(ctx context.Context, claimID uint) (backupservice.Attempt, bool, error) {
	if s == nil || s.DB == nil || s.Controller == nil || claimID == 0 {
		return backupservice.Attempt{}, false, errors.New("backup claim controller unavailable")
	}
	attempt, started, err := s.Controller.Begin(ctx, claimID)
	if err != nil {
		if errors.Is(err, backupservice.ErrDeferred) {
			return backupservice.Attempt{}, false, err
		}
		return backupservice.Attempt{}, false, backupservice.ErrDenied
	}
	if started && (attempt.ClaimID != claimID || !validAttemptHash.MatchString(attempt.AttemptHash)) {
		return backupservice.Attempt{}, false, errors.New("invalid backup attempt")
	}
	if started && attempt.Client == nil {
		_ = s.Controller.MarkUncertain(ctx, attempt)
		return backupservice.Attempt{}, false, errors.New("backup client unavailable")
	}
	return attempt, started, nil
}

func (s *ReceiptStore) ResumeSubmitted(ctx context.Context, claimID uint) (backupservice.Attempt, bool, error) {
	if s == nil || s.DB == nil || claimID == 0 {
		return backupservice.Attempt{}, false, errors.New("backup claim controller unavailable")
	}
	var claim backupstate.Claim
	if err := s.DB.WithContext(ctx).First(&claim, claimID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return backupservice.Attempt{}, false, nil
		}
		return backupservice.Attempt{}, false, err
	}
	if claim.State != backupstate.Submitted {
		return backupservice.Attempt{}, false, nil
	}
	if !validAttemptHash.MatchString(claim.RequestSHA256) {
		// Submitted fence without a usable attempt hash must not fall through to Begin.
		return backupservice.Attempt{}, false, errors.New("submitted backup claim missing attempt hash")
	}
	var count int64
	if err := s.DB.WithContext(ctx).Model(&procurementstore.PurchaseReceipt{}).
		Where("owner_kind = ? AND owner_id = ? AND attempt_hash = ?", "backup_claim", claim.ID, claim.RequestSHA256).
		Count(&count).Error; err != nil {
		return backupservice.Attempt{}, false, err
	}
	if count == 0 {
		// Submitted before SaveReceipt; never repurchase via Begin.
		return backupservice.Attempt{}, false, errors.New("submitted backup claim missing receipt")
	}
	return backupservice.Attempt{ClaimID: claim.ID, AttemptHash: claim.RequestSHA256}, true, nil
}

func (s *ReceiptStore) SaveReceipt(ctx context.Context, attempt backupservice.Attempt, httpStatus int, raw []byte) error {
	if s == nil || s.DB == nil || attempt.ClaimID == 0 {
		return errors.New("backup receipt storage unavailable")
	}
	if err := procurementstore.SavePurchaseReceipt(ctx, s.DB, "backup_claim", attempt.ClaimID, attempt.AttemptHash, httpStatus, raw); err != nil {
		return errors.New("backup receipt persistence failed")
	}
	return nil
}

func (s *ReceiptStore) MarkUncertain(ctx context.Context, attempt backupservice.Attempt) error {
	if s == nil || s.Controller == nil || attempt.ClaimID == 0 {
		return errors.New("backup claim controller unavailable")
	}
	if err := s.Controller.MarkUncertain(ctx, attempt); err != nil {
		return errors.New("backup claim reconciliation update failed")
	}
	return nil
}

func (s *ReceiptStore) DeliverSaved(ctx context.Context, attempt backupservice.Attempt) error {
	if s == nil || s.DB == nil || s.Controller == nil || attempt.ClaimID == 0 {
		return errors.New("backup delivery controller unavailable")
	}
	raw, status, err := procurementstore.ReadPurchaseReceipt(ctx, s.DB, "backup_claim", attempt.ClaimID, attempt.AttemptHash)
	if err != nil {
		return errors.New("backup saved receipt unavailable")
	}
	if err := s.Controller.Deliver(ctx, attempt, status, raw); err != nil {
		return errors.New("backup saved delivery failed")
	}
	return nil
}
