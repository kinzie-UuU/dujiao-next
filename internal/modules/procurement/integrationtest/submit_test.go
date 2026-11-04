package procurement_test

import (
	"encoding/json"
	"fmt"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSubmitToUpstream_NonRetryableError_Rejects(t *testing.T) {
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

	// 不可重试错误应返回 error
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单状态回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q after rejection, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_RetryableError_Retries(t *testing.T) {
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

	// 可重试错误不应返回 error（已入队重试）
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("expected no error for retryable failure, got: %v", err)
	}

	// 验证采购单状态 = failed（而非 rejected）
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "failed" {
		t.Errorf("expected procurement status 'failed', got %q", updatedProc.Status)
	}
	if updatedProc.RetryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", updatedProc.RetryCount)
	}
}

func TestHandleSubmitFailure_MaxRetriesExhausted(t *testing.T) {
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

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

type newZoeTransport func(*http.Request) (*http.Response, error)

func (f newZoeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewZoePaidProcurementLifecycle(t *testing.T) {
	for _, sku := range []uint{3, 21, 22, 24} {
		t.Run(fmt.Sprintf("product-%d", sku), func(t *testing.T) {
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
			var firstKey string
			status := "pending"
			deliveryItem := "SAMPLE-CREDENTIAL"
			if sku == 3 {
				deliveryItem = "https://example.invalid/codex/one-use-test"
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
					if submits == 1 {
						firstKey = key
						return nil, io.ErrUnexpectedEOF
					}
					if key == "" || key != firstKey {
						t.Fatal("retry changed idempotency key")
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
			if err := svc.CreateForOrder(order.ID); err != nil {
				t.Fatal(err)
			}
			proc, err := svc.GetByLocalOrderNo(order.OrderNo)
			if err != nil || proc == nil {
				t.Fatalf("no procurement: %v", err)
			}
			for range 2 {
				if err := svc.SubmitToUpstream(proc.ID); err != nil {
					t.Fatal(err)
				}
			}
			if submits != 2 {
				t.Fatalf("got %d submissions", submits)
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
			if err := svc.SubmitToUpstream(lateProc.ID); err == nil {
				t.Fatal("expired procurement retry was accepted")
			}
			if submits != 2 {
				t.Fatal("expired procurement made another purchase")
			}
		})
	}
}
