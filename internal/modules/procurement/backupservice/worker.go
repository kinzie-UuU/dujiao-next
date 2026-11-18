// Package backupservice contains the fail-closed backup purchase worker.
// It is not registered as a production task until its ports are wired and
// tested against the real transaction and authorization boundaries.
package backupservice

import (
	"context"
	"errors"

	"github.com/dujiao-next/internal/upstream"
)

var ErrReconcile = errors.New("backup purchase requires reconciliation")

// Attempt contains a durable request snapshot; it must not contain a buyer key.
type Attempt struct {
	ClaimID     uint
	AttemptHash string
	Request     upstream.FangfangPurchaseRequest
	// Client is constructed from the exact locked connection snapshot by Begin.
	// It is ephemeral and must never be serialized or placed in a queue payload.
	Client Client `json:"-"`
}

type Store interface {
	// Begin atomically fences the order and transitions claimed to submitted.
	// A false result acknowledges a duplicate without contacting the supplier.
	Begin(ctx context.Context, claimID uint) (Attempt, bool, error)
	SaveReceipt(ctx context.Context, attempt Attempt, httpStatus int, raw []byte) error
	MarkUncertain(ctx context.Context, attempt Attempt) error
	// DeliverSaved must read the encrypted receipt and atomically create delivery,
	// complete the claim and advance the order. It must never purchase again.
	DeliverSaved(ctx context.Context, attempt Attempt) error
}

type Client interface {
	PurchaseOnce(context.Context, upstream.FangfangPurchaseRequest) (*upstream.FangfangPurchaseResult, error)
}

type Worker struct {
	Store Store
}

func (w Worker) Run(ctx context.Context, claimID uint) error {
	if w.Store == nil || claimID == 0 {
		return errors.New("invalid backup worker")
	}
	attempt, started, err := w.Store.Begin(ctx, claimID)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	if attempt.Client == nil || attempt.ClaimID != claimID {
		_ = w.Store.MarkUncertain(ctx, attempt)
		return ErrReconcile
	}
	result, purchaseErr := attempt.Client.PurchaseOnce(ctx, attempt.Request)
	if result != nil {
		// Store every response, including malformed and over-threshold results,
		// before interpreting it. Never put its contents in an error or log.
		if err := w.Store.SaveReceipt(ctx, attempt, result.HTTPStatus, result.RawResponse); err != nil {
			_ = w.Store.MarkUncertain(ctx, attempt)
			return ErrReconcile
		}
	}
	if purchaseErr != nil || result == nil {
		_ = w.Store.MarkUncertain(ctx, attempt)
		return ErrReconcile
	}
	if err := w.Store.DeliverSaved(ctx, attempt); err != nil {
		// Durable response remains available to a delivery-only recovery task.
		return errors.New("saved backup delivery requires recovery")
	}
	return nil
}
