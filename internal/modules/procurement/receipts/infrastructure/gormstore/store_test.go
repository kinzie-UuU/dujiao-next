package gormstore_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	store "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func receiptTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	if err := securestoreserializer.Configure("purchase-receipt-test-key"); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:receipt_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&store.PurchaseReceipt{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPurchaseReceiptSQLLoggingNeverIncludesPlaintext(t *testing.T) {
	db := receiptTestDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Info})})
	const secret = "unique-sensitive-purchase-receipt"
	raw := []byte(secret)
	if err := store.SavePurchaseReceipt(context.Background(), db, "procurement", 8, strings.Repeat("d", 64), 200, raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), base64.StdEncoding.EncodeToString(raw)) {
		t.Fatal("SQL logger exposed plaintext receipt")
	}
}

func TestPurchaseReceiptEncryptedAndPrivate(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	attempt := strings.Repeat("a", 64)
	raw := []byte(`{"accounts":[{"password":"private-test-secret"}]}`)
	// No procurement order/status exists; saving must be independent of state.
	if err := store.SavePurchaseReceipt(ctx, db, "procurement", 4, attempt, 200, raw); err != nil {
		t.Fatal(err)
	}
	var record store.PurchaseReceipt
	if err := db.Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	if !securestoreserializer.IsEncryptedString(record.ResponseCiphertext) || strings.Contains(record.ResponseCiphertext, "private-test-secret") {
		t.Fatal("response stored in plaintext")
	}
	public, err := json.Marshal(record)
	if err != nil || strings.Contains(string(public), "private-test-secret") || strings.Contains(string(public), record.ResponseCiphertext) || strings.Contains(string(public), attempt) {
		t.Fatal("private receipt exposed in JSON")
	}
	got, status, err := store.ReadPurchaseReceipt(ctx, db, "procurement", 4, attempt)
	if err != nil || status != 200 || !bytes.Equal(got, raw) {
		t.Fatal("encrypted response did not round trip")
	}
	if _, _, err := store.ReadPurchaseReceipt(ctx, db, "procurement", 5, attempt); err == nil {
		t.Fatal("wrong order could read receipt")
	}
	if err := store.SavePurchaseReceipt(ctx, db, "procurement", 4, strings.Repeat("b", 64), 503, nil); err != nil {
		t.Fatal(err)
	}
	got, status, err = store.ReadPurchaseReceipt(ctx, db, "procurement", 4, strings.Repeat("b", 64))
	if err != nil || status != 503 || len(got) != 0 {
		t.Fatal("empty response did not round trip")
	}
}

func TestPurchaseReceiptConcurrentNoOverwrite(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	attempt := strings.Repeat("c", 64)
	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- store.SavePurchaseReceipt(ctx, db, "procurement", 7, attempt, 200, []byte(fmt.Sprintf("private-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, store.ErrPurchaseReceiptExists) {
			conflicts++
		} else {
			t.Fatalf("unexpected persistence error: %v", err)
		}
	}
	if success != 1 || conflicts != n-1 {
		t.Fatalf("success=%d conflict=%d", success, conflicts)
	}
	before, _, err := store.ReadPurchaseReceipt(ctx, db, "procurement", 7, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePurchaseReceipt(ctx, db, "procurement", 7, attempt, 500, []byte("replacement")); !errors.Is(err, store.ErrPurchaseReceiptExists) {
		t.Fatal("duplicate attempt overwritten")
	}
	after, _, err := store.ReadPurchaseReceipt(ctx, db, "procurement", 7, attempt)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("receipt overwritten")
	}
}

func TestPurchaseReceiptOwnerNamespaces(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	attempt := strings.Repeat("e", 64)
	for _, item := range []struct {
		kind string
		id   uint
		raw  string
	}{
		{"procurement", 9, "primary"},
		{"backup_claim", 9, "backup"},
		{"backup_claim", 10, "another-backup"},
	} {
		if err := store.SavePurchaseReceipt(ctx, db, item.kind, item.id, attempt, 200, []byte(item.raw)); err != nil {
			t.Fatal(err)
		}
		got, _, err := store.ReadPurchaseReceipt(ctx, db, item.kind, item.id, attempt)
		if err != nil || string(got) != item.raw {
			t.Fatal("wrong receipt namespace")
		}
	}
	for _, invalid := range []struct {
		kind string
		id   uint
	}{{"", 9}, {"order", 9}, {"backup_claim", 0}} {
		if err := store.SavePurchaseReceipt(ctx, db, invalid.kind, invalid.id, attempt, 200, nil); err == nil {
			t.Fatal("invalid receipt owner admitted")
		}
		if _, _, err := store.ReadPurchaseReceipt(ctx, db, invalid.kind, invalid.id, attempt); err == nil {
			t.Fatal("invalid receipt owner read")
		}
	}
}
