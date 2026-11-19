package gormstore

import (
	"fmt"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"github.com/dujiao-next/internal/queue"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type enqueueStub struct {
	payloads []queue.BackupProcurementSubmitPayload
	err      error
}

func (s *enqueueStub) EnqueueBackupProcurementSubmit(payload queue.BackupProcurementSubmitPayload) error {
	s.payloads = append(s.payloads, payload)
	return s.err
}

func triggerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:paid_trigger_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPaidTriggerIgnoresUnpinnedOrders(t *testing.T) {
	db := triggerDB(t)
	order := orderdomain.Order{OrderNo: "other", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, SKUID: 2, Quantity: 1, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "x"}}).Error; err != nil {
		t.Fatal(err)
	}
	stub := &enqueueStub{}
	if err := (&PaidTrigger{DB: db, Queue: stub}).OnOrderPaid(order.ID); err != nil {
		t.Fatal(err)
	}
	if len(stub.payloads) != 0 {
		t.Fatalf("unexpected enqueue: %+v", stub.payloads)
	}
}
