package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"

	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	categoryapp "github.com/dujiao-next/internal/modules/catalog/category/application"
	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"

	"github.com/dujiao-next/internal/constants"
	categorygormstore "github.com/dujiao-next/internal/modules/catalog/category/infrastructure/gormstore"
	mappingapp "github.com/dujiao-next/internal/modules/catalog/mapping/application"
	mappingcontract "github.com/dujiao-next/internal/modules/catalog/mapping/contract"
	mappinggormstore "github.com/dujiao-next/internal/modules/catalog/mapping/infrastructure/gormstore"
	mappinghttp "github.com/dujiao-next/internal/modules/catalog/mapping/transport/http"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type adminNoopLocalMediaRecorder struct{}

func (adminNoopLocalMediaRecorder) RecordLocalFile(context.Context, string, string) {}

type mappingConnectionProvider struct {
	db      *gorm.DB
	adapter upstream.Adapter
}

func (p *mappingConnectionProvider) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := p.db.First(&conn, id).Error; err != nil {
		return nil, err
	}
	return &conn, nil
}

func (p *mappingConnectionProvider) GetAdapter(conn *siteconnectiondomain.Connection) (upstream.Adapter, error) {
	return p.adapter, nil
}

type mappingImportUoW struct {
	products    *productgormstore.ProductStore
	skus        *productgormstore.SKUStore
	mappings    *mappinggormstore.MappingStore
	skuMappings *mappinggormstore.SKUMappingStore
}

func (unit *mappingImportUoW) WithinTransaction(fn func(mappingcontract.ImportRepositories) error) error {
	if fn == nil {
		return nil
	}
	return unit.products.Transaction(func(tx *gorm.DB) error {
		return fn(mappingcontract.ImportRepositories{
			Products:    unit.products.BindTx(tx),
			SKUs:        unit.skus.BindTx(tx),
			Mappings:    unit.mappings.WithTx(tx),
			SKUMappings: unit.skuMappings.WithTx(tx),
		})
	})
}

