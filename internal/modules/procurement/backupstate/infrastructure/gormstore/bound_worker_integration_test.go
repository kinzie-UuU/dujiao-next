package gormstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	backupservice "github.com/dujiao-next/internal/modules/procurement/backupservice"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	receiptstore "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
	"github.com/dujiao-next/internal/upstream"
	"gorm.io/gorm"
)

type syntheticBoundClient struct{ calls *int }

func (c syntheticBoundClient) PurchaseOnce(context.Context, upstream.FangfangPurchaseRequest) (*upstream.FangfangPurchaseResult, error) {
	*c.calls++
	return &upstream.FangfangPurchaseResult{HTTPStatus: 200, RawResponse: []byte(`{"delivery":"synthetic-secret"}`)}, nil
}

type syntheticController struct {
	db    *gorm.DB
	calls *int
}

func (c syntheticController) Begin(ctx context.Context, id uint) (backupservice.Attempt, bool, error) {
	attempt, started, err := beginBound(ctx, c.db, id, func(string) (string, error) { return "synthetic-key", nil }, func(*gorm.DB, uint) error { return nil }, time.Now())
	if err == nil && started {
		attempt.Client = syntheticBoundClient{calls: c.calls}
	}
	return attempt, started, err
}
func (c syntheticController) MarkUncertain(ctx context.Context, attempt backupservice.Attempt) error {
	return MarkUncertain(ctx, c.db, attempt.ClaimID, attempt.AttemptHash)
}
func (c syntheticController) SaveReceipt(ctx context.Context, attempt backupservice.Attempt, status int, raw []byte) error {
	return receiptstore.SavePurchaseReceipt(ctx, c.db, "backup_claim", attempt.ClaimID, attempt.AttemptHash, status, raw)
}
func (c syntheticController) DeliverSaved(ctx context.Context, attempt backupservice.Attempt) error {
	return CheckSavedDelivery(ctx, c.db, attempt.ClaimID, attempt.AttemptHash)
}

// This synthetic test makes no external request and deliberately keeps customer
// delivery blocked until the supplier's account contract is confirmed.
func TestBoundWorkerPersistsReceiptAndNeverRepurchases(t *testing.T) {
	if err := securestoreserializer.Configure("synthetic-backup-test-key"); err != nil {
		t.Fatal(err)
	}
	db := claimDB(t)
	if err := db.AutoMigrate(&mappingdomain.ReadOnlyBinding{}, &siteconnectiondomain.Connection{}, &receiptstore.PurchaseReceipt{}); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	claimRow, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	conn := siteconnectiondomain.Connection{ID: 4, Name: "synthetic", BaseURL: "https://canboso.com", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusActive, ApiKey: "marker", ApiSecret: "synthetic-ciphertext"}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mappingdomain.ReadOnlyBinding{ConnectionID: 4, LocalProductID: in.productID, LocalSKUID: in.skuID, UpstreamProductID: 2, UpstreamSKUCode: "6a4b4d7d1827db4ca39bb8be", Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareSnapshot(db, claimRow.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	controller := syntheticController{db: db, calls: &calls}
	worker := backupservice.Worker{Store: controller}
	if err := worker.Run(context.Background(), claimRow.ID); err == nil {
		t.Fatal("unverified delivery contract accepted")
	}
	if calls != 1 {
		t.Fatalf("supplier calls: %d", calls)
	}
	var saved backupstate.Claim
	if err := db.First(&saved, claimRow.ID).Error; err != nil || saved.State != backupstate.Submitted || saved.RequestSHA256 == "" {
		t.Fatalf("submission fence missing: %v", err)
	}
	var receipt receiptstore.PurchaseReceipt
	if err := db.First(&receipt).Error; err != nil || strings.Contains(receipt.ResponseCiphertext, "synthetic-secret") || !securestoreserializer.IsEncryptedString(receipt.ResponseCiphertext) {
		t.Fatal("encrypted receipt missing")
	}
	if err := worker.Run(context.Background(), claimRow.ID); err == nil || calls != 1 {
		t.Fatal("replayed submitted purchase")
	}
	if err := CheckSavedDelivery(context.Background(), db, claimRow.ID, saved.RequestSHA256); !errors.Is(err, errDeliveryPolicy) {
		t.Fatal("unverified delivery was not blocked")
	}
}
