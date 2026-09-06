package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dashboardcontract "github.com/dujiao-next/internal/modules/dashboard/contract"
	reportingdomain "github.com/dujiao-next/internal/modules/reporting/domain"
)

type dashboardServiceRepoStub struct {
	profitOverview dashboardcontract.ProfitOverviewRow
	profitTrends   []dashboardcontract.ProfitTrendRow
	products       []dashboardcontract.ProductRankingRow
	overview       dashboardcontract.OverviewRow
	stock          dashboardcontract.StockStatsRow
}

func (s dashboardServiceRepoStub) GetOverview(startAt, endAt time.Time) (dashboardcontract.OverviewRow, error) {
	return s.overview, nil
}

func (s dashboardServiceRepoStub) GetPaymentOrderAlertCounts(startAt, endAt time.Time) (dashboardcontract.PaymentOrderAlertCountsRow, error) {
	return dashboardcontract.PaymentOrderAlertCountsRow{}, nil
}

func (s dashboardServiceRepoStub) GetOrderTrends(startAt, endAt time.Time) ([]dashboardcontract.OrderTrendRow, error) {
	return []dashboardcontract.OrderTrendRow{}, nil
}

func (s dashboardServiceRepoStub) GetPaymentTrends(startAt, endAt time.Time) ([]dashboardcontract.PaymentTrendRow, error) {
	return []dashboardcontract.PaymentTrendRow{}, nil
}

func (s dashboardServiceRepoStub) GetStockStats(lowStockThreshold int64) (dashboardcontract.StockStatsRow, error) {
	return s.stock, nil
}

func (s dashboardServiceRepoStub) GetInventoryAlertItems(lowStockThreshold int64) ([]dashboardcontract.InventoryAlertRow, error) {
	return []dashboardcontract.InventoryAlertRow{}, nil
}

func (s dashboardServiceRepoStub) GetTopProducts(startAt, endAt time.Time, limit int) ([]dashboardcontract.ProductRankingRow, error) {
	return s.products, nil
}

func (s dashboardServiceRepoStub) GetProfitOverview(startAt, endAt time.Time) (dashboardcontract.ProfitOverviewRow, error) {
	return s.profitOverview, nil
}

func (s dashboardServiceRepoStub) GetProfitTrends(startAt, endAt time.Time) ([]dashboardcontract.ProfitTrendRow, error) {
	return s.profitTrends, nil
}

func (s dashboardServiceRepoStub) GetTopChannels(startAt, endAt time.Time, limit int) ([]dashboardcontract.ChannelRankingRow, error) {
	return []dashboardcontract.ChannelRankingRow{}, nil
}

func (s dashboardServiceRepoStub) GetTotalUserBalance() (float64, error) {
	return 0, nil
}

func TestDashboardOverviewUsesPaidOrdersForPaymentConversionRate(t *testing.T) {
	service := NewService(dashboardServiceRepoStub{
		overview: dashboardcontract.OverviewRow{
			OrdersTotal:     10,
			PaidOrders:      6,
			CompletedOrders: 3,
			PaymentsTotal:   5,
			PaymentsSuccess: 4,
			Currency:        "cny",
			GMVPaid:         120,
		},
		stock: dashboardcontract.StockStatsRow{},
	}, nil)

	response, err := service.GetOverview(context.Background(), reportingdomain.Query{
		Range:    "today",
		Timezone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatalf("get overview failed: %v", err)
	}
	if response.Currency != "CNY" {
		t.Fatalf("currency want CNY got %s", response.Currency)
	}
	if response.Funnel.PaymentConversionRate != "60.00" {
		t.Fatalf("payment conversion rate want 60.00 got %s", response.Funnel.PaymentConversionRate)
	}
	if response.KPI.PaymentSuccessRate != "80.00" {
		t.Fatalf("payment success rate want 80.00 got %s", response.KPI.PaymentSuccessRate)
	}
}

func TestDashboardOverviewBuildsInventoryAlertsFromStockStats(t *testing.T) {
	service := NewService(dashboardServiceRepoStub{
		overview: dashboardcontract.OverviewRow{
			PendingPaymentOrders: 25,
			PaymentsFailed:       12,
		},
		stock: dashboardcontract.StockStatsRow{
			OutOfStockProducts: 2,
			LowStockProducts:   1,
		},
	}, nil)

	response, err := service.GetOverview(context.Background(), reportingdomain.Query{
		Range:    "today",
		Timezone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatalf("get overview failed: %v", err)
	}
	if len(response.Alerts) != 4 {
		t.Fatalf("alerts len want 4 got %d", len(response.Alerts))
	}
	if response.Alerts[0].Type != "out_of_stock_products" || response.Alerts[0].Value != 2 {
		t.Fatalf("unexpected first alert: %+v", response.Alerts[0])
	}
}

var _ dashboardcontract.Repository = dashboardServiceRepoStub{}

func TestDashboardProfitRequiresCompleteCosts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing int64
		cost    float64
		want    string
	}{
		{"all_missing", 2, 0, ""},
		{"mixed", 1, 40, ""},
		{"complete", 0, 70, "130.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			day := time.Now().UTC().Format("2006-01-02")
			service := NewService(dashboardServiceRepoStub{
				overview:       dashboardcontract.OverviewRow{PaidOrders: 2, GMVPaid: 200},
				profitOverview: dashboardcontract.ProfitOverviewRow{TotalRevenue: 200, TotalCost: tc.cost, MissingCostItems: tc.missing},
				profitTrends:   []dashboardcontract.ProfitTrendRow{{Day: day, Revenue: 200, Cost: tc.cost, MissingCostItems: tc.missing}},
				products:       []dashboardcontract.ProductRankingRow{{ProductID: 1, PaidOrders: 2, PaidAmount: 200, TotalCost: tc.cost, MissingCostItems: tc.missing}},
			}, nil)
			query := reportingdomain.Query{Range: "today", Timezone: "UTC"}
			overview, err := service.GetOverview(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			trends, err := service.GetTrends(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			rankings, err := service.GetRankings(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			if overview.KPI.GMVPaid != "200.00" || overview.KPI.PaidOrders != 2 || rankings.TopProducts[0].PaidAmount != "200.00" || rankings.TopProducts[0].PaidOrders != 2 {
				t.Fatal("sales data was lost")
			}
			for label, profit := range map[string]*string{"overview": overview.KPI.TotalProfit, "trend": trends.Points[0].Profit, "ranking": rankings.TopProducts[0].Profit} {
				if tc.missing > 0 {
					if profit != nil {
						t.Errorf("%s profit should be unavailable, got %s", label, *profit)
					}
				} else if profit == nil || *profit != tc.want {
					t.Errorf("%s profit want %s got %v", label, tc.want, profit)
				}
			}
			if overview.KPI.MissingCostItems != tc.missing || trends.Points[0].MissingCostItems != tc.missing || rankings.TopProducts[0].MissingCostItems != tc.missing {
				t.Fatal("cost coverage was lost")
			}
			if tc.missing > 0 {
				if overview.KPI.ProfitMargin != nil {
					t.Fatal("incomplete profit margin must be null")
				}
				encoded, err := json.Marshal(overview.KPI)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]interface{}
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatal(err)
				}
				value, exists := payload["total_profit"]
				if !exists || value != nil {
					t.Fatalf("API must explicitly return null: %s", encoded)
				}
			} else if overview.KPI.ProfitMargin == nil || *overview.KPI.ProfitMargin != "65.00" {
				t.Fatal("complete margin should be 65.00")
			}
		})
	}
}
