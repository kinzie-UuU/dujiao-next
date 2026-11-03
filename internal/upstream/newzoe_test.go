package upstream

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

type newzoeTestTransport func(*http.Request) (*http.Response, error)

func (f newzoeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testNewzoeClient(t *testing.T, transport newzoeTestTransport) *newzoeAdapter {
	t.Helper()
	a, err := newNewzoeAdapter(&siteconnectiondomain.Connection{BaseURL: newzoeOrigin, ApiKey: "test-key", ApiSecret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transport
	return a
}
func newzoeTestResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestNewzoeSignedPurchaseReplay(t *testing.T) {
	var previousNonce string
	calls := 0
	a := testNewzoeClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"product_id":24,"quantity":2,"payment_method":"balance"}` || r.URL.String() != newzoeOrigin+"/api/v1/orders" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected purchase request: %s %s %s", r.Method, r.URL, body)
		}
		nonce := r.Header.Get("X-Api-Nonce")
		stamp := r.Header.Get("X-Api-Timestamp")
		if nonce == previousNonce || stamp == "" || r.Header.Get("Idempotency-Key") != "LOCAL-01" || r.Header.Get("X-Api-Key") != "test-key" {
			t.Fatal("missing or unstable purchase headers")
		}
		previousNonce = nonce
		mac := hmac.New(sha256.New, []byte("test-secret"))
		fmt.Fprint(mac, strings.Join([]string{"POST", "/api/v1/orders", stamp, nonce, string(body)}, "\n"))
		if hex.EncodeToString(mac.Sum(nil)) != r.Header.Get("X-Api-Signature") {
			t.Fatal("signature does not match canonical request")
		}
		if calls == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return newzoeTestResponse(200, `{"ok":true,"data":{"replayed":true,"order":{"id":"REMOTE-09","amount":"12.06","currency":"CNY","status":"completed","quantity":2}}}`), nil
	})
	req := CreateUpstreamOrderReq{SKUID: 24, Quantity: 2, DownstreamOrderNo: "LOCAL-01"}
	if _, err := a.CreateOrder(context.Background(), req); err == nil {
		t.Fatal("lost response should be retryable")
	}
	got, err := a.CreateOrder(context.Background(), req)
	if err != nil || !got.OK || got.OrderNo != "REMOTE-09" {
		t.Fatalf("replay failed: %+v %v", got, err)
	}
	req.SKUID = 23
	if got, err = a.CreateOrder(context.Background(), req); err != nil || got.OK || calls != 2 {
		t.Fatal("unexpected purchase of excluded product")
	}
}

func TestNewzoeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, status, id, delivery, wantStatus, wantPayload string
		wantErr                                             bool
	}{
		{"processing", "processing", "REMOTE-09", "", "fulfilling", "", false},
		{"failed", "failure", "REMOTE-09", "", "failed", "", false},
		{"completed without payment", "completed", "REMOTE-09", "", "", "", true},
		{"wrong order", "completed", "REMOTE-10", "", "", "", true},
		{"not ready", "completed", "REMOTE-09", `{"available":false}`, "fulfilling", "", false},
		{"partial", "completed", "REMOTE-09", `{"available":true,"status":"completed","type":"automatic","items":["a"]}`, "", "", true},
		{"empty credential", "completed", "REMOTE-09", `{"available":true,"status":"completed","type":"automatic","items":["a","  "]}`, "", "", true},
		{"done", "completed", "REMOTE-09", `{"available":true,"status":"completed","type":"automatic","items":["user1\npassword1","user2\npassword2"]}`, "delivered", "user1\npassword1\nuser2\npassword2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testNewzoeClient(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/v1/orders/REMOTE-09":
					return newzoeTestResponse(200, fmt.Sprintf(`{"ok":true,"data":{"order":{"id":%q,"quantity":2,"status":%q,"payment_received":%t}}}`, tc.id, tc.status, tc.name != "completed without payment")), nil
				case "/api/v1/orders/REMOTE-09/delivery":
					if tc.delivery != "" {
						return newzoeTestResponse(200, `{"ok":true,"data":{"delivery":`+tc.delivery+`}}`), nil
					}
				}
				t.Fatalf("unexpected GET %s", r.URL.Path)
				return nil, nil
			})
			got, err := a.GetOrderByNumber(context.Background(), "REMOTE-09")
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error %v", err)
			}
			if err != nil {
				return
			}
			payload := ""
			if got.Fulfillment != nil {
				payload = got.Fulfillment.Payload
			}
			if got.Status != tc.wantStatus || payload != tc.wantPayload {
				t.Fatalf("wrong delivery status/payload: %+v %q", got, payload)
			}
		})
	}
}

func TestNewzoePinnedOrigin(t *testing.T) {
	_, err := NewAdapter(&siteconnectiondomain.Connection{Protocol: constants.ConnectionProtocolNewzoe, BaseURL: "https://example.invalid", ApiKey: "key", ApiSecret: "secret"}, "")
	if err == nil {
		t.Fatal("unexpected origin accepted")
	}
	calls := 0
	a := testNewzoeClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		resp := newzoeTestResponse(307, "")
		resp.Header.Set("Location", "https://redirect.invalid/")
		return resp, nil
	})
	if _, err := a.Ping(context.Background()); err == nil || calls != 1 {
		t.Fatal("redirect should be rejected before sending another request")
	}
}

func TestNewzoeSyncHidesMissingProducts(t *testing.T) {
	a := testNewzoeClient(t, func(*http.Request) (*http.Response, error) {
		return newzoeTestResponse(200, `{"ok":true,"data":{"products":[{"id":21,"name":"Muse","price":"5.00","type":"automatic","stock":9,"max_quantity":2},{"id":22,"type":"manual"},{"id":23,"type":"automatic"}]}}`), nil
	})
	list, err := a.ListProducts(context.Background(), ListProductsOpts{IncludeInactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !list.IncludesInactive || list.Total != 3 {
		t.Fatalf("incomplete sync: %+v", list)
	}
	for _, p := range list.Items {
		switch p.ID {
		case 21:
			if !p.IsActive || p.SKUs[0].StockQuantity != 9 || p.MaxPurchaseQuantity != 2 || !strings.Contains(p.Content["zh-CN"].(string), "1 小时") {
				t.Fatal("wrong active product")
			}
		case 22, 24:
			if p.IsActive {
				t.Fatal("stale product still active")
			}
		default:
			t.Fatal("unexpected product")
		}
	}
}
