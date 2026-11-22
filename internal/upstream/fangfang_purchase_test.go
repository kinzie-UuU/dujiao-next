package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const fangfangPurchaseFixture = `{"success":true,"order":{"orderCode":"ORDER_123","status":"completed","productId":"6aeedc6cadf360530f5faf80","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":0.96,"currency":"USD"},"delivery":{"accounts":[{"user":"buyer@example.com","password":"private-delivery","otherInfo":"preserved"}]}}`

func fangfangPurchaseInput() FangfangPurchaseRequest {
	return FangfangPurchaseRequest{ProductID: "6aeedc6cadf360530f5faf80", Quantity: 1, IdempotencyKey: "order-fixed-123", PreflightMaxUSD: "1.00"}
}
func fangfangPurchaseMock(t *testing.T, post fangfangTestTransport) *FangfangPurchaseClient {
	t.Helper()
	a := fangfangTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			if r.URL.String() != fangfangOrigin+"/api/v2/telegram-buyer/products" {
				t.Fatalf("unexpected preflight path")
			}
			return fangfangTestResponse(200, `{"success":true,"walletCurrency":"USD","products":[`+fangfangMuseFixture+`]}`), nil
		}
		return post(r)
	})
	return &FangfangPurchaseClient{adapter: a}
}
func fangfangRequirePurchaseKind(t *testing.T, err error, kind string) *FangfangPurchaseError {
	t.Helper()
	var e *FangfangPurchaseError
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("expected %s, got %v", kind, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-delivery") {
		t.Fatal("secret leaked")
	}
	return e
}
func TestFangfangPurchaseSuccess(t *testing.T) {
	calls := 0
	c := fangfangPurchaseMock(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != fangfangOrigin+"/api/v2/telegram-buyer/purchase" || r.Header.Get("Idempotency-Key") != "order-fixed-123" || r.GetBody != nil || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("unsafe request")
		}
		b, _ := io.ReadAll(r.Body)
		var data map[string]any
		if json.Unmarshal(b, &data) != nil || len(data) != 3 || data["key"] != "secret-do-not-log" || data["quantity"] != float64(1) || data["product_id"] != fangfangPurchaseInput().ProductID {
			t.Fatal("invalid request body")
		}
		return fangfangTestResponse(200, fangfangPurchaseFixture), nil
	})
	out, err := c.PurchaseOnce(context.Background(), fangfangPurchaseInput())
	if err != nil || calls != 1 || out.OrderCode != "ORDER_123" || out.AmountUSD != "0.96" || out.FinalQuantity != 1 || !strings.Contains(string(out.Accounts[0]), "preserved") {
		t.Fatalf("purchase did not preserve validated result: %v", err)
	}
}
func TestFangfangPurchaseValidationNoPost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FangfangPurchaseRequest)
		kind   string
	}{
		{"unknown product", func(r *FangfangPurchaseRequest) { r.ProductID = "6aeedc6cadf360530f5faf81" }, "invalid_request"},
		{"zero quantity", func(r *FangfangPurchaseRequest) { r.Quantity = 0 }, "invalid_request"},
		{"too many", func(r *FangfangPurchaseRequest) { r.Quantity = 1001 }, "invalid_request"},
		{"missing key", func(r *FangfangPurchaseRequest) { r.IdempotencyKey = "" }, "invalid_request"},
		{"header injection", func(r *FangfangPurchaseRequest) { r.IdempotencyKey = "abcdefgh\r\nInjected: x" }, "invalid_request"},
		{"missing cap", func(r *FangfangPurchaseRequest) { r.PreflightMaxUSD = "" }, "invalid_request"},
		{"price cap", func(r *FangfangPurchaseRequest) { r.PreflightMaxUSD = "0.95" }, "rejected"},
		{"stock", func(r *FangfangPurchaseRequest) { r.Quantity = 4; r.PreflightMaxUSD = "10" }, "rejected"},
		{"missing allowlisted item", func(r *FangfangPurchaseRequest) { r.ProductID = "6a4b4d7d1827db4ca39bb8be" }, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fangfangPurchaseMock(t, func(*http.Request) (*http.Response, error) { t.Fatal("must not POST"); return nil, nil })
			in := fangfangPurchaseInput()
			tc.mutate(&in)
			_, err := c.PurchaseOnce(context.Background(), in)
			fangfangRequirePurchaseKind(t, err, tc.kind)
		})
	}
}
func TestFangfangPurchaseUncertainNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		network    bool
	}{
		{"timeout", "", 0, true},
		{"server error", `{"success":false}`, 503, false},
		{"in progress", `{"success":false}`, 409, false},
		{"non JSON", "private-secret", 200, false},
		{"missing success", `{}`, 200, false},
		{"wrong currency", strings.Replace(fangfangPurchaseFixture, `"USD"`, `"VND"`, 1), 200, false},
		{"wrong product", strings.Replace(fangfangPurchaseFixture, "6aeedc6cadf360530f5faf80", "6a4b4d7d1827db4ca39bb8be", 1), 200, false},
		{"wrong quantity", strings.Replace(fangfangPurchaseFixture, `"quantity":1`, `"quantity":2`, 1), 200, false},
		{"wrong final count", strings.Replace(fangfangPurchaseFixture, `"finalQuantity":1`, `"finalQuantity":2`, 1), 200, false},
		{"missing bonus", strings.Replace(fangfangPurchaseFixture, `"bonusQuantity":0,`, "", 1), 200, false},
		{"missing delivery password", strings.Replace(fangfangPurchaseFixture, `"password":"private-delivery"`, `"password":""`, 1), 200, false},
		{"paid not completed", strings.Replace(fangfangPurchaseFixture, `"completed"`, `"paid"`, 1), 200, false},
		{"over cap", strings.Replace(fangfangPurchaseFixture, `"amount":0.96`, `"amount":1.01`, 1), 200, false},
		{"missing amount", strings.Replace(fangfangPurchaseFixture, `"amount":0.96,`, "", 1), 200, false},
		{"oversize", strings.Repeat("x", fangfangResponseLimit+1), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := fangfangPurchaseMock(t, func(*http.Request) (*http.Response, error) {
				calls++
				if tc.network {
					return nil, errors.New("secret transport error")
				}
				return fangfangTestResponse(tc.status, tc.body), nil
			})
			out, err := c.PurchaseOnce(context.Background(), fangfangPurchaseInput())
			e := fangfangRequirePurchaseKind(t, err, "uncertain")
			if calls != 1 || (tc.network && out != nil) || (!tc.network && (out == nil || string(out.RawResponse) != tc.body)) {
				t.Fatal("must never retry or deliver uncertain purchase")
			}
			if strings.Contains(tc.body, `"orderCode":"ORDER_123"`) && e.OrderCode != "ORDER_123" {
				t.Fatal("missing reconciliation reference")
			}
		})
	}
}
func TestFangfangPurchaseExplicitRejection(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429} {
		c := fangfangPurchaseMock(t, func(*http.Request) (*http.Response, error) {
			return fangfangTestResponse(status, `{"success":false,"message":"secret"}`), nil
		})
		_, err := c.PurchaseOnce(context.Background(), fangfangPurchaseInput())
		fangfangRequirePurchaseKind(t, err, "uncertain")
	}
}

