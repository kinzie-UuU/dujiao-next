package integrationtest

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatecontract "github.com/dujiao-next/internal/modules/affiliate/contract"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

type rewardOrderReader struct{ order *orderdomain.Order }

type rewardLockedStore struct {
	affiliatecontract.Store
	order *orderdomain.Order
	locks *int
}

type rewardRollbackStore struct{ affiliatecontract.Store }

var errRewardRollback = errors.New("reward rollback")

func (r rewardRollbackStore) WithinTransaction(fn func(affiliatecontract.Store) error) error {
	return r.Store.WithinTransaction(func(tx affiliatecontract.Store) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errRewardRollback
	})
}

func (r rewardLockedStore) GetRewardOrderForUpdate(id uint) (*orderdomain.Order, error) {
	*r.locks++
	return r.Store.GetRewardOrderForUpdate(id)
}
func (r rewardLockedStore) WithinTransaction(fn func(affiliatecontract.Store) error) error {
	return r.Store.WithinTransaction(func(tx affiliatecontract.Store) error { return fn(rewardLockedStore{tx, r.order, r.locks}) })
}

func (r rewardOrderReader) GetByID(uint) (*orderdomain.Order, error) { return r.order, nil }

type rewardProductReader []productdomain.Product

func (r rewardProductReader) ListByIDs([]uint) ([]productdomain.Product, error) { return r, nil }

type rewardSettings struct {
	value settingsintegration.AffiliateSetting
}

func (s rewardSettings) GetAffiliateSetting() (settingsintegration.AffiliateSetting, error) {
	return s.value, nil
}

