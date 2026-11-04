package upstreamhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	downstreamcontract "github.com/dujiao-next/internal/modules/downstreamcallback/contract"
	downstreamdomain "github.com/dujiao-next/internal/modules/downstreamcallback/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	procurementcontract "github.com/dujiao-next/internal/modules/procurement/contract"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/money"
	upstreamadapter "github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMapCallbackStatus(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "delivered keep delivered", input: "delivered", expect: "delivered"},
		{name: "completed map delivered", input: "completed", expect: "delivered"},
		{name: "fulfilled map delivered", input: "fulfilled", expect: "delivered"},
		{name: "canceled keep canceled", input: "canceled", expect: "canceled"},
		{name: "cancelled map canceled", input: "cancelled", expect: "canceled"},
		{name: "refunded keep refunded", input: "refunded", expect: "refunded"},
		{name: "partially refunded keep value", input: "partially_refunded", expect: "partially_refunded"},
		{name: "trim and lower", input: "  ReFuNdEd  ", expect: "refunded"},
		{name: "fallback normalized raw", input: "PROCESSING", expect: "processing"},
	}

	for _, tc := range tests {
		got := mapCallbackStatus(tc.input)
		if got != tc.expect {
			t.Fatalf("%s: want %q got %q", tc.name, tc.expect, got)
		}
	}
}

type stubConnections struct {
	conn *siteconnectiondomain.Connection
}

func (s stubConnections) GetByApiKey(string) (*siteconnectiondomain.Connection, error) {
	return s.conn, nil
}

type stubSecrets struct{}

func (stubSecrets) DecryptSecret(encrypted string) (string, error) { return encrypted, nil }

type stubProcurements struct {
	order   *procurementdomain.Order
	handled bool
}

func (s *stubProcurements) GetByLocalOrderNo(string) (*procurementdomain.Order, error) {
	return s.order, nil
}

func (s *stubProcurements) HandleUpstreamCallback(uint, string, *procurementcontract.Fulfillment) error {
	s.handled = true
	return nil
}

// TestHandleCallbackOwnership 保证回调只能由采购单所属连接、且上游订单号一致时才被受理。
func TestHandleCallbackOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name         string
		connectionID uint
		upstreamID   uint
		payloadID    uint
		wantHandled  bool
	}{
		{name: "同连接同上游单号放行", connectionID: 1, upstreamID: 88, payloadID: 88, wantHandled: true},
		{name: "上游单号未落库时只校验连接", connectionID: 1, upstreamID: 0, payloadID: 88, wantHandled: true},
		{name: "其它连接冒用本地订单号", connectionID: 2, upstreamID: 88, payloadID: 88, wantHandled: false},
		{name: "上游订单号不匹配", connectionID: 1, upstreamID: 88, payloadID: 99, wantHandled: false},
	}

	for _, tc := range cases {
		procurements := &stubProcurements{order: &procurementdomain.Order{
			ID:              7,
			ConnectionID:    tc.connectionID,
			LocalOrderNo:    "LOCAL-1",
			UpstreamOrderID: tc.upstreamID,
		}}
		handler := &Handler{Dependencies: Dependencies{
			Connections: stubConnections{conn: &siteconnectiondomain.Connection{
				ID: 1, ApiKey: "key-1", ApiSecret: "secret-1", Status: "active",
			}},
			ConnectionSecrets: stubSecrets{},
			Procurements:      procurements,
		}}

		timestamp := time.Now().Unix()
		body, _ := json.Marshal(callbackPayload{
			Event:             "order.fulfilled",
			OrderID:           tc.payloadID,
			DownstreamOrderNo: "LOCAL-1",
			Status:            "delivered",
			Timestamp:         timestamp,
		})

		request := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/callback", bytes.NewReader(body))
		request.Header.Set(upstreamadapter.HeaderApiKey, "key-1")
		request.Header.Set(upstreamadapter.HeaderTimestamp, fmt.Sprintf("%d", timestamp))
		request.Header.Set(upstreamadapter.HeaderSignature,
			upstreamadapter.Sign("secret-1", http.MethodPost, "/api/v1/upstream/callback", timestamp, body))

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = request
		handler.HandleCallback(c)

		if procurements.handled != tc.wantHandled {
			t.Fatalf("%s: handled=%v want %v (body %s)", tc.name, procurements.handled, tc.wantHandled, recorder.Body.String())
		}
	}
}

type failingSecrets struct{}

func (failingSecrets) DecryptSecret(string) (string, error) { return "", fmt.Errorf("cannot decrypt") }

