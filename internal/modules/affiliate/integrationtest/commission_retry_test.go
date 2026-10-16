package integrationtest

import (
	"testing"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	affiliategormstore "github.com/dujiao-next/internal/modules/affiliate/infrastructure/gormstore"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"

	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	"github.com/dujiao-next/internal/testkit/memorysettings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

type staticOrderReader struct{ order *orderdomain.Order }

func (r staticOrderReader) GetByID(uint) (*orderdomain.Order, error) { return r.order, nil }

type staticProductReader struct{}

func (staticProductReader) ListByIDs([]uint) ([]productdomain.Product, error) {
	return []productdomain.Product{{ID: 1, IsAffiliateEnabled: true}}, nil
}

// 佣金重试任务晚于退款执行时，退款流程已找不到佣金可冲销，重试不能再生成佣金。
func TestHandleOrderPaidSkipsRefundedOrder(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{constants.OrderStatusPaid, true},
		{constants.OrderStatusRefunded, false},
		{constants.OrderStatusCanceled, false},
		{constants.OrderStatusPendingPayment, false},
		{constants.OrderStatusPartiallyRefunded, true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			_, db, _ := setupAffiliateServiceTest(t)
			if err := db.AutoMigrate(&affiliatedomain.Commission{}, &orderdomain.Order{}); err != nil {
				t.Fatalf("migrate commission: %v", err)
			}
			promoter := createAffiliateTestUser(t, db, "promoter@example.com")
			profile := createAffiliateTestProfile(t, db, promoter.ID, "PROMO1", constants.AffiliateProfileStatusActive)

			order := &orderdomain.Order{
				ID: 10, OrderNo: "RETRY-10", TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), UserID: promoter.ID + 1, Status: tc.status, AffiliateProfileID: &profile.ID,
				Items: []orderdomain.OrderItem{{ProductID: 1, TotalPrice: money.FromDecimal(decimal.NewFromInt(100))}},
			}
			if tc.status == constants.OrderStatusPartiallyRefunded {
				order.RefundedAmount = money.FromDecimal(decimal.NewFromInt(40))
			}
			if err := db.Omit("Items").Create(order).Error; err != nil {
				t.Fatal(err)
			}
			store := affiliategormstore.New(db)
			settings := settingsapp.NewService(memorysettings.New())
			if _, err := settings.UpdateAffiliateSetting(settingsintegration.AffiliateSetting{Enabled: true, CommissionRate: 20}); err != nil {
				t.Fatalf("init affiliate setting: %v", err)
			}
			svc := affiliateapp.NewService(store, userstore.New(db), staticOrderReader{order}, staticProductReader{}, settings)
			if err := svc.HandleOrderPaid(order.ID); err != nil {
				t.Fatalf("HandleOrderPaid: %v", err)
			}
			got, err := store.GetCommissionByOrderAndProfile(order.ID, profile.ID, constants.AffiliateCommissionTypeOrder)
			if err != nil {
				t.Fatalf("load commission: %v", err)
			}
			if got != nil && tc.status == constants.OrderStatusPartiallyRefunded && !got.CommissionAmount.Decimal.Equal(decimal.NewFromInt(12)) {
				t.Fatalf("partial refund commission = %s, want 12", got.CommissionAmount.Decimal)
			}
			if err := svc.HandleOrderPaid(order.ID); err != nil {
				t.Fatalf("duplicate retry: %v", err)
			}
			if (got != nil) != tc.want {
				t.Fatalf("status %s: commission created=%v, want %v", tc.status, got != nil, tc.want)
			}
		})
	}
}

// A refund may commit after HandleOrderPaid read its snapshot but before insert.
func TestCommissionInsertRechecksOrderAfterRefund(t *testing.T) {
	for _, tc := range []struct {
		status string
		refund int64
		want   int64
	}{
		{constants.OrderStatusRefunded, 100, 0},
		{constants.OrderStatusCanceled, 0, 0},
		{constants.OrderStatusPartiallyRefunded, 40, 12},
	} {
		t.Run(tc.status, func(t *testing.T) {
			_, db, _ := setupAffiliateServiceTest(t)
			if err := db.AutoMigrate(&affiliatedomain.Commission{}, &orderdomain.Order{}); err != nil {
				t.Fatal(err)
			}
			promoter := createAffiliateTestUser(t, db, "retry-stale@example.test")
			profile := createAffiliateTestProfile(t, db, promoter.ID, "STALE", constants.AffiliateProfileStatusActive)
			order := orderdomain.Order{OrderNo: "STALE-REFUND", Status: tc.status, TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), RefundedAmount: money.FromDecimal(decimal.NewFromInt(tc.refund))}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			store := affiliategormstore.New(db)
			commission := &affiliatedomain.Commission{OrderID: order.ID, AffiliateProfileID: profile.ID, CommissionType: constants.AffiliateCommissionTypeOrder, BaseAmount: money.FromDecimal(decimal.NewFromInt(100)), CommissionAmount: money.FromDecimal(decimal.NewFromInt(20)), Status: constants.AffiliateCommissionStatusAvailable}
			for i := 0; i < 2; i++ {
				if err := store.CreateOrderCommission(commission); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.GetCommissionByOrderAndProfile(order.ID, profile.ID, constants.AffiliateCommissionTypeOrder)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == 0 {
				if got != nil {
					t.Fatal("late retry recreated canceled/refunded commission")
				}
				return
			}
			if got == nil || !got.CommissionAmount.Decimal.Equal(decimal.NewFromInt(tc.want)) {
				t.Fatalf("wrong net commission: %+v", got)
			}
			var count int64
			if err := db.Model(&affiliatedomain.Commission{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("duplicate retry count=%d err=%v", count, err)
			}
		})
	}
}
