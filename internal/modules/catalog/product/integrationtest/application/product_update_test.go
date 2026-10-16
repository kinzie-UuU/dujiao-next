package integrationtest

import (
	"errors"
	"strconv"
	"testing"

	productwrite "github.com/dujiao-next/internal/modules/catalog/product/application/write"
	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestProductServiceUpdateKeepsMappedProductFulfillmentUpstream(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	category := categorydomain.Category{
		Slug:     "mapped-category",
		NameJSON: jsonmap.JSON{"zh-CN": "mapped-category"},
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}

	product := productdomain.Product{
		CategoryID:       category.ID,
		Slug:             "mapped-product",
		TitleJSON:        jsonmap.JSON{"zh-CN": "mapped-product"},
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(10)),
		PurchaseType:     constants.ProductPurchaseMember,
		FulfillmentType:  constants.FulfillmentTypeUpstream,
		ManualStockTotal: 0,
		IsMapped:         true,
		IsActive:         true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create mapped product failed: %v", err)
	}

	sku := productdomain.ProductSKU{
		ProductID:      product.ID,
		SKUCode:        productdomain.DefaultSKUCode,
		SpecValuesJSON: jsonmap.JSON{},
		PriceAmount:    money.FromDecimal(decimal.NewFromInt(10)),
		IsActive:       true,
	}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatalf("create mapped product sku failed: %v", err)
	}

	updated, err := svc.Write.Update(strconv.FormatUint(uint64(product.ID), 10), productwrite.CreateProductInput{
		CategoryID:      category.ID,
		Slug:            "mapped-product-updated",
		TitleJSON:       map[string]interface{}{"zh-CN": "mapped-product-updated"},
		PriceAmount:     decimal.NewFromInt(20),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		ManualStockTotal: func() *int {
			value := 0
			return &value
		}(),
		IsActive: func() *bool {
			value := true
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("update mapped product failed: %v", err)
	}
	if updated.FulfillmentType != constants.FulfillmentTypeUpstream {
		t.Fatalf("expected mapped product fulfillment type to remain upstream, got %s", updated.FulfillmentType)
	}

	reloaded, err := svc.Read.GetAdminByID(strconv.FormatUint(uint64(product.ID), 10))
	if err != nil {
		t.Fatalf("reload mapped product failed: %v", err)
	}
	if reloaded.FulfillmentType != constants.FulfillmentTypeUpstream {
		t.Fatalf("expected persisted fulfillment type upstream, got %s", reloaded.FulfillmentType)
	}
}

func TestProductServiceUpdateFiltersUnavailablePaymentChannels(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	category := categorydomain.Category{
		Slug:     "payment-channel-update-category",
		NameJSON: jsonmap.JSON{"zh-CN": "payment-channel-update-category"},
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}

	deletedChannel := createProductTestPaymentChannel(t, db, "Deleted", true, true)
	product := productdomain.Product{
		CategoryID:        category.ID,
		Slug:              "payment-channel-update",
		TitleJSON:         jsonmap.JSON{"zh-CN": "payment-channel-update"},
		PriceAmount:       money.FromDecimal(decimal.NewFromInt(10)),
		PurchaseType:      constants.ProductPurchaseMember,
		FulfillmentType:   constants.FulfillmentTypeAuto,
		PaymentChannelIDs: productdomain.EncodePaymentChannelIDs([]uint{deletedChannel.ID}),
		IsActive:          true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	updated, err := svc.Write.Update(strconv.FormatUint(uint64(product.ID), 10), productwrite.CreateProductInput{
		CategoryID:        category.ID,
		Slug:              product.Slug,
		TitleJSON:         map[string]interface{}{"zh-CN": "payment-channel-update"},
		PriceAmount:       decimal.NewFromInt(10),
		PurchaseType:      constants.ProductPurchaseMember,
		FulfillmentType:   constants.FulfillmentTypeAuto,
		PaymentChannelIDs: []uint{deletedChannel.ID},
		IsActive: func() *bool {
			value := true
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("update product failed: %v", err)
	}
	if got := productdomain.DecodePaymentChannelIDs(updated.PaymentChannelIDs); len(got) != 0 {
		t.Fatalf("expected stale-only payment channels to be cleared, got %v", got)
	}
}

func TestProductServiceUpdateRejectsInvalidPurchaseLimits(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	cat := categorydomain.Category{Slug: "test-purchase-limit-update", NameJSON: jsonmap.JSON{"zh-CN": "test"}}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	intPtr := func(v int) *int { return &v }

	created, err := svc.Write.Create(productwrite.CreateProductInput{
		CategoryID:          cat.ID,
		Slug:                "valid-limit-product",
		TitleJSON:           map[string]interface{}{"zh-CN": "valid"},
		PriceAmount:         decimal.NewFromInt(10),
		PurchaseType:        constants.ProductPurchaseMember,
		FulfillmentType:     constants.FulfillmentTypeManual,
		ManualStockTotal:    intPtr(1),
		MinPurchaseQuantity: intPtr(2),
		MaxPurchaseQuantity: intPtr(5),
	})
	if err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	_, err = svc.Write.Update(strconv.FormatUint(uint64(created.ID), 10), productwrite.CreateProductInput{
		CategoryID:          cat.ID,
		Slug:                "valid-limit-product",
		TitleJSON:           map[string]interface{}{"zh-CN": "valid"},
		PriceAmount:         decimal.NewFromInt(10),
		PurchaseType:        constants.ProductPurchaseMember,
		FulfillmentType:     constants.FulfillmentTypeManual,
		ManualStockTotal:    intPtr(1),
		MaxPurchaseQuantity: intPtr(1), // 已存在 min=2，新设 max=1 应触发校验
	})
	if !errors.Is(err, productcontract.ErrProductPurchaseLimitInvalid) {
		t.Fatalf("expected productcontract.ErrProductPurchaseLimitInvalid on update, got %v", err)
	}
}

func TestSecurityRegressionProductServiceUpdateWithoutStockKeepsReservedSKUStock(t *testing.T) {
	svc, db := newProductServiceForTest(t)
	category := categorydomain.Category{Slug: "keep-stock-category", NameJSON: jsonmap.JSON{"zh-CN": "keep-stock"}}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}
	product := productdomain.Product{
		CategoryID:       category.ID,
		Slug:             "keep-stock-product",
		TitleJSON:        jsonmap.JSON{"zh-CN": "keep-stock-product"},
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(10)),
		PurchaseType:     constants.ProductPurchaseMember,
		FulfillmentType:  constants.FulfillmentTypeManual,
		ManualStockTotal: 10,
		IsActive:         true,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}
	// 单规格 DEFAULT 与多规格各验证一次
	single := productdomain.ProductSKU{ProductID: product.ID, SKUCode: productdomain.DefaultSKUCode, PriceAmount: money.FromDecimal(decimal.NewFromInt(10)), ManualStockTotal: 7, ManualStockLocked: 3, IsActive: true}
	if err := db.Create(&single).Error; err != nil {
		t.Fatalf("create sku failed: %v", err)
	}
	active := true
	update := func(skus []productwrite.ProductSKUInput) {
		t.Helper()
		if _, err := svc.Write.Update(strconv.FormatUint(uint64(product.ID), 10), productwrite.CreateProductInput{
			CategoryID:      category.ID,
			Slug:            product.Slug,
			TitleJSON:       map[string]interface{}{"zh-CN": "renamed"},
			PriceAmount:     decimal.NewFromInt(10),
			PurchaseType:    constants.ProductPurchaseMember,
			FulfillmentType: constants.FulfillmentTypeManual,
			SKUs:            skus,
			IsActive:        &active,
		}); err != nil {
			t.Fatalf("update product failed: %v", err)
		}
	}
	update(nil)
	var reloaded productdomain.ProductSKU
	db.First(&reloaded, single.ID)
	if reloaded.ManualStockTotal != 7 {
		t.Fatalf("single spec: expected live stock 7 kept, got %d", reloaded.ManualStockTotal)
	}

	update([]productwrite.ProductSKUInput{{ID: single.ID, KeepManualStock: true, SKUCode: "A", PriceAmount: decimal.NewFromInt(10), IsActive: &active}})
	db.First(&reloaded, single.ID)
	if reloaded.ManualStockTotal != 7 {
		t.Fatalf("multi spec: expected live stock 7 kept, got %d", reloaded.ManualStockTotal)
	}

	// SKU-only stock edits must also update the product aggregate even when the
	// top-level manual_stock_total field is omitted by the admin form.
	for _, stock := range []int{12, 0, -1} {
		update([]productwrite.ProductSKUInput{{ID: single.ID, SKUCode: "A", ManualStockTotal: stock, PriceAmount: decimal.NewFromInt(10), IsActive: &active}})
		var current productdomain.Product
		if err := db.First(&current, product.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.ManualStockTotal != stock {
			t.Fatalf("aggregate after SKU-only edit=%d want %d", current.ManualStockTotal, stock)
		}
		// Existing SKU addressed by its code must keep stock as well.
		update([]productwrite.ProductSKUInput{{SKUCode: "A", KeepManualStock: true, PriceAmount: decimal.NewFromInt(10), IsActive: &active}})
		if err := db.First(&current, product.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&reloaded, single.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.ManualStockTotal != stock || reloaded.ManualStockTotal != stock || reloaded.ManualStockLocked != 3 {
			t.Fatalf("code matched stock lost: product=%d sku=%+v", current.ManualStockTotal, reloaded)
		}
	}
}