func TestHandleCallbackRejectsOversizedBodyAndUndecryptableSecret(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		proc := &stubProcurements{order: &procurementdomain.Order{ConnectionID: 1}}
		handler := &Handler{Dependencies: Dependencies{
			Connections:       stubConnections{conn: &siteconnectiondomain.Connection{ID: 1, ApiKey: "key", ApiSecret: "ciphertext", Status: "active"}},
			ConnectionSecrets: failingSecrets{}, Procurements: proc,
		}}
		body := []byte(`{"downstream_order_no":"LOCAL","status":"delivered"}`)
		if oversized {
			body = []byte(strings.Repeat("x", (1<<20)+1))
		}
		ts := time.Now().Unix()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/callback", bytes.NewReader(body))
		req.Header.Set(upstreamadapter.HeaderApiKey, "key")
		req.Header.Set(upstreamadapter.HeaderTimestamp, fmt.Sprint(ts))
		req.Header.Set(upstreamadapter.HeaderSignature, upstreamadapter.Sign("ciphertext", http.MethodPost, "/api/v1/upstream/callback", ts, body))
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = req
		handler.HandleCallback(c)
		want := "internal error"
		if oversized {
			want = "failed to read request body"
		}
		if proc.handled || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("oversized=%v body=%s handled=%v", oversized, rec.Body.String(), proc.handled)
		}
	}
}

type reservationOrders struct {
	Orders
	created, canceled int
}

func (s *reservationOrders) CreateOrder(CreateOrderInput) (*orderdomain.Order, error) {
	s.created++
	return &orderdomain.Order{ID: 50, OrderNo: "LOCAL-50", Status: constants.OrderStatusPendingPayment}, nil
}
func (s *reservationOrders) CancelOrder(id, user uint) (*orderdomain.Order, error) {
	s.canceled++
	return &orderdomain.Order{ID: id, Status: constants.OrderStatusCanceled}, nil
}
func (s *reservationOrders) GetOrderByUser(id, user uint) (*orderdomain.Order, error) {
	return &orderdomain.Order{ID: id, OrderNo: "LOCAL-EXISTING", Status: constants.OrderStatusPaid, TotalAmount: money.FromDecimal(decimal.NewFromInt(16))}, nil
}

type reservationPayments struct {
	Payments
	calls int
}

func (s *reservationPayments) CreatePayment(CreatePaymentInput) (*CreatePaymentResult, error) {
	s.calls++
	return &CreatePaymentResult{OrderPaid: true}, nil
}

type reservationProducts struct{ ProductRepository }

func (reservationProducts) GetByID(string) (*productdomain.Product, error) {
	return &productdomain.Product{ID: 1, IsActive: true, FulfillmentType: constants.FulfillmentTypeUpstream}, nil
}

type reservationSKUs struct{ SKURepository }

func (reservationSKUs) GetByID(uint) (*productdomain.ProductSKU, error) {
	return &productdomain.ProductSKU{ID: 1, ProductID: 1, IsActive: true}, nil
}

type reservationSettings struct{ Settings }

func (reservationSettings) GetSiteCurrency(string) (string, error) { return "CNY", nil }

type reservationRefs struct {
	DownstreamOrderReferences
	createErr, lookupErr error
	existing             *downstreamdomain.OrderRef
}

func (s reservationRefs) Create(*downstreamdomain.OrderRef) error { return s.createErr }
func (s reservationRefs) GetByCredentialAndDownstreamNo(uint, string) (*downstreamdomain.OrderRef, error) {
	return s.existing, s.lookupErr
}

func TestUpstreamOrderNeverDebitsWithoutDurableReference(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		refs                         reservationRefs
		wantStatus, creates, cancels int
	}{
		{"reference write failure", reservationRefs{createErr: errors.New("database unavailable")}, 503, 1, 1},
		{"concurrent duplicate", reservationRefs{createErr: downstreamcontract.ErrDuplicateRef}, 409, 1, 1},
		{"reference lookup failure", reservationRefs{lookupErr: errors.New("database unavailable")}, 503, 0, 0},
		{"business retry returns original", reservationRefs{existing: &downstreamdomain.OrderRef{OrderID: 7}}, 200, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orders := &reservationOrders{}
			payments := &reservationPayments{}
			handler := &Handler{Dependencies: Dependencies{Orders: orders, Payments: payments, SKUs: reservationSKUs{}, ProductRepository: reservationProducts{}, DownstreamRefs: tc.refs, Settings: reservationSettings{}}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(upstreamUserIDKey, uint(3))
			c.Set(upstreamCredentialIDKey, uint(5))
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/upstream/orders", strings.NewReader(`{"sku_id":1,"quantity":1,"downstream_order_no":"remote-1"}`))
			handler.CreateOrder(c)
			if w.Code != tc.wantStatus || payments.calls != 0 || orders.created != tc.creates || orders.canceled != tc.cancels {
				t.Fatalf("status=%d payments=%d creates=%d cancels=%d body=%s", w.Code, payments.calls, orders.created, orders.canceled, w.Body)
			}
		})
	}
}
