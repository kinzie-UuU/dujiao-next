package procurement_test

import (
	"errors"
	"testing"
	"time"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"

	"github.com/dujiao-next/internal/constants"
)

// ── CreateForOrder tests ──

func TestCreateForOrder_SkipsNonUpstreamItems(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SKIP-001", constants.OrderStatusPaid, constants.FulfillmentTypeAuto)

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("CreateForOrder: %v", err)
	}

	// 验证没有创建采购单
	var count int64
	db.Model(&ProcurementOrder{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no procurement orders for auto fulfillment, got %d", count)
	}
}

func TestCreateForOrder_IdempotentSkipsDuplicate(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-DUP-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	// 第一次创建成功
	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("first CreateForOrder: %v", err)
	}

	// 第二次应该返回 ErrExists
	err := svc.CreateForOrder(order.ID)
	if err != ErrExists {
		t.Errorf("expected ErrExists on duplicate, got: %v", err)
	}
}

type failingSubmissionQueue struct {
	err error
	ids []uint
}

func (q *failingSubmissionQueue) EnqueueSubmit(id uint) error {
	q.ids = append(q.ids, id)
	return q.err
}
func (q *failingSubmissionQueue) EnqueuePoll(uint, time.Duration) error { return nil }

func TestQueueOutageAllowsRetryOfSameProcurement(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "PROC-QUEUE-OUTAGE", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	if err := db.Create(&mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 21, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	q := &failingSubmissionQueue{err: errors.New("queue offline")}
	svc := newTestProcurementServiceWithQueue(db, newTestSiteConnectionService(db, "test-key", t.TempDir()), q)
	if err := svc.CreateForOrder(order.ID); !errors.Is(err, q.err) {
		t.Fatalf("expected queue error, got %v", err)
	}
	var row ProcurementOrder
	if err := db.Where("local_order_id = ?", order.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "failed" || row.ErrorMessage == "" {
		t.Fatalf("initial enqueue failure not actionable: %q, %q", row.Status, row.ErrorMessage)
	}
	if err := svc.RetryManual(row.ID); !errors.Is(err, q.err) {
		t.Fatalf("retry during outage: %v", err)
	}
	q.err = nil
	if err := svc.RetryManual(row.ID); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if len(q.ids) != 3 || q.ids[0] != row.ID || q.ids[1] != row.ID || q.ids[2] != row.ID {
		t.Fatalf("unexpected procurement IDs: %v", q.ids)
	}
	if err := db.First(&row, row.ID).Error; err != nil || row.Status != "pending" {
		t.Fatalf("expected pending, got %q, %v", row.Status, err)
	}
}

func TestSoftDeletedLegacyProcurementCannotBeRecreated(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "DELETED-LEGACY", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	old := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "pending")
	if err := db.Model(old).Updates(map[string]interface{}{"submission_version": 0, "deleted_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	svc := newTestProcurementService(db, newTestSiteConnectionService(db, "key", t.TempDir()))
	if err := svc.CreateForOrder(order.ID); !errors.Is(err, ErrExists) {
		t.Fatalf("recreated hidden legacy: %v", err)
	}
	duplicate := &ProcurementOrder{LocalOrderID: order.ID, ConnectionID: 1, Currency: "CNY", SubmissionVersion: 1}
	if err := db.Create(duplicate).Error; err == nil {
		t.Fatal("database allowed soft-delete uniqueness bypass")
	}
	var count int64
	db.Table("procurement_orders").Where("local_order_id = ?", order.ID).Count(&count)
	if count != 1 {
		t.Fatalf("changed history: %d", count)
	}
}
