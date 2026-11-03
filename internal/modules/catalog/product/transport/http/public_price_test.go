package producthttp

import (
	"encoding/json"
	"errors"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"strings"
	"testing"

	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	reseller "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

type stubResellerDisplayPricer struct {
	result *reseller.DisplayPriceResult
	err    error
}

func (s stubResellerDisplayPricer) LoadDisplayPricingBatch(tenant reseller.TenantContext, products []productdomain.Product) (*reseller.DisplayPricingBatch, error) {
	return &reseller.DisplayPricingBatch{Tenant: tenant}, nil
}

func (s stubResellerDisplayPricer) ResolveDisplayPrices(tenant reseller.TenantContext, product *productdomain.Product, batch *reseller.DisplayPricingBatch) (*reseller.DisplayPriceResult, error) {
	return s.result, s.err
}

func TestDecoratePublicProductDisplayPricePrefersFirstActiveSKU(t *testing.T) {
	h := &PublicHandler{}
	product := &productdomain.Product{
		ID:          1,
		PriceAmount: money.FromDecimal(decimal.RequireFromString("59.90")),
		SKUs: []productdomain.ProductSKU{
			{
				ID:          11,
				IsActive:    true,
				SortOrder:   100,
				PriceAmount: money.FromDecimal(decimal.RequireFromString("89.90")),
			},
			{
				ID:          12,
				IsActive:    true,
				SortOrder:   10,
				PriceAmount: money.FromDecimal(decimal.RequireFromString("49.90")),
			},
		},
	}

	item, err := h.decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}
	expected := decimal.RequireFromString("89.90")
	if !item.PriceAmount.Decimal.Equal(expected) {
		t.Fatalf("expected display price %s, got: %s", expected.String(), item.PriceAmount.String())
	}
}

func TestDecoratePublicProductForTenantUsesResellerPricesAndHidesMainDiscounts(t *testing.T) {
	product := &productdomain.Product{
		ID:          1,
		Slug:        "reseller-display",
		PriceAmount: money.FromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: productdomain.WholesalePriceTiers{
			{MinQuantity: 5, UnitPrice: money.FromDecimal(decimal.NewFromInt(80))},
		},
		SKUs: []productdomain.ProductSKU{
			{
				ID:          11,
				ProductID:   1,
				SKUCode:     "VISIBLE",
				IsActive:    true,
				SortOrder:   100,
				PriceAmount: money.FromDecimal(decimal.NewFromInt(100)),
			},
			{
				ID:          12,
				ProductID:   1,
				SKUCode:     "HIDDEN",
				IsActive:    true,
				SortOrder:   10,
				PriceAmount: money.FromDecimal(decimal.NewFromInt(90)),
			},
		},
	}
	h := &PublicHandler{pricer: stubResellerDisplayPricer{result: &reseller.DisplayPriceResult{
		Visible:      true,
		ProductID:    1,
		DisplaySKUID: 11,
		DisplayPrice: money.FromDecimal(decimal.NewFromInt(130)),
		SKUPrices: map[uint]money.Amount{
			11: money.FromDecimal(decimal.NewFromInt(130)),
		},
		HiddenSKUIDs: map[uint]bool{12: true},
	}}}
	tenant := reseller.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")

	item, err := h.decoratePublicProductForTenant(product, nil, tenant, &reseller.DisplayPricingBatch{})
	if err != nil {
		t.Fatalf("decoratePublicProductForTenant failed: %v", err)
	}
	if !item.PriceAmount.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller product price 130, got %s", item.PriceAmount.String())
	}
	if len(item.SKUs) != 1 || item.SKUs[0].ID != 11 {
		t.Fatalf("expected only visible sku 11, got %+v", item.SKUs)
	}
	if !item.SKUs[0].PriceAmount.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller sku price 130, got %s", item.SKUs[0].PriceAmount.String())
	}
	if item.PromotionPriceAmount != nil || item.PromotionID != nil || len(item.PromotionRules) > 0 {
		t.Fatalf("reseller display must not expose main promotion fields: %+v", item)
	}
	if len(item.WholesalePrices) > 0 {
		t.Fatalf("reseller display must not expose main wholesale prices: %+v", item.WholesalePrices)
	}
}

func TestDecoratePublicProductForTenantHiddenProduct(t *testing.T) {
	h := &PublicHandler{pricer: stubResellerDisplayPricer{result: &reseller.DisplayPriceResult{Visible: false}}}
	tenant := reseller.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")
	product := &productdomain.Product{
		ID:          1,
		PriceAmount: money.FromDecimal(decimal.NewFromInt(100)),
		SKUs: []productdomain.ProductSKU{
			{ID: 11, ProductID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(100))},
		},
	}

	_, err := h.decoratePublicProductForTenant(product, nil, tenant, &reseller.DisplayPricingBatch{})
	if !errors.Is(err, productcontract.ErrResellerProductNotListed) {
		t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
	}
}

