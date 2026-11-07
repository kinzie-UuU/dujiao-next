package procurement_test

import (
	"encoding/json"
	"errors"
	"fmt"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	procurementstore "github.com/dujiao-next/internal/modules/procurement/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"

	"github.com/dujiao-next/internal/constants"
	siteconnectionapp "github.com/dujiao-next/internal/modules/siteconnection/application"
)

// ── SubmitToUpstream tests ──

func TestSubmitToUpstream_Success(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SUBMIT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	// 创建 product mapping 和 sku mapping
	pm := &mappingdomain.Mapping{
		ConnectionID:      1,
		LocalProductID:    1,
		UpstreamProductID: 101,
		IsActive:          true,
	}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{
		ProductMappingID: pm.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	}
	db.Create(sm)

	// mock upstream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"order_id": 999,
			"order_no": "UP-999",
			"status":   "accepted",
			"amount":   "50.00",
			"currency": "CNY",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name:      "test-upstream",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")

	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	// 验证采购单状态 = accepted
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "accepted" {
		t.Errorf("expected procurement status 'accepted', got %q", updatedProc.Status)
	}
	if updatedProc.UpstreamOrderID != 999 {
		t.Errorf("expected upstream_order_id=999, got %d", updatedProc.UpstreamOrderID)
	}

	// 验证本地订单状态 = fulfilling
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_ErrorWithoutReceiptRequiresReconciliation(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-NONRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "product_out_of_stock",
			"error_message": "product out of stock",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// A negative response without a conclusive receipt does not prove no purchase.
	_ = svc.SubmitToUpstream(proc.ID)

	// Persist uncertainty instead of converting an unknown remote effect to rejection.
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "uncertain" {
		t.Errorf("expected procurement status 'uncertain', got %q", updatedProc.Status)
	}

	// Keep the local fulfillment state until supplier reconciliation.
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q while receipt is uncertain, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_TransientErrorDoesNotResubmit(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-RETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "temporary failure",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 3,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// Persist uncertainty without enqueueing another purchase.
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("expected durable uncertainty for ambiguous failure, got: %v", err)
	}

	// Keep ambiguous acceptance visible and do not increase a purchase retry budget.
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "uncertain" {
		t.Errorf("expected procurement status 'uncertain', got %q", updatedProc.Status)
	}
	if updatedProc.RetryCount != 0 {
		t.Errorf("expected retry_count=0, got %d", updatedProc.RetryCount)
	}
}

func TestSubmitToUpstream_RetryLimitDoesNotProveNoAcceptance(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-MAXRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	productMapping := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(productMapping)
	db.Create(&mappingdomain.SKUMapping{
		ProductMappingID: productMapping.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "timeout after retries",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 2, RetryIntervals: "[30,60]",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "failed")
	// 设置 retry_count 已达上限
	db.Model(proc).Update("retry_count", 2)

	svc := newTestProcurementService(db, connSvc)

	// 通过公开提交入口验证：可重试错误在次数耗尽后仍必须转为 rejected。
	_ = svc.SubmitToUpstream(proc.ID)

	// Persist uncertainty instead of converting an unknown remote effect to rejection.
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "uncertain" {
		t.Errorf("expected procurement status 'uncertain', got %q", updatedProc.Status)
	}

	// 验证本地订单回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

type newZoeTransport func(*http.Request) (*http.Response, error)

func (f newZoeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewZoePaidProcurementLifecycle(t *testing.T) {
	for _, sku := range []uint{3, 21, 22, 24, 34} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("product-%d/legacy-%t", sku, legacy), func(t *testing.T) {
				db := setupProcurementTestDB(t)
				order := createProcTestOrder(t, db, "LOCAL-TEST", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
				connections := newTestSiteConnectionService(db, "test-key", t.TempDir())
				conn, err := connections.Create(siteconnectionapp.CreateInput{
					Name: "NewZoe", BaseURL: "https://shop.newzoe.cloud", ApiKey: "test-key",
					ApiSecret: "test-secret", Protocol: constants.ConnectionProtocolNewzoe,
				})
				if err != nil {
					t.Fatal(err)
				}
				pm := &mappingdomain.Mapping{ConnectionID: conn.ID, LocalProductID: 1, UpstreamProductID: sku, IsActive: true}
				if err := db.Create(pm).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: sku, UpstreamIsActive: true}).Error; err != nil {
					t.Fatal(err)
				}

				var submits int
				status := "pending"
				deliveryItem := "SAMPLE-CREDENTIAL"
				if sku == 3 {
					deliveryItem = "https://example.invalid/codex/one-use-test"
				}
				if sku == 34 {
					deliveryItem = "figma-test@example.invalid----synthetic-password----synthetic-mail-token"
				}
				old := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = old })
				http.DefaultTransport = newZoeTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "shop.newzoe.cloud" {
						t.Fatalf("unexpected host %s", r.URL.Host)
					}
					body := ""
					switch r.Method + " " + r.URL.Path {
					case "POST /api/v1/orders":
						var item struct {
							CouponCode    *string `json:"coupon_code"`
							ProductID     uint    `json:"product_id"`
							Quantity      int     `json:"quantity"`
							PaymentMethod string  `json:"payment_method"`
						}
						if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
							t.Fatal(err)
						}
						if item.ProductID != sku || item.Quantity != 1 || item.PaymentMethod != "balance" || item.CouponCode != nil {
							t.Fatalf("unexpected purchase: %#v", item)
						}
						key := r.Header.Get("Idempotency-Key")
						submits++
						if key != order.OrderNo {
							t.Fatal("purchase did not use stable local order key")
						}

						body = `{"order":{"id":"NZ-A0001","status":"processing","payment_received":true,"amount":"5.00","currency":"CNY","quantity":1}}`
					case "GET /api/v1/orders/NZ-A0001":
						body = fmt.Sprintf(`{"order":{"id":"NZ-A0001","status":%q,"payment_received":true,"amount":"5.00","currency":"CNY","quantity":1}}`, status)
					case "GET /api/v1/orders/NZ-A0001/delivery":
						body = fmt.Sprintf(`{"delivery":{"available":true,"status":"completed","type":"automatic","items":[%q],"content":%q}}`, deliveryItem, deliveryItem)
					default:
						t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"data":` + body + `}`)), Request: r}, nil
				})
				svc := newTestProcurementService(db, connections)
				var recoveryQueue *failingSubmissionQueue
				if sku == 34 && !legacy {
					recoveryQueue = &failingSubmissionQueue{err: errors.New("simulated queue response unavailable")}
					svc = newTestProcurementServiceWithQueue(db, connections, recoveryQueue)
				}
				createErr := svc.CreateForOrder(order.ID)
				if recoveryQueue != nil {
					if !errors.Is(createErr, recoveryQueue.err) {
						t.Fatalf("expected initial queue failure: %v", createErr)
					}
				} else if createErr != nil {
					t.Fatal(createErr)
				}
				proc, err := svc.GetByLocalOrderNo(order.OrderNo)
				if err != nil || proc == nil {
					t.Fatalf("no procurement: %v", err)
				}
				if recoveryQueue != nil {
					if submits != 0 || proc.Status != "failed" {
						t.Fatal("queue failure made a supplier purchase")
					}
					makeDispatchDue(t, db, proc.ID)
					recoveryQueue.err = nil
					if err := svc.RetryPendingSubmissions(); err != nil {
						t.Fatal(err)
					}
					if len(recoveryQueue.ids) != 2 || recoveryQueue.ids[0] != proc.ID || recoveryQueue.ids[1] != proc.ID {
						t.Fatalf("queue recovery changed purchase identity: %v", recoveryQueue.ids)
					}
					// The first ambiguous enqueue may also have kept its task. Both
					// workers below use the same durable submission fence.
				}
				if err := svc.SubmitToUpstream(proc.ID); err != nil {
					t.Fatal(err)
				}
				if err := svc.SubmitToUpstream(proc.ID); err == nil {
					t.Fatal("accepted request was replayed")
				}
				if submits != 1 {
					t.Fatalf("got %d submissions", submits)
				}
				// Existing receipt-bearing version-0 rows keep their polling lifecycle.
				if legacy {
					if err := db.Model(&ProcurementOrder{}).Where("id = ?", proc.ID).Updates(map[string]interface{}{"submission_version": 0, "submitted_at": nil, "submission_request_sha256": "", "submission_connection_sha256": ""}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := svc.CancelManual(proc.ID); err == nil {
					t.Fatal("accepted order was canceled locally")
				}
				if err := svc.PollUpstreamStatus(proc.ID); err != nil {
					t.Fatal(err)
				}
				current, err := svc.GetByLocalOrderNo(order.OrderNo)
				if err != nil || current.Status != "accepted" || current.UpstreamOrderNo != "NZ-A0001" {
					t.Fatalf("lost accepted order: %#v %v", current, err)
				}
				status = "completed"
				svc.SyncAcceptedOrders()
				if err := svc.PollUpstreamStatus(proc.ID); err != nil {
					t.Fatal(err)
				}
				var results []fulfillmentdomain.Fulfillment
				if err := db.Where("order_id = ?", order.ID).Find(&results).Error; err != nil {
					t.Fatal(err)
				}
				if len(results) != 1 || results[0].Payload != deliveryItem {
					t.Fatalf("unexpected fulfillment: %#v", results)
				}
				var delivered orderdomain.Order
				if err := db.First(&delivered, order.ID).Error; err != nil || delivered.Status != constants.OrderStatusDelivered {
					t.Fatalf("not delivered: %s %v", delivered.Status, err)
				}

				late := createProcTestOrder(t, db, "LATE-TEST", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
				lateProc := createTestProcurementOrder(t, db, conn.ID, late.ID, late.OrderNo, "failed")
				if err := db.Model(lateProc).Update("created_at", time.Now().Add(-24*time.Hour)).Error; err != nil {
					t.Fatal(err)
				}
				if err := svc.RetryManual(lateProc.ID); err != nil {
					t.Fatal(err)
				}
				if err := svc.SubmitToUpstream(lateProc.ID); err != nil {
					t.Fatal(err)
				}
				lateState, _ := svc.GetByLocalOrderNo(late.OrderNo)
				if lateState.Status != "uncertain" {
					t.Fatal("age guard did not preserve uncertainty")
				}
				if submits != 1 {
					t.Fatal("expired procurement made another purchase")
				}
			})
		}
	}

}

func TestSubmissionIntentSurvivesLostResponseAndReceiptWriteFailure(t *testing.T) {
	for _, failure := range []string{"lost-response", "receipt-write"} {
		t.Run(failure, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "INTENT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
			db.Create(pm)
			db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true})
			var calls atomic.Int32
			var proc *ProcurementOrder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var durable ProcurementOrder
				if err := db.First(&durable, proc.ID).Error; err != nil || durable.Status != "submitted" || durable.SubmittedAt == nil || len(durable.SubmissionRequestSHA256) != 64 {
					t.Error("supplier contacted before durable intent")
				}
				if failure == "lost-response" {
					socket, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					socket.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"ok":true,"order_id":808,"order_no":"UP-808","amount":"100.00","currency":"CNY"}`)
			}))
			defer server.Close()
			connections := newTestSiteConnectionService(db, "test-key", t.TempDir())
			conn, err := connections.Create(siteconnectionapp.CreateInput{Name: "synthetic", BaseURL: server.URL, ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext})
			if err != nil {
				t.Fatal(err)
			}
			proc = createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
			if failure == "receipt-write" {
				db.Callback().Update().Before("gorm:update").Register("test:receipt_failure", func(tx *gorm.DB) {
					if values, ok := tx.Statement.Dest.(map[string]interface{}); ok && values["status"] == constants.ProcurementStatusAccepted {
						tx.AddError(errors.New("synthetic receipt disk failure"))
					}
				})
			}
			svc := newTestProcurementService(db, connections)
			err = svc.SubmitToUpstream(proc.ID)
			if failure == "receipt-write" && err == nil {
				t.Fatal("receipt write failure hidden")
			}
			restarted := newTestProcurementService(db, connections)
			if err := restarted.SubmitToUpstream(proc.ID); err == nil {
				t.Fatal("restart replayed uncertain purchase")
			}
			if err := restarted.RetryManual(proc.ID); err == nil {
				t.Fatal("manual retry replayed uncertain purchase")
			}
			if err := restarted.CancelManual(proc.ID); err == nil {
				t.Fatal("manual cancel hid uncertain purchase")
			}
			if calls.Load() != 1 {
				t.Fatal("duplicate supplier request")
			}
			var stored ProcurementOrder
			db.First(&stored, proc.ID)
			if stored.Status != "submitted" && stored.Status != "uncertain" {
				t.Fatal("lost persistent attempt")
			}
			persisted := filepath.Join(t.TempDir(), "restart.db")
			if err := db.Exec("VACUUM INTO ?", persisted).Error; err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestPersistedUnknownRestartChild$", "-test.v")
			command.Env = append(os.Environ(), "PROCUREMENT_RESTART_DB="+persisted)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("restarted process: %v %s", err, output)
			}

		})
	}
}

