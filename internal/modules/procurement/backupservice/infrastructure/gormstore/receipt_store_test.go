package gormstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	backupservice "github.com/dujiao-next/internal/modules/procurement/backupservice"
	"strings"
	"testing"
	"time"

	procurementstore "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
	"github.com/dujiao-next/internal/upstream"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type receiptControllerStub struct {
	attempt    backupservice.Attempt
	status     int
	raw        []byte
	deliverErr error
}

type receiptClientStub struct{}

func (receiptClientStub) PurchaseOnce(context.Context, upstream.FangfangPurchaseRequest) (*upstream.FangfangPurchaseResult, error) {
	return nil, errors.New("not called")
}

func (s *receiptControllerStub) Begin(_ context.Context, _ uint) (backupservice.Attempt, bool, error) {
	return s.attempt, true, nil
}
func (s *receiptControllerStub) MarkUncertain(context.Context, backupservice.Attempt) error {
	return nil
}
func (s *receiptControllerStub) Deliver(_ context.Context, _ backupservice.Attempt, status int, raw []byte) error {
	s.status, s.raw = status, append([]byte(nil), raw...)
	return s.deliverErr
}

func TestReceiptStorePrivateRoundTrip(t *testing.T) {
	if err := securestoreserializer.Configure("receipt-adapter-test-key"); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:receipt_adapter_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&procurementstore.PurchaseReceipt{}); err != nil {
		t.Fatal(err)
	}
	controller := &receiptControllerStub{attempt: backupservice.Attempt{ClaimID: 77, AttemptHash: strings.Repeat("a", 64), Client: receiptClientStub{}}}
	store := &ReceiptStore{DB: db, Controller: controller}
	ctx := context.Background()
	attempt, started, err := store.Begin(ctx, 77)
	if err != nil || !started {
		t.Fatal("begin failed")
	}
	secret := []byte(`{"accounts":[{"password":"sensitive-receipt"}]}`)
	if err := store.SaveReceipt(ctx, attempt, 200, secret); err != nil {
		t.Fatal(err)
	}
	var saved procurementstore.PurchaseReceipt
	if err := db.Take(&saved).Error; err != nil || !securestoreserializer.IsEncryptedString(saved.ResponseCiphertext) || strings.Contains(saved.ResponseCiphertext, "sensitive-receipt") {
		t.Fatal("receipt not encrypted")
	}
	if err := store.SaveReceipt(ctx, attempt, 500, []byte("replacement")); err == nil {
		t.Fatal("duplicate receipt replaced")
	}
	if err := store.DeliverSaved(ctx, attempt); err != nil || controller.status != 200 || !bytes.Equal(controller.raw, secret) {
		t.Fatal("encrypted receipt not delivered")
	}
	controller.deliverErr = errors.New("sensitive-receipt")
	if err := store.DeliverSaved(ctx, attempt); err == nil || strings.Contains(err.Error(), "sensitive-receipt") {
		t.Fatal("sensitive controller error exposed")
	}
	controller.attempt.ClaimID = 78
	if _, _, err := store.Begin(ctx, 77); err == nil {
		t.Fatal("mismatched claim accepted")
	}
	controller.attempt.ClaimID, controller.attempt.AttemptHash = 77, "invalid"
	if _, _, err := store.Begin(ctx, 77); err == nil {
		t.Fatal("invalid attempt hash accepted")
	}
	controller.attempt.AttemptHash, controller.attempt.Client = strings.Repeat("a", 64), nil
	if _, _, err := store.Begin(ctx, 77); err == nil {
		t.Fatal("unbound client accepted")
	}
}
