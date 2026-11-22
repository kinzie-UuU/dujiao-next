package upstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewzoeExpandedCatalogAndPurchase(t *testing.T) {
	for _, tc := range []struct {
		id   uint
		term string
	}{
		{16, "30 分钟"}, {35, "24 小时"}, {36, "2FA"}, {37, "2FA"}, {38, "2FA"}, {39, "首次登录"}, {40, "质保 2 天"},
	} {
		t.Run(fmt.Sprint(tc.id), func(t *testing.T) {
			calls := 0
			a := testNewzoeClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == "GET" && r.URL.Path == "/api/v1/products" {
					return newzoeTestResponse(200, fmt.Sprintf(`{"ok":true,"data":{"products":[{"id":%d,"name":"New product","description":"<script>bad</script>","price":"5.50","type":"automatic","stock":0,"max_quantity":1}]}}`, tc.id)), nil
				}
				b, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/api/v1/orders" || string(b) != fmt.Sprintf(`{"product_id":%d,"quantity":1,"payment_method":"balance"}`, tc.id) || r.Header.Get("Idempotency-Key") != "NEW-PRODUCT" {
					t.Fatalf("unexpected purchase: %s %s %s", r.Method, r.URL.Path, b)
				}
				return newzoeTestResponse(201, `{"ok":true,"data":{"order":{"id":"NEW-ORDER","amount":"5.00","currency":"CNY","quantity":1,"status":"completed"}}}`), nil
			})
			p, err := a.GetProduct(context.Background(), tc.id)
			if err != nil || p.ID != tc.id || len(p.SKUs) != 1 || p.SKUs[0].SKUCode != fmt.Sprintf("NEWZOE-%d", tc.id) || p.SKUs[0].StockQuantity != 0 {
				t.Fatalf("bad product: %+v %v", p, err)
			}
			terms := p.Content["zh-CN"].(string)
			if !strings.Contains(terms, tc.term) {
				t.Fatalf("missing product term: %s", terms)
			}
			if tc.id == 40 && strings.Contains(terms, "首次登录") {
				t.Fatal("two-day warranty replaced by first-login warranty")
			}
			if strings.Contains(p.Description["zh-CN"].(string), "<script>") {
				t.Fatal("unescaped supplier description")
			}
			result, err := a.CreateOrder(context.Background(), CreateUpstreamOrderReq{SKUID: tc.id, Quantity: 1, DownstreamOrderNo: "NEW-PRODUCT"})
			if err != nil || !result.OK || result.Amount != "5.00" || calls != 2 {
				t.Fatalf("bad purchase: %+v %v", result, err)
			}
		})
	}
}

func TestNewzoeExpandedProductsRejectUnsafeCatalogEntries(t *testing.T) {
	for _, id := range []uint{16, 35, 36, 37, 38, 39, 40} {
		for _, fields := range []string{
			`"price":"1","type":"manual","stock":1`,
			`"price":"1","type":"automatic","stock":1,"input_fields":[{"name":"password"}]`,
			`"price":"0","type":"automatic","stock":1`,
			`"price":"invalid","type":"automatic","stock":1`,
			`"price":"1","type":"automatic","stock":-1`,
			`"price":"1","type":"automatic"`,
		} {
			t.Run(fmt.Sprintf("%d/%s", id, fields), func(t *testing.T) {
				a := testNewzoeClient(t, func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" {
						t.Fatal("catalog validation made a purchase")
					}
					return newzoeTestResponse(200, fmt.Sprintf(`{"ok":true,"data":{"products":[{"id":%d,"name":"Test",%s}]}}`, id, fields)), nil
				})
				if _, err := a.GetProduct(context.Background(), id); err == nil {
					t.Fatal("unsafe product accepted")
				}
			})
		}
	}
}
