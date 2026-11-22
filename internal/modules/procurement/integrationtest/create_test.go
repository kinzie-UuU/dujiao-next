package procurement_test

import (
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	procurementstore "github.com/dujiao-next/internal/modules/procurement/infrastructure/gormstore"
	"gorm.io/gorm"
	"reflect"
	"sync"
	"testing"
	"time"
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

// Only records task IDs. No worker, HTTP request or supplier balance is involved.
type dispatchRetryQueue struct {
	mu  sync.Mutex
	err error
	ids []uint
}

func (q *dispatchRetryQueue) EnqueueSubmit(id uint) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, id)
	return q.err
}
func (q *dispatchRetryQueue) EnqueuePoll(uint, time.Duration) error { return nil }
func (q *dispatchRetryQueue) recorded() []uint {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]uint(nil), q.ids...)
}
func (q *dispatchRetryQueue) setError(err error) { q.mu.Lock(); defer q.mu.Unlock(); q.err = err }
func makeDispatchDue(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	if err := db.Model(&ProcurementOrder{}).Where("id = ?", id).Updates(map[string]interface{}{"next_retry_at": time.Now().Add(-time.Minute), "updated_at": time.Now().Add(-2 * time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRetryRecoversQueueFailureWithSameOrder(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "DISPATCH-OUTAGE", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	if err := db.Create(&mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 34, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	offline := errors.New("simulated queue unavailable")
	q := &dispatchRetryQueue{err: offline}
	svc := newTestProcurementServiceWithQueue(db, newTestSiteConnectionService(db, "test-key", t.TempDir()), q)
	if err := svc.CreateForOrder(order.ID); !errors.Is(err, offline) {
		t.Fatalf("expected queue outage: %v", err)
	}
	var p ProcurementOrder
	if err := db.Where("local_order_id = ?", order.ID).First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if p.Status != "failed" || p.NextRetryAt == nil {
		t.Fatalf("no durable retry after queue failure: %+v", p)
	}
	if err := svc.RetryPendingSubmissions(); err != nil {
		t.Errorf("retry sweep: %v", err)
	}
	if len(q.recorded()) != 1 {
		t.Fatal("retry ignored initial backoff")
	}
	makeDispatchDue(t, db, p.ID)
	if err := svc.RetryPendingSubmissions(); !errors.Is(err, offline) {
		t.Fatalf("retry did not report queue failure: %v", err)
	} // queue still unavailable
	if len(q.recorded()) != 2 {
		t.Fatal("due failure did not retry")
	}
	if err := svc.RetryPendingSubmissions(); err != nil {
		t.Errorf("retry sweep: %v", err)
	}
	if len(q.recorded()) != 2 {
		t.Fatal("queue outage caused immediate repeated dispatch")
	}
	q.setError(nil)
	makeDispatchDue(t, db, p.ID)
	if err := svc.RetryPendingSubmissions(); err != nil {
		t.Errorf("retry sweep: %v", err)
	}
	ids := q.recorded()
	if !reflect.DeepEqual(ids, []uint{p.ID, p.ID, p.ID}) {
		t.Fatalf("retry changed procurement identity: %v", ids)
	}
	var count int64
	if err := db.Model(&ProcurementOrder{}).Where("local_order_id = ?", order.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate procurement: %d %v", count, err)
	}
	if err := db.First(&p, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if p.SubmittedAt != nil || p.SubmissionRequestSHA256 != "" || p.UpstreamOrderNo != "" {
		t.Fatal("queue dispatch fabricated supplier submission")
	}
}

func TestDispatchRetryRecoversLostPendingTaskAndClaimsOnce(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	order := createProcTestOrder(t, db, "DISPATCH-LOST", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	p := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "pending")
	if err := db.Model(p).Update("updated_at", time.Now().Add(-2*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	q := &dispatchRetryQueue{}
	svc := newTestProcurementServiceWithQueue(db, newTestSiteConnectionService(db, "test-key", t.TempDir()), q)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.RetryPendingSubmissions(); err != nil {
				t.Errorf("retry sweep: %v", err)
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(q.recorded(), []uint{p.ID}) {
		t.Fatalf("parallel sweep duplicated dispatch: %v", q.recorded())
	}
	// Enqueue success is not evidence that Redis kept the task: allow recovery
	// after the lease, while keeping the original procurement identity.
	makeDispatchDue(t, db, p.ID)
	if err := svc.RetryPendingSubmissions(); err != nil {
		t.Errorf("retry sweep: %v", err)
	}
	if !reflect.DeepEqual(q.recorded(), []uint{p.ID, p.ID}) {
		t.Fatalf("lost task not recoverable: %v", q.recorded())
	}
}

func TestDispatchRetryNeverReopensPossiblySubmittedRows(t *testing.T) {
	cases := []struct {
		name    string
		updates map[string]interface{}
	}{
		{"legacy", map[string]interface{}{"submission_version": 0}},
		{"submitted-at", map[string]interface{}{"submitted_at": time.Now()}},
		{"request-digest", map[string]interface{}{"submission_request_sha256": "persisted-request"}},
		{"connection-digest", map[string]interface{}{"submission_connection_sha256": "persisted-connection"}},
		{"numeric-receipt", map[string]interface{}{"upstream_order_id": 123}},
		{"number-receipt", map[string]interface{}{"upstream_order_no": "NZ-PAID"}},
		{"deleted", map[string]interface{}{"deleted_at": time.Now()}},
	}
	for _, status := range []string{"submitted", "uncertain", "accepted", "rejected", "fulfilled", "completed", "refunded", "partially_refunded", "canceled"} {
		cases = append(cases, struct {
			name    string
			updates map[string]interface{}
		}{status, map[string]interface{}{"status": status}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "PROTECTED-"+tc.name, constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			p := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "failed")
			makeDispatchDue(t, db, p.ID)
			if err := db.Model(p).Updates(tc.updates).Error; err != nil {
				t.Fatal(err)
			}
			var before ProcurementOrder
			if err := db.First(&before, p.ID).Error; err != nil {
				t.Fatal(err)
			}
			q := &dispatchRetryQueue{}
			svc := newTestProcurementServiceWithQueue(db, newTestSiteConnectionService(db, "test-key", t.TempDir()), q)
			if err := svc.RetryPendingSubmissions(); err != nil {
				t.Errorf("retry sweep: %v", err)
			}
			if len(q.recorded()) != 0 {
				t.Fatal("protected procurement was dispatched")
			}
			var after ProcurementOrder
			if err := db.First(&after, p.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("retry sweep mutated protected procurement")
			}
		})
	}
}

func TestDispatchRetryExcludesIneligibleLocalAndParentOrders(t *testing.T) {
	for _, parent := range []bool{false, true} {
		for _, state := range []string{"pending_payment", "canceled", "refunded", "partially_refunded", "delivered", "deleted", "refunded_amount"} {
			// A partially refunded parent may legitimately have an untouched paid child;
			// the child-specific refund check is what prevents spending refunded funds.
			if parent && (state == "partially_refunded" || state == "refunded_amount") {
				continue
			}
			t.Run(fmt.Sprintf("parent-%t/%s", parent, state), func(t *testing.T) {
				db := setupProcurementTestDB(t)
				order := createProcTestOrder(t, db, "INELIGIBLE", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
				target := order
				if parent {
					target = createProcTestOrder(t, db, "PARENT", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
					if err := db.Model(order).Update("parent_id", target.ID).Error; err != nil {
						t.Fatal(err)
					}
				}
				updates := map[string]interface{}{"status": state}
				if state == "deleted" {
					updates = map[string]interface{}{"deleted_at": time.Now()}
				}
				if state == "refunded_amount" {
					updates = map[string]interface{}{"refunded_amount": "1.00"}
				}
				if err := db.Model(&orderdomain.Order{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				p := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "failed")
				makeDispatchDue(t, db, p.ID)
				q := &dispatchRetryQueue{}
				svc := newTestProcurementServiceWithQueue(db, newTestSiteConnectionService(db, "test-key", t.TempDir()), q)
				if err := svc.RetryPendingSubmissions(); err != nil {
					t.Errorf("retry sweep: %v", err)
				}
				if len(q.recorded()) != 0 {
					t.Fatal("ineligible order was dispatched")
				}
			})
		}
	}
}

func TestDispatchRetryClaimRechecksStaleCandidate(t *testing.T) {
	for _, change := range []string{"submitted", "receipt", "refund", "parent-canceled"} {
		t.Run(change, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			parent := createProcTestOrder(t, db, "CLAIM-PARENT", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			order := createProcTestOrder(t, db, "CLAIM-CHILD", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			if err := db.Model(order).Update("parent_id", parent.ID).Error; err != nil {
				t.Fatal(err)
			}
			p := createTestProcurementOrder(t, db, 1, order.ID, order.OrderNo, "pending")
			makeDispatchDue(t, db, p.ID)
			store := procurementstore.New(db)
			at := time.Now()
			candidates, err := store.ListDispatchable(at, 200)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("candidate missing: %d %v", len(candidates), err)
			}
			switch change {
			case "submitted":
				err = db.Model(p).Updates(map[string]interface{}{"status": "submitted", "submitted_at": at, "submission_request_sha256": "claimed"}).Error
			case "receipt":
				err = db.Model(p).Update("upstream_order_no", "NZ-EXISTING").Error
			case "refund":
				err = db.Model(order).Update("refunded_amount", "1.00").Error
			case "parent-canceled":
				err = db.Model(parent).Update("status", constants.OrderStatusCanceled).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ClaimDispatch(p.ID, at)
			if err != nil || claimed {
				t.Fatalf("stale candidate was claimed=%t err=%v", claimed, err)
			}
		})
	}
}