func TestConcurrentSubmissionHasOneDurableClaim(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "INTENT-CONCURRENT", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		fmt.Fprint(w, `{"ok":true,"order_id":888,"order_no":"UP-888","amount":"100.00","currency":"CNY"}`)
	}))
	defer server.Close()
	connections := newTestSiteConnectionService(db, "key", t.TempDir())
	conn, err := connections.Create(siteconnectionapp.CreateInput{Name: "synthetic", BaseURL: server.URL, ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext})
	if err != nil {
		t.Fatal(err)
	}
	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connections)
	done := make(chan error, 1)
	go func() { done <- svc.SubmitToUpstream(proc.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("submission did not reach fixture")
	}
	second := svc.SubmitToUpstream(proc.ID)
	close(release)
	if second == nil {
		t.Fatal("concurrent claim accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent duplicate purchase")
	}
}

func TestLegacyAttemptIsFencedBeforePreparationSideEffects(t *testing.T) {
	for _, status := range []string{constants.OrderStatusPaid, constants.OrderStatusFulfilling, constants.OrderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "LEGACY-INTENT", status, constants.FulfillmentTypeUpstream)
			proc := createTestProcurementOrder(t, db, 9999, order.ID, order.OrderNo, "failed")
			if err := db.Model(proc).Update("submission_version", 0).Error; err != nil {
				t.Fatal(err)
			}
			svc := newTestProcurementService(db, newTestSiteConnectionService(db, "key", t.TempDir()))
			if err := svc.SubmitToUpstream(proc.ID); err != nil {
				t.Fatal(err)
			}
			var stored ProcurementOrder
			db.First(&stored, proc.ID)
			if stored.Status != "uncertain" {
				t.Fatal("legacy row rejected before uncertainty fence")
			}
			var unchanged orderdomain.Order
			db.First(&unchanged, order.ID)
			if unchanged.Status != status {
				t.Fatal("legacy preparation changed local order")
			}
		})
	}
}

