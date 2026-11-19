package gormstore

import (
	"fmt"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"testing"
	"time"

	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func claimDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:claim_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{}, &backupstate.Claim{}, &backupstate.Gate{}, &backupstate.GateAudit{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Gate{ID: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}
func seedClaim(t *testing.T, db *gorm.DB, product backupstate.Product) claimInput {
	t.Helper()
	order := orderdomain.Order{OrderNo: "order", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	productID, skuID := uint(backupstate.LocalMuseProductID), uint(backupstate.LocalMuseSKUID)
	if product == backupstate.Plus {
		productID, skuID = backupstate.LocalPlusProductID, backupstate.LocalPlusSKUID
	}
	item := orderdomain.OrderItem{OrderID: order.ID, ProductID: productID, SKUID: skuID, Quantity: 1, FulfillmentType: "upstream", TitleJSON: map[string]interface{}{"zh-CN": "test"}}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	in := claimInput{orderID: order.ID, product: product, productID: productID, skuID: skuID, trigger: backupstate.Automatic}
	if product == backupstate.Muse {
		proc := procurementdomain.Order{LocalOrderID: order.ID, ConnectionID: 2, Status: "pending", SubmissionVersion: 1, Currency: "CNY"}
		if err := db.Create(&proc).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		in.trigger = backupstate.Manual
		in.adminID = 1
	}
	return in
}
func TestClaimIsUniqueAndBindsVerifiedOrder(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Muse)
	got, err := claim(db, in)
	if err != nil || got.State != backupstate.Claimed || got.PrimaryOrderID == 0 {
		t.Fatalf("claim=%v err=%v", got, err)
	}
	if _, err := claim(db, in); err == nil {
		t.Fatal("duplicate claim accepted")
	}
	if err := db.Create(&backupstate.Claim{LocalOrderID: in.orderID, Product: backupstate.Muse, State: backupstate.Claimed}).Error; err == nil {
		t.Fatal("database unique constraint not enforced")
	}
}
func TestClaimRejectsUnsafeSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, claimInput)
	}{
		{"unpaid", func(t *testing.T, db *gorm.DB, in claimInput) {
			db.Model(&orderdomain.Order{}).Where("id = ?", in.orderID).Update("status", "pending")
		}},
		{"legacy", func(t *testing.T, db *gorm.DB, in claimInput) {
			db.Model(&procurementdomain.Order{}).Where("local_order_id = ?", in.orderID).Update("submission_version", 0)
		}},
		{"submitted", func(t *testing.T, db *gorm.DB, in claimInput) {
			db.Model(&procurementdomain.Order{}).Where("local_order_id = ?", in.orderID).Update("status", "submitted")
		}},
		{"submission hash", func(t *testing.T, db *gorm.DB, in claimInput) {
			db.Model(&procurementdomain.Order{}).Where("local_order_id = ?", in.orderID).Update("submission_request_sha256", "abc")
		}},
		{"delivered", func(t *testing.T, db *gorm.DB, in claimInput) {
			if err := db.Create(&fulfillmentdomain.Fulfillment{OrderID: in.orderID, Type: "manual", Status: "delivered"}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong sku", func(t *testing.T, db *gorm.DB, in claimInput) {
			db.Model(&orderdomain.OrderItem{}).Where("order_id = ?", in.orderID).Update("sku_id", 71)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := claimDB(t)
			in := seedClaim(t, db, backupstate.Muse)
			tt.mutate(t, db, in)
			if _, err := claim(db, in); err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
}
func TestManualPlusRequiresAdministrator(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	in.adminID = 0
	if _, err := claim(db, in); err == nil {
		t.Fatal("unauthorized claim")
	}
	in.adminID = 1
	if _, err := claim(db, in); err != nil {
		t.Fatal(err)
	}
}

func TestPaidPinsAllowNoPrimaryAutomaticClaim(t *testing.T) {
	db := claimDB(t)
	order := orderdomain.Order{OrderNo: "muse-paid", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: backupstate.LocalMuseProductID, SKUID: backupstate.LocalMuseSKUID, Quantity: 1, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "test"}}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := ClaimMuse(db, order.ID)
	if err != nil || got == nil || got.PrimaryOrderID != 0 || got.State != backupstate.Claimed {
		t.Fatalf("muse no-primary claim=%v err=%v", got, err)
	}

	db = claimDB(t)
	order = orderdomain.Order{OrderNo: "plus-paid", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: backupstate.LocalPlusProductID, SKUID: backupstate.LocalPlusSKUID, Quantity: 1, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "test"}}).Error; err != nil {
		t.Fatal(err)
	}
	got, err = ClaimPlusAutomatic(db, order.ID)
	if err != nil || got == nil || got.AdminID != 0 || got.State != backupstate.Claimed {
		t.Fatalf("plus automatic claim=%v err=%v", got, err)
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	const n = 8
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, err := claim(db, in); results <- err }()
	}
	succeeded := 0
	for i := 0; i < n; i++ {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful claims = %d, want 1", succeeded)
	}
	var count int64
	if err := db.Model(&backupstate.Claim{}).Where("local_order_id = ?", in.orderID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("claims=%d err=%v", count, err)
	}
}
