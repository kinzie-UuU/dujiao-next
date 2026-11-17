package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

type fangfangTestTransport func(*http.Request) (*http.Response, error)

// Optional real-service verification; never calls a purchase endpoint or logs credentials.
func TestFangfangLiveReadOnly(t *testing.T) {
	path := os.Getenv("FANGFANG_READONLY_KEY_FILE")
	if path == "" {
		t.Skip("read-only credential not provided")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private credential file")
	}
	a, err := newFangfangAdapter(&siteconnectiondomain.Connection{BaseURL: fangfangOrigin, ApiSecret: strings.TrimSpace(string(key))})
	if err != nil {
		t.Fatal(err)
	}
	balance, err := a.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balance.Currency != "USD" {
		t.Fatal("unexpected wallet currency")
	}
	products, err := a.ListProducts(context.Background(), ListProductsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if products.Total != 2 {
		t.Fatal("expected both reviewed products")
	}
	for _, p := range products.Items {
		t.Logf("upstream=%d currency=%s price=%s stock=%d", p.ID, p.Currency, p.PriceAmount, p.SKUs[0].StockQuantity)
	}
}

func TestFangfangProtocolRegistration(t *testing.T) {
	a, err := NewAdapter(&siteconnectiondomain.Connection{Protocol: constants.ConnectionProtocolFangfang, BaseURL: fangfangOrigin, ApiSecret: "test-secret"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(*fangfangAdapter); !ok {
		t.Fatal("incorrect adapter")
	}
}

func (f fangfangTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fangfangTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func fangfangTestClient(t *testing.T, transport fangfangTestTransport) *fangfangAdapter {
	t.Helper()
	a, err := newFangfangAdapter(&siteconnectiondomain.Connection{BaseURL: fangfangOrigin, ApiSecret: "secret-do-not-log"})
	if err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transport
	return a
}

const fangfangMuseFixture = `{"productId":"6aeedc6cadf360530f5faf80","name":"Muse 31B ACTIVE","description":"<script>bad</script>","productType":"account","price":{"amount":0.96,"currency":"USD"},"availability":{"available":3}}`

func fangfangCatalog(products string) string {
	return `{"success":true,"walletCurrency":"USD","products":[` + products + `]}`
}

func TestFangfangReadOnlyCatalog(t *testing.T) {
	calls := 0
	a := fangfangTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "canboso.com" || r.URL.RawQuery != "" || r.Header.Get("x-buyer-key") != "secret-do-not-log" || r.Header.Get("User-Agent") == "" {
			t.Fatal("unsafe request")
		}
		switch r.URL.Path {
		case "/api/v2/telegram-buyer/balance":
			return fangfangTestResponse(200, `{"success":true,"walletCurrency":"USD","balance":0.123456}`), nil
		case "/api/v2/telegram-buyer/products":
			return fangfangTestResponse(200, fangfangCatalog(fangfangMuseFixture+`,{"productId":"not-reviewed","name":"Do not import"}`)), nil
		default:
			t.Fatal("unexpected endpoint")
			return nil, nil
		}
	})
	balance, err := a.Ping(context.Background())
	if err != nil || balance.Currency != "USD" || balance.Balance != "0.123456" {
		t.Fatalf("balance: %+v %v", balance, err)
	}
	list, err := a.ListProducts(context.Background(), ListProductsOpts{IncludeInactive: true})
	if err != nil || list.Total != 1 || list.IncludesInactive || len(list.Items) != 1 {
		t.Fatalf("catalog: %+v %v", list, err)
	}
	p := list.Items[0]
	if p.ID != 1 || p.SKUs[0].SKUCode != "6aeedc6cadf360530f5faf80" || p.PriceAmount != "0.96" || p.Currency != "USD" || p.SKUs[0].StockQuantity != 3 || strings.Contains(p.Content["zh-CN"].(string), "<script>") {
		t.Fatalf("product: %+v", p)
	}
	for _, opts := range []ListProductsOpts{{Page: 2, PageSize: 1}, {Page: int(^uint(0) >> 1), PageSize: 2}} {
		list, err = a.ListProducts(context.Background(), opts)
		if err != nil || list.Total != 1 || len(list.Items) != 0 {
			t.Fatal("invalid pagination")
		}
	}
	if _, err = a.GetProduct(context.Background(), 99); !errors.Is(err, ErrUpstreamProductUnavailable) {
		t.Fatal("missing product accepted")
	}
	before := calls
	resp, err := a.CreateOrder(context.Background(), CreateUpstreamOrderReq{SKUID: 1, Quantity: 1})
	if err != nil || resp.OK || resp.ErrorCode != "invalid_request" {
		t.Fatal("purchase did not fail closed")
	}
	if _, err = a.GetOrder(context.Background(), 1); err == nil {
		t.Fatal("order lookup enabled")
	}
	if err = a.CancelOrder(context.Background(), 1); err == nil {
		t.Fatal("cancellation enabled")
	}
	if _, err = a.DownloadImage(context.Background(), "https://example.com/x"); err == nil {
		t.Fatal("download enabled")
	}
	if err = a.read(context.Background(), "/purchase", &struct{}{}); err == nil {
		t.Fatal("arbitrary endpoint enabled")
	}
	if calls != before {
		t.Fatal("disabled operation made a request")
	}
}
func TestFangfangValidation(t *testing.T) {
	for _, body := range []string{
		`{"success":false,"walletCurrency":"USD","products":[]}`,
		`{"success":true,"walletCurrency":"VND","products":[]}`,
		`{"success":true,"walletCurrency":"USD","products":null}`,
		`{"success":true,"walletCurrency":"USD"}`,
		fangfangCatalog(fangfangMuseFixture + "," + fangfangMuseFixture),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"USD"`, `"VND"`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"available":3`, `"available":null`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"available":3`, `"available":-1`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"account"`, `"upgrade_account"`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"amount":0.96`, `"amount":-0.96`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"amount":0.96`, `"amount":0`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"amount":0.96`, `"amount":1e100`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"name":"Muse 31B ACTIVE"`, `"name":" "`, 1)),
		fangfangCatalog(strings.Replace(fangfangMuseFixture, `"productType":"account"`, `"productType":"account","purchaseRequirements":{"customerEmail":true}`, 1)),
	} {
		a := fangfangTestClient(t, func(*http.Request) (*http.Response, error) { return fangfangTestResponse(200, body), nil })
		if _, err := a.ListProducts(context.Background(), ListProductsOpts{}); err == nil {
			t.Fatalf("invalid catalog accepted: %s", body)
		}
	}
	a := fangfangTestClient(t, func(*http.Request) (*http.Response, error) {
		return fangfangTestResponse(200, fangfangCatalog(strings.Replace(fangfangMuseFixture, `"available":3`, `"available":0`, 1))), nil
	})
	list, err := a.ListProducts(context.Background(), ListProductsOpts{})
	if err != nil || list.Items[0].SKUs[0].StockStatus != constants.ProductStockStatusOutOfStock || list.Items[0].SKUs[0].StockQuantity != 0 {
		t.Fatal("zero stock lost")
	}
	for _, balance := range []string{`null`, `-1`, `1e100`, `"invalid"`} {
		a := fangfangTestClient(t, func(*http.Request) (*http.Response, error) {
			return fangfangTestResponse(200, `{"success":true,"walletCurrency":"USD","balance":`+balance+`}`), nil
		})
		if _, err := a.Ping(context.Background()); err == nil {
			t.Fatal("invalid balance accepted")
		}
	}
}
func TestFangfangSecurity(t *testing.T) {
	for _, conn := range []*siteconnectiondomain.Connection{nil, {BaseURL: "http://canboso.com", ApiSecret: "key"}, {BaseURL: "https://canboso.com.evil", ApiSecret: "key"}, {BaseURL: fangfangOrigin}, {BaseURL: fangfangOrigin, ApiSecret: "bad\nkey"}} {
		if _, err := newFangfangAdapter(conn); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	calls := 0
	a := fangfangTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		r := fangfangTestResponse(302, "secret-do-not-log")
		r.Header.Set("Location", "https://example.com/secret-do-not-log")
		return r, nil
	})
	if _, err := a.Ping(context.Background()); err == nil || strings.Contains(err.Error(), "secret-do-not-log") || calls != 1 {
		t.Fatal("redirect followed or secret leaked")
	}
	a.client.Transport = fangfangTestTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-do-not-log") })
	if _, err := a.Ping(context.Background()); err == nil || strings.Contains(err.Error(), "secret-do-not-log") {
		t.Fatal("transport secret leaked")
	}
	for _, body := range []string{`secret-do-not-log`, strings.Repeat("x", fangfangResponseLimit+1)} {
		a.client.Transport = fangfangTestTransport(func(*http.Request) (*http.Response, error) { return fangfangTestResponse(200, body), nil })
		if _, err := a.Ping(context.Background()); err == nil || strings.Contains(err.Error(), "secret-do-not-log") {
			t.Fatal("invalid body accepted/leaked")
		}
	}
}

func TestFangfangReviewedPlus(t *testing.T) {
	body := fangfangCatalog(`{"productId":"6a4b4d7d1827db4ca39bb8be","name":"ChatGPT Plus Apple Pay Gmail 30天 - 全保修","description":"Full warranty; exclusions apply","productType":"account","price":{"amount":5.74,"currency":"USD"},"availability":{"available":71},"promotions":[{"unitPrice":145000}]}`)
	a := fangfangTestClient(t, func(*http.Request) (*http.Response, error) { return fangfangTestResponse(200, body), nil })
	p, err := a.GetProduct(context.Background(), 2)
	if err != nil || p.ID != 2 || p.SKUs[0].SKUCode != "6a4b4d7d1827db4ca39bb8be" || p.PriceAmount != "5.74" || p.SKUs[0].StockQuantity != 71 || len(p.WholesalePrices) != 0 {
		t.Fatalf("Plus mapping or currency incorrect: %+v %v", p, err)
	}
}
