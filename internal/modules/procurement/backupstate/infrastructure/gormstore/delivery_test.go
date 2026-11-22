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
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", fmt.Sprintf(`{"version":1,"claim_id":%d,"upstream_product_id":%q,"quantity":1,"preflight_max_usd":%q}`, c.ID, backupstate.UpstreamPlusProductID, "7.00")).Error; err != nil {
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
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", fmt.Sprintf(`{"version":1,"claim_id":%d,"upstream_product_id":%q,"quantity":1,"preflight_max_usd":%q}`, c.ID, backupstate.UpstreamPlusProductID, "7.00")).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"success":true,"order":{"orderCode":"ORDER2","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass","verifyEmail":"JBSWY3DPEHPK3PXP","otherInfo":null,"expiryText":null}]}}`)
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

func TestGmailDeliverySynchronizesParentWithoutDuplicateFulfillment(t *testing.T) {
	for _, tc := range []struct{ name, sibling, want string }{{"only child", "", "delivered"}, {"other paid", "paid", "partially_delivered"}, {"other refunded", "refunded", "partially_refunded"}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := securestoreserializer.Configure("test-only-gmail-parent-receipt"); err != nil {
				t.Fatal(err)
			}
			db := claimDB(t)
			if err := db.AutoMigrate(&receiptstore.PurchaseReceipt{}); err != nil {
				t.Fatal(err)
			}
			in := seedClaim(t, db, backupstate.Gmail)
			parent := orderdomain.Order{OrderNo: "parent", Status: "paid", Currency: "CNY"}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&orderdomain.Order{}).Where("id = ?", in.orderID).Update("parent_id", parent.ID).Error; err != nil {
				t.Fatal(err)
			}
			if tc.sibling != "" {
				if err := db.Create(&orderdomain.Order{OrderNo: "sibling", ParentID: &parent.ID, Status: tc.sibling, Currency: "CNY"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			c, err := claim(db, in)
			if err != nil {
				t.Fatal(err)
			}
			h := strings.Repeat("a", 64)
			if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); err != nil {
				t.Fatal(err)
			}
			snap := fmt.Sprintf(`{"version":1,"claim_id":%d,"upstream_product_id":%q,"quantity":1,"preflight_max_usd":"1.49"}`, c.ID, backupstate.UpstreamGmailProductID)
			if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", snap).Error; err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"success":true,"order":{"orderCode":"ORDER_GMAIL_PARENT","status":"completed","productId":"6ae37dc5ed8e0e40e4de28b7","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":1.49,"currency":"USD"},"delivery":{"accounts":[{"user":"aged@gmail.com","password":"synthetic","verifyEmail":"recovery@example.net"}]}}`)
			if err := receiptstore.SavePurchaseReceipt(context.Background(), db, "backup_claim", c.ID, h, 200, raw); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := deliverReceipt(db, c.ID, h); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(&parent, parent.ID).Error; err != nil || parent.Status != tc.want {
				t.Fatalf("parent status=%s error=%v", parent.Status, err)
			}
			var count int64
			db.Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", c.LocalOrderID).Count(&count)
			if count != 1 {
				t.Fatal("duplicate fulfillment")
			}
			for _, terminal := range []string{"canceled", "refunded"} {
				if err := db.Model(&parent).Update("status", terminal).Error; err != nil {
					t.Fatal(err)
				}
				if err := deliverReceipt(db, c.ID, h); err != nil {
					t.Fatal(err)
				}
				if err := db.First(&parent, parent.ID).Error; err != nil || parent.Status != terminal {
					t.Fatal("terminal parent was overwritten")
				}
			}
		})
	}
}