func TestFangfangPurchasePreservesDeliveryOnUncertainResponse(t *testing.T) {
	for _, status := range []int{200, 503} {
		body := strings.Replace(fangfangPurchaseFixture, `"amount":0.96`, `"amount":1.01`, 1)
		c := fangfangPurchaseMock(t, func(*http.Request) (*http.Response, error) {
			return fangfangTestResponse(status, body), nil
		})
		out, err := c.PurchaseOnce(context.Background(), fangfangPurchaseInput())
		fangfangRequirePurchaseKind(t, err, "uncertain")
		if out == nil || out.HTTPStatus != status || string(out.RawResponse) != body || len(out.Accounts) != 1 || !strings.Contains(string(out.Accounts[0]), "private-delivery") {
			t.Fatal("uncertain receipt or delivery was not preserved")
		}
	}
}

func TestFangfangPurchaseResultOmitsSecretsFromJSON(t *testing.T) {
	result := FangfangPurchaseResult{RawResponse: []byte(`{"password":"raw-secret"}`), Accounts: []json.RawMessage{json.RawMessage(`{"password":"account-secret"}`)}}
	serialized, err := json.Marshal(result)
	if err != nil || strings.Contains(string(serialized), "raw-secret") || strings.Contains(string(serialized), "account-secret") || strings.Contains(string(serialized), "RawResponse") || strings.Contains(string(serialized), "Accounts") {
		t.Fatal("purchase response secrets exposed in JSON")
	}
}
func TestFangfangPurchaseRedirectNotFollowed(t *testing.T) {
	calls := 0
	c := fangfangPurchaseMock(t, func(*http.Request) (*http.Response, error) {
		calls++
		r := fangfangTestResponse(307, `{"success":false}`)
		r.Header.Set("Location", "https://example.com/steal")
		return r, nil
	})
	_, err := c.PurchaseOnce(context.Background(), fangfangPurchaseInput())
	fangfangRequirePurchaseKind(t, err, "uncertain")
	if calls != 1 {
		t.Fatal("redirect followed")
	}
}

func TestFangfangPurchasePlusAndBonus(t *testing.T) {
	in := fangfangPurchaseInput()
	in.ProductID = "6a4b4d7d1827db4ca39bb8be"
	in.PreflightMaxUSD = "5.74"
	fixture := strings.ReplaceAll(fangfangMuseFixture, "6aeedc6cadf360530f5faf80", in.ProductID)
	fixture = strings.Replace(fixture, `"amount":0.96`, `"amount":5.74`, 1)
	success := strings.ReplaceAll(fangfangPurchaseFixture, "6aeedc6cadf360530f5faf80", in.ProductID)
	success = strings.Replace(success, `"amount":0.96`, `"amount":5.74`, 1)
	success = strings.Replace(success, `"bonusQuantity":0,"finalQuantity":1`, `"bonusQuantity":1,"finalQuantity":2`, 1)
	success = strings.Replace(success, `"accounts":[`, `"accounts":[{"user":"bonus@example.com","password":"bonus-private"},`, 1)
	calls := 0
	a := fangfangTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return fangfangTestResponse(200, `{"success":true,"walletCurrency":"USD","products":[`+fixture+`]}`), nil
		}
		calls++
		return fangfangTestResponse(200, success), nil
	})
	out, err := (&FangfangPurchaseClient{adapter: a}).PurchaseOnce(context.Background(), in)
	if err != nil || calls != 1 || out.FinalQuantity != 2 || len(out.Accounts) != 2 || out.AmountUSD != "5.74" {
		t.Fatalf("plus/bonus result invalid: %v", err)
	}
}
func TestFangfangPurchasePreflightCurrencyNoPost(t *testing.T) {
	a := fangfangTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("must not purchase VND wallet")
		}
		return fangfangTestResponse(200, `{"success":true,"walletCurrency":"VND","products":[`+fangfangMuseFixture+`]}`), nil
	})
	_, err := (&FangfangPurchaseClient{adapter: a}).PurchaseOnce(context.Background(), fangfangPurchaseInput())
	fangfangRequirePurchaseKind(t, err, "rejected")
}