func setupAdminHandlerTest(t *testing.T, protocol string, upstreamHandler http.HandlerFunc) (*mappinghttp.AdminHandler, *gorm.DB, uint, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_product_mapping_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&categorydomain.Category{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&siteconnectiondomain.Connection{},
		&mappingdomain.Mapping{},
		&mappingdomain.SKUMapping{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	server := httptest.NewServer(upstreamHandler)
	categoryRepo := categorygormstore.NewCategoryStore(db)
	categoryService := categoryapp.NewService(categoryRepo)

	conn := siteconnectiondomain.Connection{
		Name:      "upstream",
		BaseURL:   server.URL,
		ApiKey:    "test-key",
		ApiSecret: "test-secret",
		Protocol:  protocol,
		Status:    constants.ConnectionStatusActive,
	}
	// Newzoe only signs requests to its fixed HTTPS origin. Route those requests
	// directly to the fixture without opening an external network connection.
	if protocol == constants.ConnectionProtocolNewzoe {
		conn.BaseURL = "https://shop.newzoe.cloud"
		previous := http.DefaultTransport
		http.DefaultTransport = fixtureTransport(upstreamHandler)
		t.Cleanup(func() { http.DefaultTransport = previous })
	}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatalf("create connection failed: %v", err)
	}

	adapter, err := upstream.NewAdapter(&conn, t.TempDir())
	if err != nil {
		t.Fatalf("create upstream adapter failed: %v", err)
	}

	productStore := productgormstore.NewProductStore(db)
	skuStore := productgormstore.NewSKUStore(db)
	mappingStore := mappinggormstore.NewMappingStore(db)
	skuMappingStore := mappinggormstore.NewSKUMappingStore(db)

	mappingService, err := mappingapp.NewService(mappingapp.Options{
		Mappings:    mappingStore,
		SKUMappings: skuMappingStore,
		Products:    productStore,
		SKUs:        skuStore,
		Categories:  categoryRepo,
		Connections: &mappingConnectionProvider{db: db, adapter: adapter},
		Media:       adminNoopLocalMediaRecorder{},
		Transactions: &mappingImportUoW{
			products:    productStore,
			skus:        skuStore,
			mappings:    mappingStore,
			skuMappings: skuMappingStore,
		},
	})
	if err != nil {
		t.Fatalf("create product mapping service: %v", err)
	}
	mappingService.SetCategoryCreator(categoryService)

	h := mappinghttp.NewAdminHandler(mappingService)
	return h, db, conn.ID, server.Close
}

type fixtureTransport http.HandlerFunc

func (serve fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	serve(w, req)
	return w.Result(), nil
}

func TestBatchImportNewzoeCreatesThreeMappedProducts(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, constants.ConnectionProtocolNewzoe, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/products" || r.Header.Get("X-Api-Signature") == "" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"ok":true,"data":{"products":[
			{"id":21,"name":"Muse 1B","price":"5.00","stock":12,"max_quantity":4,"type":"automatic"},
			{"id":22,"name":"Muse 31B","price":"8.34","stock":8,"max_quantity":2,"type":"automatic"},
			{"id":24,"name":"Duolingo Super annual","price":"6.03","stock":5,"max_quantity":1,"type":"automatic"}
		]}}`)
	})
	defer cleanup()

	category := categorydomain.Category{Slug: "provider-other", NameJSON: jsonmap.JSON{"zh-CN": "其他"}, IsActive: true}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"connection_id":%d,"category_id":%d,"upstream_product_ids":[24,21,22]}`, connID, category.ID)
	for _, expectedNew := range []int{3, 0} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.BatchImportUpstreamProducts(c)
		var result struct {
			Data struct {
				SuccessCount int `json:"success_count"`
				Total        int
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Data.Total != 3 || result.Data.SuccessCount != expectedNew {
			t.Fatalf("expected %d new imports: status %d body %s", expectedNew, w.Code, w.Body)
		}
	}
	for _, item := range []struct {
		id    uint
		limit int
	}{{21, 4}, {22, 2}, {24, 1}} {
		var m mappingdomain.Mapping
		if err := db.Where("connection_id = ? AND upstream_product_id = ?", connID, item.id).First(&m).Error; err != nil {
			t.Fatal(err)
		}
		var product productdomain.Product
		if err := db.First(&product, m.LocalProductID).Error; err != nil {
			t.Fatal(err)
		}
		if !m.IsActive || m.UpstreamFulfillmentType != constants.FulfillmentTypeAuto || product.FulfillmentType != constants.FulfillmentTypeUpstream || product.IsActive || !product.IsMapped || product.CategoryID != category.ID || product.MaxPurchaseQuantity != item.limit {
			t.Errorf("incorrect mapped product: upstream %d, fulfillment %s/%s, mapped %v, published %v, category %d, limit %d", item.id, m.UpstreamFulfillmentType, product.FulfillmentType, product.IsMapped, product.IsActive, product.CategoryID, product.MaxPurchaseQuantity)
		}
		var sku mappingdomain.SKUMapping
		if err := db.Where("product_mapping_id = ?", m.ID).First(&sku).Error; err != nil || sku.UpstreamSKUID != item.id || sku.UpstreamStock <= 0 {
			t.Errorf("missing purchasable SKU for product %d: %+v, %v", item.id, sku, err)
		}
	}
}

func TestBatchImportUpstreamProductsAutoCreatesCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, constants.ConnectionProtocolDujiaoNext, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					Description:     jsonmap.JSON{"zh-CN": "描述"},
					Content:         jsonmap.JSON{"zh-CN": "内容"},
					Images:          []string{},
					Tags:            []string{},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID == 0 {
		t.Fatalf("expected imported product to be assigned to auto-created category")
	}

	var category categorydomain.Category
	if err := db.First(&category, imported.CategoryID).Error; err != nil {
		t.Fatalf("load auto-created category failed: %v", err)
	}
	if category.Slug != "upstream-streaming" {
		t.Fatalf("expected auto-created category slug upstream-streaming, got %q", category.Slug)
	}
}

func TestBatchImportUpstreamProductsRestoresSoftDeletedAutoCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, constants.ConnectionProtocolDujiaoNext, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	deletedCategory := categorydomain.Category{
		Slug:     "upstream-streaming",
		NameJSON: jsonmap.JSON{"zh-CN": "已删除分类"},
		IsActive: true,
	}
	if err := db.Create(&deletedCategory).Error; err != nil {
		t.Fatalf("create soft-delete target category failed: %v", err)
	}
	if err := categorygormstore.NewCategoryStore(db).Delete(fmt.Sprintf("%d", deletedCategory.ID)); err != nil {
		t.Fatalf("soft delete category failed: %v", err)
	}

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID != deletedCategory.ID {
		t.Fatalf("expected imported product category %d, got %d", deletedCategory.ID, imported.CategoryID)
	}

	var restored categorydomain.Category
	if err := db.First(&restored, deletedCategory.ID).Error; err != nil {
		t.Fatalf("expected category to be restored, got %v", err)
	}
	if restored.NameJSON["zh-CN"] != "流媒体" {
		t.Fatalf("expected restored category name to be refreshed, got %+v", restored.NameJSON)
	}
}

type readonlyQuoteAdapter struct {
	upstream.Adapter
	price      string
	stock      int
	reads      int
	observedAt time.Time
}

