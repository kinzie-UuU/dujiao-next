package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	receiptstore "github.com/dujiao-next/internal/modules/procurement/receipts/infrastructure/gormstore"
	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
)

func TestDeliveryBindsSavedResponseButFailsClosedWithoutVerifiedContract(t *testing.T) {
	if err := securestoreserializer.Configure("test-only-backup-receipt-encryption"); err != nil {
		t.Fatal(err)
	}
	db := claimDB(t)
	if err := db.AutoMigrate(&receiptstore.PurchaseReceipt{}); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", fmt.Sprintf(`{"version":1,"claim_id":%d,"upstream_product_id":%q,"quantity":1,"preflight_max_usd":%q}`, c.ID, backupstate.UpstreamPlusProductID, backupstate.PlusPreflightMaxUSD)).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"success":true,"delivery":{"accounts":[{"user":"test","password":"secret"}]}}`)
	if err := receiptstore.SavePurchaseReceipt(context.Background(), db, "backup_claim", c.ID, h, 200, raw); err != nil {
		t.Fatal(err)
	}
	if err := deliverReceipt(db, c.ID, strings.Repeat("b", 64)); !errors.Is(err, errReceipt) {
		t.Fatalf("mismatched request not rejected: %v", err)
	}
	if err := deliverReceipt(db, c.ID, h); !errors.Is(err, errDeliveryPolicy) {
		t.Fatalf("unverified response not rejected: %v", err)
	}
	got, status, err := receiptstore.ReadPurchaseReceipt(context.Background(), db, "backup_claim", c.ID, h)
	if err != nil || status != 200 || string(got) != string(raw) {
		t.Fatal("original receipt not preserved")
	}
	var stored backupstate.Claim
	if err := db.First(&stored, c.ID).Error; err != nil || stored.State != backupstate.Submitted {
		t.Fatal("claim state changed")
	}
}

func TestDeliveryCompletesVerifiedPlusContract(t *testing.T) {
	if err := securestoreserializer.Configure("test-only-backup-receipt-encryption"); err != nil {
		t.Fatal(err)
	}
	db := claimDB(t)
	if err := db.AutoMigrate(&receiptstore.PurchaseReceipt{}); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", fmt.Sprintf(`{"version":1,"claim_id":%d,"upstream_product_id":%q,"quantity":1,"preflight_max_usd":%q}`, c.ID, backupstate.UpstreamPlusProductID, backupstate.PlusPreflightMaxUSD)).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"success":true,"order":{"orderCode":"ORDER2","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass","otherInfo":"JBSWY3DPEHPK3PXP"}]}}`)
	if err := receiptstore.SavePurchaseReceipt(context.Background(), db, "backup_claim", c.ID, h, 200, raw); err != nil {
		t.Fatal(err)
	}
	if err := deliverReceipt(db, c.ID, h); err != nil {
		t.Fatal(err)
	}
	var stored backupstate.Claim
	if err := db.First(&stored, c.ID).Error; err != nil || stored.State != backupstate.Completed {
		t.Fatal("claim not completed")
	}
	var fulfillment fulfillmentdomain.Fulfillment
	if err := db.Where("order_id = ?", c.LocalOrderID).First(&fulfillment).Error; err != nil || !strings.Contains(fulfillment.Payload, "plus@example.com") {
		t.Fatalf("fulfillment missing: %v", err)
	}
	var order orderdomain.Order
	if err := db.First(&order, c.LocalOrderID).Error; err != nil || order.Status != "delivered" {
		t.Fatalf("order not delivered: %v", err)
	}
	if err := deliverReceipt(db, c.ID, h); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalMisboundPlusNeverFulfillsGmail(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Gmail)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("product", backupstate.Plus).Error; err != nil {
		t.Fatal(err)
	}
	if err := deliverReceipt(db, c.ID, h); !errors.Is(err, errDeliveryPolicy) {
		t.Fatalf("misbound Plus was not blocked: %v", err)
	}
	var stored backupstate.Claim
	if err := db.First(&stored, c.ID).Error; err != nil || stored.State != backupstate.Submitted {
		t.Fatal("historical claim changed")
	}
	var count int64
	db.Model(&fulfillmentdomain.Fulfillment{}).Count(&count)
	if count != 0 {
		t.Fatal("wrong product delivered")
	}
}
