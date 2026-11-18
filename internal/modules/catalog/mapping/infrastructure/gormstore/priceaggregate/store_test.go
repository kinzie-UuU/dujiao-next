package priceaggregate

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	connectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func fixture(t *testing.T) (*Store, Input) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:aggregate_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&productdomain.Product{}, &productdomain.ProductSKU{}, &connectiondomain.Connection{}, &mappingdomain.Mapping{}, &mappingdomain.SKUMapping{}, &mappingdomain.ReadOnlyBinding{}, &FXQuote{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	amount := func(v string) money.Amount { return money.FromDecimal(decimal.RequireFromString(v)) }
	rows := []any{
		&productdomain.Product{ID: 1, Slug: "test", TitleJSON: jsonmap.JSON{"zh-CN": "test"}, PriceAmount: amount("10"), IsActive: true},
		&productdomain.ProductSKU{ID: 2, ProductID: 1, SKUCode: "test", PriceAmount: amount("10"), IsActive: true},
		&connectiondomain.Connection{ID: 3, Protocol: "newzoe", Status: "active", ExchangeRate: decimal.NewFromInt(1), PriceMarkupPercent: decimal.NewFromInt(180), PriceRoundingMode: "ceil_int"},
		&connectiondomain.Connection{ID: 7, Protocol: "fangfang-readonly", Status: "active", BaseURL: "https://canboso.com", ExchangeRate: decimal.NewFromInt(1)},
		&mappingdomain.Mapping{ID: 4, ConnectionID: 3, LocalProductID: 1, UpstreamProductID: 22, IsActive: true},
		&mappingdomain.SKUMapping{ID: 5, ProductMappingID: 4, LocalSKUID: 2, UpstreamSKUID: 22, UpstreamPrice: amount("7.26"), UpstreamStock: 2, UpstreamIsActive: true, StockSyncedAt: &now},
		&mappingdomain.ReadOnlyBinding{ID: 6, ConnectionID: 7, LocalProductID: 1, LocalSKUID: 2, UpstreamProductID: 1, UpstreamSKUCode: "supplier", UpstreamPrice: "0.96", Currency: "USD", UpstreamStock: 2, QuotedAt: now},
		&FXQuote{ID: 8, Version: 1, Base: "USD", Quote: "CNY", Rate: "7", Source: "test-source", ObservedAt: now},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var in Input
	for _, item := range []struct {
		dst any
		id  uint
	}{{&in.Product, 1}, {&in.SKU, 2}, {&in.Connection, 3}, {&in.BackupConnection, 7}, {&in.Mapping, 4}, {&in.Primary, 5}, {&in.Backup, 6}, {&in.FX, 8}} {
		if err := db.First(item.dst, item.id).Error; err != nil {
			t.Fatal(err)
		}
	}
	in.BackupMarkupPercent = decimal.NewFromInt(150)
	in.MinimumPrice = decimal.NewFromInt(1)
	in.PrimaryMaxAge, in.BackupMaxAge, in.FXMaxAge = time.Hour, time.Hour, time.Hour
	s := New(db)
	s.now = func() time.Time { return now.Add(time.Minute) }
	return s, in
}

func readPrices(t *testing.T, s *Store) (string, string) {
	t.Helper()
	var p productdomain.Product
	var sku productdomain.ProductSKU
	if err := s.DB.First(&p, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&sku, 2).Error; err != nil {
		t.Fatal(err)
	}
	return p.PriceAmount.Decimal.StringFixed(2), sku.PriceAmount.Decimal.StringFixed(2)
}

func TestApplyAggregatePricesAtomically(t *testing.T) {
	s, in := fixture(t)
	price, err := s.Apply(in)
	if err != nil || price.StringFixed(2) != "21.00" {
		t.Fatalf("price=%s err=%v", price, err)
	}
	p, sku := readPrices(t, s)
	if p != "21.00" || sku != "21.00" {
		t.Fatalf("prices %s %s", p, sku)
	}
	if _, err := s.Apply(in); !errors.Is(err, ErrRecalculate) {
		t.Fatalf("stale write %v", err)
	}
}

func TestChangedQuoteFXOrPriceRequiresRecalculation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Store)
	}{
		{"primary quote", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&mappingdomain.SKUMapping{}).Where("id = 5").UpdateColumn("upstream_price", "8").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"backup quote", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&mappingdomain.ReadOnlyBinding{}).Where("id = 6").UpdateColumn("upstream_price", "2").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"exchange rate", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&FXQuote{}).Where("id = 8").UpdateColumn("version", 2).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"primary mapping identity", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&mappingdomain.Mapping{}).Where("id = 4").UpdateColumn("upstream_product_id", 23).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"primary SKU identity", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&mappingdomain.SKUMapping{}).Where("id = 5").UpdateColumn("upstream_sku_id", 23).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"product price", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&productdomain.Product{}).Where("id = 1").UpdateColumn("price_amount", "12").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"sku price", func(t *testing.T, s *Store) {
			if err := s.DB.Model(&productdomain.ProductSKU{}).Where("id = 2").UpdateColumn("price_amount", "12").Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, in := fixture(t)
			tc.mutate(t, s)
			if _, err := s.Apply(in); !errors.Is(err, ErrRecalculate) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestProductFailureRollsBackSKU(t *testing.T) {
	s, in := fixture(t)
	if err := s.DB.Callback().Update().Before("gorm:update").Register("mock:product_price_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "products" {
			if m, ok := tx.Statement.Dest.(map[string]interface{}); ok {
				if _, exists := m["price_amount"]; exists {
					tx.AddError(errors.New("synthetic write failure"))
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(in); err == nil {
		t.Fatal("write failure was hidden")
	}
	p, sku := readPrices(t, s)
	if p != "10.00" || sku != "10.00" {
		t.Fatalf("partial write %s %s", p, sku)
	}
}

func TestUnavailableBackupCannotLowerPrice(t *testing.T) {
	s, in := fixture(t)
	if err := s.DB.Model(&mappingdomain.ReadOnlyBinding{}).Where("id = 6").Update("upstream_stock", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&in.Backup, 6).Error; err != nil {
		t.Fatal(err)
	}
	// Current price exceeds the valid primary price; unavailable backup cannot
	// justify lowering it.
	if err := s.DB.Model(&productdomain.Product{}).Where("id = 1").Update("price_amount", "30").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&productdomain.ProductSKU{}).Where("id = 2").Update("price_amount", "30").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&in.Product, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&in.SKU, 2).Error; err != nil {
		t.Fatal(err)
	}
	price, err := s.Apply(in)
	if err != nil || price.StringFixed(2) != "30.00" {
		t.Fatalf("price=%s err=%v", price, err)
	}
}

func TestBackupPriceCanSetHigherAggregate(t *testing.T) {
	s, in := fixture(t)
	if err := s.DB.Model(&mappingdomain.ReadOnlyBinding{}).Where("id = 6").Update("upstream_price", "4").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&in.Backup, 6).Error; err != nil {
		t.Fatal(err)
	}
	price, err := s.Apply(in)
	if err != nil || price.StringFixed(2) != "70.00" {
		t.Fatalf("price=%s err=%v", price, err)
	}
	p, sku := readPrices(t, s)
	if p != "70.00" || sku != "70.00" {
		t.Fatalf("prices %s %s", p, sku)
	}
}

func TestStaleAndInconsistentInputsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *Store, *Input)
		want error
	}{
		{"stale primary", func(t *testing.T, s *Store, in *Input) {
			s.now = func() time.Time { return in.Primary.StockSyncedAt.Add(2 * time.Hour) }
		}, ErrInvalidSnapshot},
		{"disabled supplier", func(t *testing.T, s *Store, in *Input) {
			if err := s.DB.Model(&connectiondomain.Connection{}).Where("id = 7").Update("status", "disabled").Error; err != nil {
				t.Fatal(err)
			}
		}, ErrRecalculate},
		{"additional SKU", func(t *testing.T, s *Store, in *Input) {
			if err := s.DB.Create(&productdomain.ProductSKU{ID: 9, ProductID: 1, SKUCode: "other", IsActive: true}).Error; err != nil {
				t.Fatal(err)
			}
		}, ErrInvalidSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, in := fixture(t)
			tc.edit(t, s, &in)
			if _, err := s.Apply(in); !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestConcurrentSameSnapshotOnlyOneUpdates(t *testing.T) {
	s, in := fixture(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := s.Apply(in); errs <- err }()
	}
	close(start)
	wg.Wait()
	close(errs)
	var success, retry int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRecalculate) {
			retry++
		} else {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if success != 1 || retry != 1 {
		t.Fatalf("success=%d retry=%d", success, retry)
	}
	p, sku := readPrices(t, s)
	if p != "21.00" || sku != "21.00" {
		t.Fatalf("prices %s %s", p, sku)
	}
}
