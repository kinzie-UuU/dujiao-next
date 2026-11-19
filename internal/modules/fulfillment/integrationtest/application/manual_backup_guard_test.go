package application_test

import (
	"testing"

	"github.com/dujiao-next/internal/constants"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/procurement/backupstate"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
)

func TestManualDeliveryBlockedByBackupClaim(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	if err := db.AutoMigrate(&backupstate.Claim{}, &procurementdomain.Order{}); err != nil {
		t.Fatal(err)
	}
	order := &orderdomain.Order{OrderNo: "BACKUP-GUARD", UserID: 1, Status: constants.OrderStatusPaid, Currency: "CNY"}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Claim{LocalOrderID: order.ID, ProductID: backupstate.LocalPlusProductID, SKUID: backupstate.LocalPlusSKUID, State: backupstate.Claimed, Product: backupstate.Plus, AdminID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	svc := fulfillmentapp.New(fulfillmentapp.Options{OrderStore: orderstore.New(db, "test-guest-credential-secret-with-32-bytes"), FulfillmentStore: fulfillmentstore.New(db)})
	if _, err := svc.CreateManual(fulfillmentapp.CreateManualInput{OrderID: order.ID, AdminID: 1, Payload: "synthetic"}); err == nil {
		t.Fatal("manual delivery bypassed backup claim")
	}
	var count int64
	if err := db.Model(&fulfillmentdomain.Fulfillment{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unexpected delivery: %v", err)
	}
	var loaded orderdomain.Order
	if err := db.First(&loaded, order.ID).Error; err != nil || loaded.Status != constants.OrderStatusPaid {
		t.Fatalf("order status changed: %v", err)
	}
}
