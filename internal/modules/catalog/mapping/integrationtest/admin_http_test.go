package integrationtest

import (
	"context"
	"encoding/json"
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
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
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