func (a *readonlyQuoteAdapter) GetProduct(_ context.Context, id uint) (*upstream.UpstreamProduct, error) {
	a.reads++
	a.observedAt = time.Now().UTC()
	native := map[uint]string{1: "6aeedc6cadf360530f5faf80", 2: "6a4b4d7d1827db4ca39bb8be"}[id]
	return &upstream.UpstreamProduct{ID: id, Currency: "USD", IsActive: true, SKUs: []upstream.UpstreamSKU{{ID: id, SKUCode: native, PriceAmount: a.price, StockQuantity: a.stock, IsActive: true}}}, nil
}

func readonlyBindingFixture(t *testing.T) (*mappingapp.Service, *gorm.DB, *readonlyQuoteAdapter) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:readonly_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&categorydomain.Category{}, &productdomain.Product{}, &productdomain.ProductSKU{}, &siteconnectiondomain.Connection{}, &mappingdomain.Mapping{}, &mappingdomain.SKUMapping{}, &mappingdomain.ReadOnlyBinding{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&categorydomain.Category{ID: 1, Slug: "test", NameJSON: jsonmap.JSON{"zh-CN": "测试"}},
		&siteconnectiondomain.Connection{ID: 1, Protocol: constants.ConnectionProtocolFangfang},
		&siteconnectiondomain.Connection{ID: 2, Protocol: constants.ConnectionProtocolNewzoe},
		&productdomain.Product{ID: 37, Slug: "upstream-1-22-1793713309179", CategoryID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, FulfillmentType: constants.FulfillmentTypeUpstream, IsMapped: true, IsActive: true},
		&productdomain.Product{ID: 41, Slug: "gpt-plus-account-preorder", CategoryID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, FulfillmentType: constants.FulfillmentTypeManual, IsActive: true},
		&productdomain.Product{ID: 49, Slug: "muse-invitation-cdk", CategoryID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, FulfillmentType: constants.FulfillmentTypeManual},
		&productdomain.Product{ID: 50, Slug: "muse-31b-first-login", CategoryID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, FulfillmentType: constants.FulfillmentTypeManual, IsActive: true},
		&productdomain.Product{ID: 51, Slug: "gmail-aged-2010-2022", CategoryID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, FulfillmentType: constants.FulfillmentTypeManual, IsActive: true},
		&productdomain.ProductSKU{ID: 70, ProductID: 37, SKUCode: "MUSE31B", PriceAmount: money.FromDecimal(decimal.NewFromInt(26)), IsActive: true},
		&productdomain.ProductSKU{ID: 74, ProductID: 41, SKUCode: "gpt-plus-account-preorder-1m", PriceAmount: money.FromDecimal(decimal.NewFromInt(108)), ManualStockTotal: 6, IsActive: true},
		&productdomain.ProductSKU{ID: 84, ProductID: 49, SKUCode: "CDK"},
		&productdomain.ProductSKU{ID: 85, ProductID: 50, SKUCode: "fang-fang-muse-31b-default", PriceAmount: money.FromDecimal(decimal.NewFromInt(26)), ManualStockTotal: 10, IsActive: true},
		&productdomain.ProductSKU{ID: 86, ProductID: 51, SKUCode: "fang-fang-gmail-2010-2022-default", PriceAmount: money.FromDecimal(decimal.NewFromInt(40)), ManualStockTotal: 10, IsActive: true},
		&mappingdomain.Mapping{ID: 2, ConnectionID: 2, LocalProductID: 37, UpstreamProductID: 22, IsActive: true},
		&mappingdomain.SKUMapping{ID: 2, ProductMappingID: 2, LocalSKUID: 70, UpstreamSKUID: 22, UpstreamStock: 0},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	adapter := &readonlyQuoteAdapter{price: "0.96", stock: 3}
	service, err := mappingapp.NewService(mappingapp.Options{
		Products: productgormstore.NewProductStore(db), SKUs: productgormstore.NewSKUStore(db),
		ReadOnlyBindings: mappinggormstore.NewReadOnlyBindingStore(db),
		Connections:      &mappingConnectionProvider{db: db, adapter: adapter}, Media: adminNoopLocalMediaRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, adapter
}
func snapshotReadOnlyProtected(t *testing.T, db *gorm.DB) string {
	t.Helper()
	state := map[string]any{}
	for _, table := range []string{"products", "product_skus", "product_mappings", "sku_mappings"} {
		var rows []map[string]interface{}
		if err := db.Table(table).Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		state[table] = rows
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestReadOnlyBindingsPreserveExistingFulfillment(t *testing.T) {
	service, db, adapter := readonlyBindingFixture(t)
	before := snapshotReadOnlyProtected(t, db)
	muse := mappingapp.ReadOnlyBindingInput{ConnectionID: 1, LocalProductID: 50, LocalSKUID: 85, UpstreamProductID: 1}
	beforeRequest := time.Now().UTC()
	first, err := service.BindReadOnlySupplier(context.Background(), muse)
	if err != nil {
		t.Fatal(err)
	}
	if first.PurchaseEnabled || first.UpstreamPrice != "0.96" || first.UpstreamStock != 3 || first.UpstreamSKUCode != "6aeedc6cadf360530f5faf80" {
		t.Fatalf("incorrect quote: %+v", first)
	}
	if first.QuotedAt.Before(beforeRequest) || first.QuotedAt.After(adapter.observedAt) {
		t.Fatal("quote time must be the start of the supplier request")
	}
	adapter.price, adapter.stock = "1.02", 8
	again, err := service.RefreshReadOnlyBinding(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.UpstreamPrice != "1.02" || again.UpstreamStock != 8 || again.PurchaseEnabled {
		t.Fatalf("incorrect refresh: %+v", again)
	}
	plus, err := service.BindReadOnlySupplier(context.Background(), mappingapp.ReadOnlyBindingInput{ConnectionID: 1, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 2})
	if err != nil || plus.PurchaseEnabled {
		t.Fatalf("plus bind: %+v %v", plus, err)
	}
	rows, err := service.ListReadOnlyBindings(1)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list: %+v %v", rows, err)
	}
	if got := snapshotReadOnlyProtected(t, db); got != before {
		t.Fatal("binding or refresh modified existing product, SKU, procurement mapping, or stock")
	}
	if adapter.reads != 3 {
		t.Fatal("unexpected upstream operations")
	}
}
func TestReadOnlyBindingsRejectWrongProductAndProtocol(t *testing.T) {
	service, db, adapter := readonlyBindingFixture(t)
	for _, input := range []mappingapp.ReadOnlyBindingInput{
		{ConnectionID: 1, LocalProductID: 49, LocalSKUID: 84, UpstreamProductID: 1},
		{ConnectionID: 1, LocalProductID: 50, LocalSKUID: 74, UpstreamProductID: 1},
		{ConnectionID: 1, LocalProductID: 37, LocalSKUID: 70, UpstreamProductID: 1},
		{ConnectionID: 1, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 1},
		{ConnectionID: 2, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 2},
		{ConnectionID: 1, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 3},
	} {
		if _, err := service.BindReadOnlySupplier(context.Background(), input); !errors.Is(err, mappingcontract.ErrReadOnlyBindingInvalid) {
			t.Fatalf("unexpected result: %v", err)
		}
	}
	var count int64
	db.Model(&mappingdomain.ReadOnlyBinding{}).Count(&count)
	if count != 0 || adapter.reads != 0 {
		t.Fatal("invalid binding performed supplier read or write")
	}
}
func TestReadOnlyBindingAdminRoutes(t *testing.T) {
	service, _, _ := readonlyBindingFixture(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mappinghttp.RegisterAdminRoutes(router, mappinghttp.NewAdminHandler(service))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/supplier-readonly-bindings", strings.NewReader(`{"connection_id":1,"local_product_id":51,"local_sku_id":86,"upstream_product_id":2,"purchase_enabled":true}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"purchase_enabled":false`) {
		t.Fatalf("bind %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/supplier-readonly-bindings?connection_id=1", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"local_sku_id":86`) {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/supplier-readonly-bindings/1/refresh", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"purchase_enabled":false`) {
		t.Fatalf("refresh %d %s", w.Code, w.Body.String())
	}
}

func TestReadOnlyBindingCannotRetargetExistingAssociation(t *testing.T) {
	service, db, _ := readonlyBindingFixture(t)
	input := mappingapp.ReadOnlyBindingInput{ConnectionID: 1, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 2}
	original, err := service.BindReadOnlySupplier(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&productdomain.ProductSKU{ID: 87, ProductID: 51, SKUCode: "another-sku"}).Error; err != nil {
		t.Fatal(err)
	}
	input.LocalSKUID = 87
	if _, err := service.BindReadOnlySupplier(context.Background(), input); !errors.Is(err, mappingcontract.ErrReadOnlyBindingInvalid) {
		t.Fatalf("off-pin retarget not refused: %v", err)
	}
	rows, err := service.ListReadOnlyBindings(1)
	if err != nil || len(rows) != 1 || rows[0].ID != original.ID || rows[0].LocalSKUID != 86 {
		t.Fatalf("association changed: %+v %v", rows, err)
	}
}