func TestDecoratePublicProductForTenantAllHiddenSKUsReturnsNotListed(t *testing.T) {
	h := &PublicHandler{pricer: stubResellerDisplayPricer{result: &reseller.DisplayPriceResult{
		Visible:      true,
		DisplayPrice: money.FromDecimal(decimal.NewFromInt(100)),
		HiddenSKUIDs: map[uint]bool{11: true, 12: true},
	}}}
	tenant := reseller.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")
	product := &productdomain.Product{
		ID:          1,
		PriceAmount: money.FromDecimal(decimal.NewFromInt(100)),
		SKUs: []productdomain.ProductSKU{
			{ID: 11, ProductID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(100))},
			{ID: 12, ProductID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(120))},
		},
	}

	_, err := h.decoratePublicProductForTenant(product, nil, tenant, &reseller.DisplayPricingBatch{})
	if !errors.Is(err, productcontract.ErrResellerProductNotListed) {
		t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
	}
}

func TestDecoratePublicProductForTenantInvalidDisplayPricingIsHidden(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "below base", err: reseller.ErrPriceBelowBase},
		{name: "markup exceeds max", err: reseller.ErrMarkupExceeded},
		{name: "unknown pricing mode", err: reseller.ErrPricingModeInvalid},
		{name: "already not listed", err: productcontract.ErrResellerProductNotListed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &PublicHandler{pricer: stubResellerDisplayPricer{err: tt.err}}
			tenant := reseller.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")
			product := &productdomain.Product{
				ID:          1,
				PriceAmount: money.FromDecimal(decimal.NewFromInt(100)),
				SKUs: []productdomain.ProductSKU{
					{ID: 11, ProductID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(100))},
				},
			}
			_, err := h.decoratePublicProductForTenant(product, nil, tenant, &reseller.DisplayPricingBatch{})
			if !errors.Is(err, productcontract.ErrResellerProductNotListed) {
				t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
			}
		})
	}
}

func TestProductSKUStockOmissionDiffersFromExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		body  string
		keep  bool
		stock int
	}{
		{`[{"id":1,"sku_code":"A","price_amount":10}]`, true, 0},
		{`[{"id":1,"sku_code":"A","price_amount":10,"manual_stock_total":0}]`, false, 0},
		{`[{"id":1,"sku_code":"A","price_amount":10,"manual_stock_total":9}]`, false, 9},
	} {
		var request []ProductSKURequest
		if err := json.Unmarshal([]byte(tc.body), &request); err != nil {
			t.Fatal(err)
		}
		got := toProductSKUInputs(request)[0]
		if got.KeepManualStock != tc.keep || got.ManualStockTotal != tc.stock {
			t.Fatalf("request %s: %+v", tc.body, got)
		}
	}
}

func TestPublicProductResponseStripsActiveMarkupFromStoredLocales(t *testing.T) {
	content := jsonmap.JSON{
		"zh-CN": `<section data-uuuaigc-service-rules="20260718"><p>说明</p><a href="/faq">帮助</a></section><img src="/uploads/i" style="width:100%;height:auto" onerror="alert(1)">`,
		"zh-TW": `<a href="jav&#x61;script:alert(1)">連結</a><video controls preload="metadata" src="/uploads/v" style="width:100%;max-width:960px"></video><svg><animate onbegin="alert(1)"></animate></svg>`,
		"en-US": `<h3>Usage</h3><strong>Sign in</strong><iframe srcdoc="bad"></iframe><script>alert(1)</script><img src="data:text/html,alert(1)">`,
	}
	view := publicProductView{Product: productdomain.Product{ContentJSON: content}}
	got := view.toProductResp().Content
	for locale, required := range map[string][]string{
		"zh-CN": {`data-uuuaigc-service-rules="20260718"`, "说明", `href="/faq"`, `src="/uploads/i"`, "100%", "auto"},
		"zh-TW": {"連結", "<video", "controls", `src="/uploads/v"`, "960px"},
		"en-US": {"<h3>Usage</h3>", "<strong>Sign in</strong>"},
	} {
		html := got[locale].(string)
		for _, bad := range []string{"alert", "onerror", "onbegin", "javascript:", "<svg", "<iframe", "srcdoc", "<script", "data:text/html"} {
			if strings.Contains(html, bad) {
				t.Errorf("%s retained %q: %s", locale, bad, html)
			}
		}
		for _, term := range required {
			if !strings.Contains(html, term) {
				t.Errorf("%s lost %q: %s", locale, term, html)
			}
		}
	}
	if !strings.Contains(content["zh-CN"].(string), "onerror") {
		t.Error("public read mutated stored content")
	}
}
