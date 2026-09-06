package gormstore

import (
	"fmt"
	"math"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	"github.com/dujiao-next/internal/constants"
	dashboard "github.com/dujiao-next/internal/modules/dashboard/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

func TestGetProfitOverviewDeductsRefundRecords(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	now := time.Now().UTC().Truncate(time.Second)

	category := createDashboardCategory(t, db, "dashboard-profit-refund-category")
	product := &productdomain.Product{
		CategoryID:      category.ID,
		Slug:            "dashboard-profit-refund-product",
		TitleJSON:       jsonmap.JSON{"zh-CN": "利润测试商品"},
		PriceAmount:     money.FromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	manualRefundedOrder := createDashboardProfitOrderWithItem(t, db, product, "DJ-PROFIT-MANUAL", constants.OrderStatusRefunded, 100, 40, "利润测试商品", now)
	walletRefundedOrder := createDashboardProfitOrderWithItem(t, db, product, "DJ-PROFIT-WALLET", constants.OrderStatusPartiallyRefunded, 120, 50, "利润测试商品", now)

	records := []orderdomain.OrderRefundRecord{
		{
			UserID:    1,
			OrderID:   manualRefundedOrder.ID,
			Type:      constants.OrderRefundTypeManual,
			Amount:    money.FromDecimal(decimal.NewFromInt(100)),
			Currency:  "CNY",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			UserID:    1,
			OrderID:   walletRefundedOrder.ID,
			Type:      constants.OrderRefundTypeWallet,
			Amount:    money.FromDecimal(decimal.NewFromInt(20)),
			Currency:  "CNY",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			UserID:    1,
			OrderID:   walletRefundedOrder.ID,
			Type:      constants.OrderRefundTypeManual,
			Amount:    money.FromDecimal(decimal.NewFromInt(10)),
			Currency:  "CNY",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	for idx := range records {
		if err := db.Create(&records[idx]).Error; err != nil {
			t.Fatalf("create refund record failed: %v", err)
		}
	}

	result, err := repo.GetProfitOverview(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("get profit overview failed: %v", err)
	}
	if math.Abs(result.TotalRevenue-90) > 0.000001 {
		t.Fatalf("total revenue want 90 got %.2f", result.TotalRevenue)
	}
	if math.Abs(result.TotalCost-90) > 0.000001 {
		t.Fatalf("total cost want 90 got %.2f", result.TotalCost)
	}
}

func TestGetProfitTrendsDeductsRefundRecords(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	category := createDashboardCategory(t, db, "dashboard-profit-trend-refund-category")
	product := &productdomain.Product{
		CategoryID:      category.ID,
		Slug:            "dashboard-profit-trend-refund-product",
		TitleJSON:       jsonmap.JSON{"zh-CN": "利润趋势测试商品"},
		PriceAmount:     money.FromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	day1Order := createDashboardProfitOrderWithItem(t, db, product, "DJ-PROFIT-TREND-DAY1", constants.OrderStatusRefunded, 80, 30, "利润趋势测试商品", base)
	day2Order := createDashboardProfitOrderWithItem(t, db, product, "DJ-PROFIT-TREND-DAY2", constants.OrderStatusRefunded, 100, 40, "利润趋势测试商品", base.Add(24*time.Hour))

	records := []orderdomain.OrderRefundRecord{
		{
			UserID:    1,
			OrderID:   day1Order.ID,
			Type:      constants.OrderRefundTypeManual,
			Amount:    money.FromDecimal(decimal.NewFromInt(80)),
			Currency:  "CNY",
			CreatedAt: base,
			UpdatedAt: base,
		},
		{
			UserID:    1,
			OrderID:   day2Order.ID,
			Type:      constants.OrderRefundTypeWallet,
			Amount:    money.FromDecimal(decimal.NewFromInt(30)),
			Currency:  "CNY",
			CreatedAt: base.Add(24 * time.Hour),
			UpdatedAt: base.Add(24 * time.Hour),
		},
		{
			UserID:    1,
			OrderID:   day2Order.ID,
			Type:      constants.OrderRefundTypeManual,
			Amount:    money.FromDecimal(decimal.NewFromInt(10)),
			Currency:  "CNY",
			CreatedAt: base.Add(24 * time.Hour),
			UpdatedAt: base.Add(24 * time.Hour),
		},
	}
	for idx := range records {
		if err := db.Create(&records[idx]).Error; err != nil {
			t.Fatalf("create refund record failed: %v", err)
		}
	}

	rows, err := repo.GetProfitTrends(base.Add(-time.Hour), base.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("get profit trends failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("profit trend rows want 2 got %d", len(rows))
	}
	rowMap := make(map[string]dashboard.ProfitTrendRow, len(rows))
	for _, row := range rows {
		rowMap[row.Day] = row
	}
	day1 := "2026-03-01"
	day2 := "2026-03-02"
	if math.Abs(rowMap[day1].Revenue-0) > 0.000001 || math.Abs(rowMap[day1].Cost-30) > 0.000001 {
		t.Fatalf("unexpected day1 row: %+v", rowMap[day1])
	}
	if math.Abs(rowMap[day2].Revenue-60) > 0.000001 || math.Abs(rowMap[day2].Cost-40) > 0.000001 {
		t.Fatalf("unexpected day2 row: %+v", rowMap[day2])
	}
}

func TestGetProfitOverviewDeductsInWindowRefundForOutOfWindowOrder(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	startAt := now.AddDate(0, 0, -7)
	endAt := now.Add(time.Hour)

	category := createDashboardCategory(t, db, "dashboard-profit-period-refund-category")
	product := &productdomain.Product{
		CategoryID:      category.ID,
		Slug:            "dashboard-profit-period-refund-product",
		TitleJSON:       jsonmap.JSON{"zh-CN": "周期退款测试商品"},
		PriceAmount:     money.FromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	outsideOrder := &orderdomain.Order{
		OrderNo:        "DJ-PROFIT-OUTSIDE-ORDER",
		UserID:         1,
		Status:         constants.OrderStatusRefunded,
		Currency:       "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:    money.FromDecimal(decimal.NewFromInt(100)),
		CreatedAt:      startAt.Add(-24 * time.Hour),
		UpdatedAt:      startAt.Add(-24 * time.Hour),
	}
	if err := db.Create(outsideOrder).Error; err != nil {
		t.Fatalf("create outside order failed: %v", err)
	}
	if err := db.Create(&orderdomain.OrderItem{
		OrderID:         outsideOrder.ID,
		ProductID:       product.ID,
		TitleJSON:       jsonmap.JSON{"zh-CN": "周期退款测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(100)),
		CostPrice:       money.FromDecimal(decimal.NewFromInt(40)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(100)),
		CouponDiscount:  money.FromDecimal(decimal.Zero),
		FulfillmentType: constants.FulfillmentTypeManual,
		CreatedAt:       startAt.Add(-24 * time.Hour),
		UpdatedAt:       startAt.Add(-24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("create outside order item failed: %v", err)
	}

	inWindowOrder := &orderdomain.Order{
		OrderNo:        "DJ-PROFIT-IN-WINDOW",
		UserID:         1,
		Status:         constants.OrderStatusCompleted,
		Currency:       "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(60)),
		DiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:    money.FromDecimal(decimal.NewFromInt(60)),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(inWindowOrder).Error; err != nil {
		t.Fatalf("create in-window order failed: %v", err)
	}
	if err := db.Create(&orderdomain.OrderItem{
		OrderID:         inWindowOrder.ID,
		ProductID:       product.ID,
		TitleJSON:       jsonmap.JSON{"zh-CN": "周期退款测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(60)),
		CostPrice:       money.FromDecimal(decimal.NewFromInt(20)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(60)),
		CouponDiscount:  money.FromDecimal(decimal.Zero),
		FulfillmentType: constants.FulfillmentTypeManual,
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error; err != nil {
		t.Fatalf("create in-window order item failed: %v", err)
	}

	if err := db.Create(&orderdomain.OrderRefundRecord{
		UserID:    1,
		OrderID:   outsideOrder.ID,
		Type:      constants.OrderRefundTypeManual,
		Amount:    money.FromDecimal(decimal.NewFromInt(50)),
		Currency:  "CNY",
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}

	result, err := repo.GetProfitOverview(startAt, endAt)
	if err != nil {
		t.Fatalf("get profit overview failed: %v", err)
	}
	if math.Abs(result.TotalRevenue-10) > 0.000001 {
		t.Fatalf("total revenue want 10 got %.2f", result.TotalRevenue)
	}
	if math.Abs(result.TotalCost-20) > 0.000001 {
		t.Fatalf("total cost want 20 got %.2f", result.TotalCost)
	}
}

func TestGetProfitTrendsIncludesRefundOnlyDayInWindow(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	startAt := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	endAt := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	day1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)

	category := createDashboardCategory(t, db, "dashboard-profit-refund-only-day-category")
	product := &productdomain.Product{
		CategoryID:      category.ID,
		Slug:            "dashboard-profit-refund-only-day-product",
		TitleJSON:       jsonmap.JSON{"zh-CN": "退款单日测试商品"},
		PriceAmount:     money.FromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	inWindowOrder := &orderdomain.Order{
		OrderNo:        "DJ-PROFIT-TREND-IN-WINDOW",
		UserID:         1,
		Status:         constants.OrderStatusCompleted,
		Currency:       "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(80)),
		DiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:    money.FromDecimal(decimal.NewFromInt(80)),
		CreatedAt:      day1,
		UpdatedAt:      day1,
	}
	if err := db.Create(inWindowOrder).Error; err != nil {
		t.Fatalf("create in-window order failed: %v", err)
	}
	if err := db.Create(&orderdomain.OrderItem{
		OrderID:         inWindowOrder.ID,
		ProductID:       product.ID,
		TitleJSON:       jsonmap.JSON{"zh-CN": "退款单日测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(80)),
		CostPrice:       money.FromDecimal(decimal.NewFromInt(30)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(80)),
		CouponDiscount:  money.FromDecimal(decimal.Zero),
		FulfillmentType: constants.FulfillmentTypeManual,
		CreatedAt:       day1,
		UpdatedAt:       day1,
	}).Error; err != nil {
		t.Fatalf("create in-window order item failed: %v", err)
	}

	outsideOrder := &orderdomain.Order{
		OrderNo:        "DJ-PROFIT-TREND-OUTSIDE-ORDER",
		UserID:         1,
		Status:         constants.OrderStatusRefunded,
		Currency:       "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:    money.FromDecimal(decimal.NewFromInt(100)),
		CreatedAt:      startAt.Add(-48 * time.Hour),
		UpdatedAt:      startAt.Add(-48 * time.Hour),
	}
	if err := db.Create(outsideOrder).Error; err != nil {
		t.Fatalf("create outside order failed: %v", err)
	}
	if err := db.Create(&orderdomain.OrderItem{
		OrderID:         outsideOrder.ID,
		ProductID:       product.ID,
		TitleJSON:       jsonmap.JSON{"zh-CN": "退款单日测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(100)),
		CostPrice:       money.FromDecimal(decimal.NewFromInt(40)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(100)),
		CouponDiscount:  money.FromDecimal(decimal.Zero),
		FulfillmentType: constants.FulfillmentTypeManual,
		CreatedAt:       startAt.Add(-48 * time.Hour),
		UpdatedAt:       startAt.Add(-48 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("create outside order item failed: %v", err)
	}

	if err := db.Create(&orderdomain.OrderRefundRecord{
		UserID:    1,
		OrderID:   outsideOrder.ID,
		Type:      constants.OrderRefundTypeManual,
		Amount:    money.FromDecimal(decimal.NewFromInt(30)),
		Currency:  "CNY",
		CreatedAt: day2,
		UpdatedAt: day2,
	}).Error; err != nil {
		t.Fatalf("create refund record failed: %v", err)
	}

	rows, err := repo.GetProfitTrends(startAt, endAt)
	if err != nil {
		t.Fatalf("get profit trends failed: %v", err)
	}
	rowMap := make(map[string]dashboard.ProfitTrendRow, len(rows))
	for _, row := range rows {
		rowMap[row.Day] = row
	}
	if math.Abs(rowMap["2026-03-01"].Revenue-80) > 0.000001 || math.Abs(rowMap["2026-03-01"].Cost-30) > 0.000001 {
		t.Fatalf("unexpected 2026-03-01 row: %+v", rowMap["2026-03-01"])
	}
	if math.Abs(rowMap["2026-03-02"].Revenue-(-30)) > 0.000001 || math.Abs(rowMap["2026-03-02"].Cost-0) > 0.000001 {
		t.Fatalf("unexpected 2026-03-02 row: %+v", rowMap["2026-03-02"])
	}
}

func TestProfitCostCoveragePreservesRevenue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		costs    []int64
		wantCost float64
	}{
		{"all_missing", []int64{0, 0}, 0},
		{"mixed", []int64{40, 0}, 40},
		{"complete", []int64{40, 30}, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, db := setupDashboardRepositoryTest(t)
			now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
			category := createDashboardCategory(t, db, "cost-coverage-category")
			product := &productdomain.Product{CategoryID: category.ID, Slug: "cost-coverage", TitleJSON: jsonmap.JSON{"zh-CN": "成本测试"}, PriceAmount: money.FromDecimal(decimal.NewFromInt(100)), PurchaseType: constants.ProductPurchaseMember, FulfillmentType: constants.FulfillmentTypeManual, IsActive: true}
			if err := db.Create(product).Error; err != nil {
				t.Fatal(err)
			}
			for i, cost := range tc.costs {
				createDashboardProfitOrderWithItem(t, db, product, fmt.Sprintf("COST-%d", i), constants.OrderStatusCompleted, 100, cost, "成本测试", now)
			}
			var missing int64
			for _, cost := range tc.costs {
				if cost <= 0 {
					missing++
				}
			}
			start, end := now.Add(-time.Hour), now.Add(time.Hour)
			overview, err := repo.GetProfitOverview(start, end)
			if err != nil {
				t.Fatal(err)
			}
			if overview.TotalRevenue != 200 || overview.TotalCost != tc.wantCost || overview.MissingCostItems != missing {
				t.Errorf("overview dropped revenue or changed cost: %+v", overview)
			}
			trends, err := repo.GetProfitTrends(start, end)
			if err != nil {
				t.Fatal(err)
			}
			if len(trends) != 1 || trends[0].Revenue != 200 || trends[0].Cost != tc.wantCost || trends[0].MissingCostItems != missing {
				t.Errorf("trend dropped revenue or changed cost: %+v", trends)
			}
			products, err := repo.GetTopProducts(start, end, 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(products) != 1 || products[0].PaidAmount != 200 || products[0].TotalCost != tc.wantCost || products[0].PaidOrders != 2 || products[0].MissingCostItems != missing {
				t.Errorf("ranking dropped sales data: %+v", products)
			}
		})
	}
}