func TestSubmissionFenceRespectsParentLifecycle(t *testing.T) {
	for _, status := range []string{"paid", "fulfilling", "partially_delivered", "partially_refunded", "canceled", "refunded", "completed", "delivered", "pending_payment", "expired"} {
		t.Run(status, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			parent := createProcTestOrder(t, db, "PARENT-FENCE", status, constants.FulfillmentTypeUpstream)
			child := createProcTestOrder(t, db, "CHILD-FENCE", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
			if err := db.Model(child).Update("parent_id", parent.ID).Error; err != nil {
				t.Fatal(err)
			}
			proc := createTestProcurementOrder(t, db, 1, child.ID, child.OrderNo, "pending")
			claimed, err := procurementstore.New(db).BeginSubmission(proc.ID, "pending", strings.Repeat("a", 64), strings.Repeat("b", 64), time.Now())
			allowed := status == "paid" || status == "fulfilling" || status == "partially_delivered" || status == "partially_refunded"
			if allowed && (err != nil || !claimed) {
				t.Fatalf("active parent blocked: %v", err)
			}
			if !allowed && claimed {
				t.Fatal("terminal or unpaid parent admitted purchase")
			}
		})
	}
}

func TestPersistedUnknownRestartChild(t *testing.T) {
	path := os.Getenv("PROCUREMENT_RESTART_DB")
	if path == "" {
		t.Skip("invoked only by isolated restart fixture")
	}
	db, err := gorm.Open(sqlite.Open("file:"+path+"?mode=ro"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestProcurementService(db, newTestSiteConnectionService(db, "test-key", t.TempDir()))
	if err := svc.SubmitToUpstream(1); err == nil {
		t.Fatal("process restart replayed purchase")
	}
	if err := svc.RetryManual(1); err == nil {
		t.Fatal("process restart allowed manual replay")
	}
	if err := svc.CancelManual(1); err == nil {
		t.Fatal("process restart allowed cancellation")
	}
}

func TestConcurrentCreateForOneLocalOrderCannotDuplicatePurchase(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	order := createProcTestOrder(t, db, "CREATE-RACE", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		posts.Add(1)
		fmt.Fprint(w, `{"ok":true,"order_id":888,"order_no":"UP-888","amount":"100.00","currency":"CNY"}`)
	}))
	defer server.Close()
	connections := newTestSiteConnectionService(db, "key", t.TempDir())
	conn, err := connections.Create(siteconnectionapp.CreateInput{Name: "synthetic", BaseURL: server.URL, ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext})
	if err != nil {
		t.Fatal(err)
	}
	pm := &mappingdomain.Mapping{ConnectionID: conn.ID, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	if err := db.Create(pm).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	// Force both application-level existence reads to finish before either INSERT.
	ready, release := make(chan struct{}, 2), make(chan struct{})
	if err := db.Callback().Create().Before("gorm:begin_transaction").Register("test:creation_barrier", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "procurement_orders" {
			ready <- struct{}{}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestProcurementService(db, connections)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- svc.CreateForOrder(order.ID) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("creation barrier timeout")
		}
	}
	close(release)
	first, second := <-done, <-done
	if (first == nil) == (second == nil) {
		t.Fatalf("expected exactly one successful creation: %v / %v", first, second)
	}
	db.Callback().Create().Remove("test:creation_barrier")
	var rows []ProcurementOrder
	if err := db.Where("local_order_id = ?", order.ID).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
	if err := svc.SubmitToUpstream(rows[0].ID); err != nil {
		t.Fatal(err)
	}
	_ = svc.SubmitToUpstream(rows[0].ID)
	if posts.Load() != 1 {
		t.Fatalf("POST count=%d", posts.Load())
	}
}
