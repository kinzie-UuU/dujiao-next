package refund_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	refundapp "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestMixedGPTRewardRefundUsesSnapshot(t *testing.T) {
	for _, scenario := range []string{"gpt-first", "other-first", "other-then-parent"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 1)
			createTestUser(t, db, 2)
			m := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
			profile := affiliatedomain.Profile{UserID: 1, AffiliateCode: "MIXEDGPT", Status: constants.AffiliateProfileStatusActive}
			if err := db.Create(&profile).Error; err != nil {
				t.Fatal(err)
			}
			parent := createTestOrder(t, db, 2, "MIXED-PARENT", decimal.NewFromInt(1000))
			gpt := createTestOrder(t, db, 2, "MIXED-GPT", decimal.NewFromInt(10))
			other := createTestOrder(t, db, 2, "MIXED-OTHER", decimal.NewFromInt(990))
			for _, o := range []*orderdomain.Order{parent, gpt, other} {
				updates := map[string]interface{}{"paid_at": time.Now(), "status": constants.OrderStatusCompleted}
				if o.ID != parent.ID {
					updates["parent_id"] = parent.ID
				}
				if err := db.Model(o).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			}
			key := func(id uint) string { return strconv.FormatUint(uint64(id), 10) }
			commission := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: parent.ID, CommissionType: "gpt_reward", BaseAmount: m(10), CommissionAmount: m(4), Status: "wallet_credited",
				RewardOrderAmounts: map[string]string{key(parent.ID): "10", key(gpt.ID): "10", key(other.ID): "0"},
				RewardOrderTotals:  map[string]string{key(parent.ID): "1000", key(gpt.ID): "10", key(other.ID): "990"}}
			if err := db.Create(&commission).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&walletdomain.Account{UserID: 1, Balance: m(4)}).Error; err != nil {
				t.Fatal(err)
			}
			// No live product or affiliate setting is supplied: reversal uses only the stored snapshot.
			first, second := gpt, other
			if scenario != "gpt-first" {
				first, second = other, gpt
			}
			if scenario == "other-then-parent" {
				second = parent
			}
			for i, o := range []*orderdomain.Order{first, second} {
				refundAmount := o.TotalAmount
				if scenario == "other-then-parent" && i == 1 {
					refundAmount = m(10)
				}
				if _, _, err := svc.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: o.ID, Amount: refundAmount}); err != nil {
					t.Fatal(err)
				}
				var actual affiliatedomain.Commission
				if err := db.First(&actual, commission.ID).Error; err != nil {
					t.Fatal(err)
				}
				want := int64(0)
				if i == 0 && scenario != "gpt-first" {
					want = 4
				}
				var account walletdomain.Account
				if err := db.Where("user_id = ?", 1).First(&account).Error; err != nil {
					t.Fatal(err)
				}
				if !actual.CommissionAmount.Decimal.Equal(decimal.NewFromInt(want)) || !account.Balance.Decimal.Equal(decimal.NewFromInt(want)) {
					t.Fatalf("step %d reward=%s wallet=%s want=%d", i, actual.CommissionAmount, account.Balance, want)
				}
			}
		})
	}
}

func TestChildRefundReversesParentWalletReward(t *testing.T) {
	for _, channel := range []string{"manual", "wallet"} {
		t.Run(channel, func(t *testing.T) {
			svc, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 1)
			createTestUser(t, db, 2)
			m := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
			profile := affiliatedomain.Profile{UserID: 1, AffiliateCode: "PARENT01", Status: constants.AffiliateProfileStatusActive}
			if err := db.Create(&profile).Error; err != nil {
				t.Fatal(err)
			}
			parent := createTestOrder(t, db, 2, "REWARD-PARENT", decimal.NewFromInt(100))
			now := time.Now()
			if err := db.Model(parent).Updates(map[string]interface{}{"status": constants.OrderStatusCompleted, "paid_at": now}).Error; err != nil {
				t.Fatal(err)
			}
			children := []*orderdomain.Order{createTestOrder(t, db, 2, "REWARD-CHILD-A", decimal.NewFromInt(50)), createTestOrder(t, db, 2, "REWARD-CHILD-B", decimal.NewFromInt(50))}
			for _, child := range children {
				if err := db.Model(child).Updates(map[string]interface{}{"parent_id": parent.ID, "paid_at": now, "status": constants.OrderStatusCompleted}).Error; err != nil {
					t.Fatal(err)
				}
			}
			commission := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: parent.ID, CommissionType: "gpt_reward", BaseAmount: m(100), CommissionAmount: m(4), Status: "wallet_credited"}
			if err := db.Create(&commission).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&walletdomain.Account{UserID: 1, Balance: m(4)}).Error; err != nil {
				t.Fatal(err)
			}
			for i, child := range children {
				var err error
				if channel == "manual" {
					_, _, err = svc.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: child.ID, Amount: m(50)})
				} else {
					_, _, _, err = svc.AdminRefundToWallet(refundapp.AdminRefundToWalletInput{OrderID: child.ID, Amount: m(50)})
				}
				if err != nil {
					t.Fatal(err)
				}
				var actual affiliatedomain.Commission
				if err := db.First(&actual, commission.ID).Error; err != nil {
					t.Fatal(err)
				}
				var account walletdomain.Account
				if err := db.Where("user_id = ?", 1).First(&account).Error; err != nil {
					t.Fatal(err)
				}
				want := decimal.NewFromInt(int64(2 - i*2))
				if !actual.CommissionAmount.Decimal.Equal(want) || !account.Balance.Decimal.Equal(want) {
					t.Fatalf("child %d failed parent reversal: commission=%s balance=%s", i, actual.CommissionAmount, account.Balance)
				}
			}
		})
	}
}