func TestGPTRewardEligibilityAndIdempotency(t *testing.T) {
	for _, scenario := range []string{"all-rounding-cap", "all-other-product", "all-future-product", "all-mixed", "eligible", "mixed", "other-product", "not-listed", "child", "unpaid", "self", "usd", "disabled", "zero", "low-value", "refunded", "partial-refund", "child-refund", "stale-reader"} {
		t.Run(scenario, func(t *testing.T) {
			_, db, store := setupAffiliateServiceTest(t)
			if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &affiliatedomain.Commission{}); err != nil {
				t.Fatal(err)
			}
			user := createAffiliateTestUser(t, db, "reward@example.com")
			profile := createAffiliateTestProfile(t, db, user.ID, "GPTREW01", constants.AffiliateProfileStatusActive)
			m := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
			now := time.Now()
			order := &orderdomain.Order{ID: 10, UserID: user.ID + 1, AffiliateProfileID: &profile.ID, Currency: "CNY", PaidAt: &now, Status: constants.OrderStatusPaid, TotalAmount: m(100), Items: []orderdomain.OrderItem{{ProductID: 1, TotalPrice: m(100), Quantity: 3}}}
			products := rewardProductReader{{ID: 1, Slug: "gpt-plus-ios-us"}, {ID: 2, Slug: "claude-pro", IsAffiliateEnabled: true}}
			setting := settingsintegration.AffiliateSetting{Enabled: true, RewardMode: settingsintegration.AffiliateRewardModeGPTWallet, EligibleProductIDs: []uint{1, 2}, ConfirmDays: 7}
			wantReward := false
			switch scenario {
			case "all-rounding-cap", "all-other-product", "all-future-product", "all-mixed":
				wantReward = true
				setting.AllProducts = true
				setting.EligibleProductIDs = nil
				setting.ConfirmDays = 0
				order.Items[0].ProductID = 2
				products[1].IsAffiliateEnabled = false
				if scenario == "all-future-product" {
					order.Items[0].ProductID = 999
					products = nil // New/deleted catalog entries do not affect paid snapshots.
				}
				if scenario == "all-rounding-cap" {
					order.TotalAmount = money.FromDecimal(decimal.RequireFromString("2.99"))
					order.Children = []orderdomain.Order{{ID: 11, TotalAmount: m(100), Items: order.Items}, {ID: 12, TotalAmount: m(100), Items: order.Items}, {ID: 13, TotalAmount: m(100), Items: order.Items}}
				}
				if scenario == "all-mixed" {
					order.Children = []orderdomain.Order{{ID: 11, TotalAmount: m(100), Items: []orderdomain.OrderItem{{ProductID: 1, TotalPrice: m(100)}}}, {ID: 12, TotalAmount: m(100), Items: order.Items}}
					order.TotalAmount = m(200)
				}
			case "eligible":
				wantReward = true
			case "mixed":
				wantReward = true
				order.Children = []orderdomain.Order{{ID: 11, TotalAmount: m(100), Items: order.Items}, {ID: 12, TotalAmount: m(100), Items: []orderdomain.OrderItem{{ProductID: 2, TotalPrice: m(100)}}}}
				order.TotalAmount = m(200)
			case "other-product":
				order.Items[0].ProductID = 2
			case "not-listed":
				setting.EligibleProductIDs = []uint{2}
			case "child":
				parent := uint(9)
				order.ParentID = &parent
			case "unpaid":
				order.PaidAt = nil
			case "self":
				order.UserID = user.ID
			case "usd":
				order.Currency = "USD"
			case "disabled":
				setting.Enabled = false
			case "zero":
				order.TotalAmount = m(0)
			case "low-value":
				wantReward = true
				order.TotalAmount = m(1)
			case "refunded":
				order.Status = constants.OrderStatusRefunded
			case "partial-refund":
				order.RefundedAmount = m(10)
			case "child-refund":
				order.Children = []orderdomain.Order{{ID: 11, TotalAmount: m(100), RefundedAmount: m(10), Items: order.Items}}
			}
			locks := 0
			order.OrderNo = "reward-parent"
			for i := range order.Items {
				order.Items[i].TitleJSON = jsonmap.JSON{"en": "test"}
			}
			for i := range order.Children {
				order.Children[i].OrderNo = "reward-child-" + strconv.Itoa(i)
				for j := range order.Children[i].Items {
					order.Children[i].Items[j].TitleJSON = jsonmap.JSON{"en": "test"}
				}
			}
			if err := db.Create(order).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "stale-reader" {
				if err := db.Model(&orderdomain.Order{}).Where("id = ?", order.ID).Update("refunded_amount", m(10)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "eligible" {
				rollbackSvc := affiliateapp.NewService(rewardRollbackStore{store}, nil, rewardOrderReader{order}, products, rewardSettings{setting})
				if err := rollbackSvc.HandleOrderPaid(order.ID); !errors.Is(err, errRewardRollback) {
					t.Fatal(err)
				}
				rows, err := store.ListCommissionsByOrder(order.ID, nil)
				if err != nil || len(rows) != 0 {
					t.Fatal("reward survived transaction rollback")
				}
			}
			svc := affiliateapp.NewService(rewardLockedStore{store, order, &locks}, nil, rewardOrderReader{order}, products, rewardSettings{setting})
			if err := svc.HandleOrderPaid(order.ID); err != nil {
				t.Fatal(err)
			}
			if scenario != "disabled" && locks != 1 {
				t.Fatalf("generation did not lock order: %d", locks)
			}
			rows, err := store.ListCommissionsByOrder(order.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !wantReward {
				if len(rows) != 0 {
					t.Fatal("ineligible order earned reward")
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("want one reward per checkout, got %d", len(rows))
			}
			first := rows[0]
			if first.RewardOrderAmounts["10"] == "" || first.RewardOrderTotals["10"] == "" {
				t.Fatal("missing reward snapshot")
			}
			if scenario == "mixed" && (first.RewardOrderAmounts["11"] != "100.00" || first.RewardOrderAmounts["12"] != "0.00") {
				t.Fatalf("wrong mixed snapshot: %+v", first.RewardOrderAmounts)
			}
			if scenario == "all-rounding-cap" {
				if first.RewardOrderAmounts["10"] != "2.99" || first.RewardOrderAmounts["11"] != "1.00" || first.RewardOrderAmounts["12"] != "1.00" || first.RewardOrderAmounts["13"] != "0.99" {
					t.Fatalf("rounded shares exceeded paid amount or were unstable: %+v", first.RewardOrderAmounts)
				}
				if first.CommissionAmount.Decimal.GreaterThan(decimal.NewFromInt(2)) {
					t.Fatal("reward exceeded paid whole yuan")
				}
			}
			value := first.CommissionAmount.Decimal
			if value.LessThan(decimal.NewFromInt(1)) || value.GreaterThan(decimal.NewFromInt(5)) || !value.Equal(value.Truncate(0)) {
				t.Fatalf("not integer 1-5: %s", value)
			}
			if scenario == "low-value" && !value.Equal(decimal.NewFromInt(1)) {
				t.Fatal("reward exceeded spend")
			}
			if setting.AllProducts {
				if first.Status != constants.AffiliateCommissionStatusAvailable || first.AvailableAt == nil {
					t.Fatal("zero-day all-product reward is not immediately available")
				}
			} else if first.Status != constants.AffiliateCommissionStatusPendingConfirm || first.ConfirmAt == nil {
				t.Fatal("confirmation window ignored")
			}
			for i := 0; i < 5; i++ {
				if err := svc.HandleOrderPaid(order.ID); err != nil {
					t.Fatal(err)
				}
			}
			rows, err = store.ListCommissionsByOrder(order.ID, nil)
			if err != nil || len(rows) != 1 || !rows[0].CommissionAmount.Decimal.Equal(value) {
				t.Fatal("repeated callback rerolled reward")
			}
			if scenario == "all-mixed" {
				if first.RewardOrderAmounts["11"] != "100.00" || first.RewardOrderAmounts["12"] != "100.00" || first.RewardOrderAmounts["10"] != "200.00" {
					t.Fatalf("all-product mixed snapshot: %+v", first.RewardOrderAmounts)
				}
				// A later policy/catalog change cannot exempt the non-GPT child's refund.
				changed := affiliateapp.NewService(store, nil, rewardOrderReader{order}, nil, rewardSettings{settingsintegration.AffiliateSetting{}})
				if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
					return changed.HandleOrderRefunded(tx, order, decimal.NewFromInt(100), decimal.Zero, "other-refund", 12)
				}); err != nil {
					t.Fatal(err)
				}
				rows, _ = store.ListCommissionsByOrder(order.ID, nil)
				if !rows[0].CommissionAmount.Decimal.Equal(value.Sub(value.Div(decimal.NewFromInt(2)).Round(2))) {
					t.Fatal("non-GPT child refund did not use frozen all-product snapshot")
				}
				if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
					return changed.HandleOrderRefunded(tx, order, decimal.NewFromInt(100), decimal.NewFromInt(100), "remaining-refund", 11)
				}); err != nil {
					t.Fatal(err)
				}
				rows, _ = store.ListCommissionsByOrder(order.ID, nil)
				if !rows[0].CommissionAmount.Decimal.IsZero() {
					t.Fatal("full refund did not revoke all-product reward")
				}
			}
			if scenario == "mixed" {
				// Product eligibility changes after payment must not alter the stored refund basis.
				changed := affiliateapp.NewService(store, nil, rewardOrderReader{order}, rewardProductReader{{ID: 1, Slug: "renamed"}}, rewardSettings{settingsintegration.AffiliateSetting{}})
				if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
					return changed.HandleOrderRefunded(tx, order, decimal.NewFromInt(100), decimal.Zero, "other-refund", 12)
				}); err != nil {
					t.Fatal(err)
				}
				rows, _ = store.ListCommissionsByOrder(order.ID, nil)
				if !rows[0].CommissionAmount.Decimal.Equal(value) {
					t.Fatal("non-GPT refund deducted reward")
				}
				if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
					return changed.HandleOrderRefunded(tx, order, decimal.NewFromInt(100), decimal.NewFromInt(100), "gpt-refund", 11)
				}); err != nil {
					t.Fatal(err)
				}
				rows, _ = store.ListCommissionsByOrder(order.ID, nil)
				if !rows[0].CommissionAmount.Decimal.IsZero() {
					t.Fatal("GPT refund did not use frozen eligibility")
				}
			}
		})
	}
}
