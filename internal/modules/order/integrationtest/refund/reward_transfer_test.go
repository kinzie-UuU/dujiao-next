package refund_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	affiliatestore "github.com/dujiao-next/internal/modules/affiliate/infrastructure/gormstore"
	refundapp "github.com/dujiao-next/internal/modules/order/application/refund"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestTransferredRewardRefundBalanceAndLedger(t *testing.T) {
	for _, channel := range []string{"manual", "wallet"} {
		t.Run(channel, func(t *testing.T) {
			refunds, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 1)
			createTestUser(t, db, 2)
			m := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
			settings := settingsapp.NewService(settingsstore.New(db))
			if _, err := settings.UpdateAffiliateSetting(settingsintegration.AffiliateSetting{Enabled: true, RewardMode: "gpt_random_wallet", EligibleProductIDs: []uint{1}}); err != nil {
				t.Fatal(err)
			}
			rewards := affiliateapp.NewService(affiliatestore.New(db), nil, nil, nil, settings)
			profile := affiliatedomain.Profile{UserID: 1, AffiliateCode: "TRANSFER", Status: constants.AffiliateProfileStatusActive}
			if err := db.Create(&profile).Error; err != nil {
				t.Fatal(err)
			}
			order := createTestOrder(t, db, 2, "TRANSFER-REFUND", decimal.NewFromInt(100))
			if err := db.Model(order).Updates(map[string]interface{}{"paid_at": time.Now(), "status": constants.OrderStatusCompleted}).Error; err != nil {
				t.Fatal(err)
			}
			key := strconv.FormatUint(uint64(order.ID), 10)
			reward := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: order.ID, CommissionType: "gpt_reward", BaseAmount: m(100), CommissionAmount: m(5), Status: constants.AffiliateCommissionStatusAvailable, RewardOrderAmounts: map[string]string{key: "100"}, RewardOrderTotals: map[string]string{key: "100"}}
			if err := db.Create(&reward).Error; err != nil {
				t.Fatal(err)
			}
			if credited, err := rewards.CreditRewardsToWallet(1); err != nil || !credited.Decimal.Equal(decimal.NewFromInt(5)) {
				t.Fatalf("credit=%s err=%v", credited, err)
			}
			check := func(want int64, status string) {
				t.Helper()
				var account walletdomain.Account
				var actual affiliatedomain.Commission
				if err := db.Where("user_id = ?", 1).First(&account).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(&actual, reward.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !account.Balance.Decimal.Equal(decimal.NewFromInt(want)) || !actual.CommissionAmount.Decimal.Equal(decimal.NewFromInt(want)) || actual.Status != status {
					t.Fatalf("wallet=%s reward=%s status=%s want=%d/%s", account.Balance, actual.CommissionAmount, actual.Status, want, status)
				}
			}
			refund := func(n int64) error {
				if channel == "manual" {
					_, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: order.ID, Amount: m(n)})
					return err
				}
				_, _, _, err := refunds.AdminRefundToWallet(refundapp.AdminRefundToWalletInput{OrderID: order.ID, Amount: m(n)})
				return err
			}
			check(5, "wallet_credited")
			if err := refund(40); err != nil {
				t.Fatal(err)
			}
			check(3, "wallet_credited")
			if err := refund(60); err != nil {
				t.Fatal(err)
			}
			check(0, constants.AffiliateCommissionStatusRejected)
			if err := refund(1); err == nil {
				t.Fatal("refund beyond fully refunded total accepted")
			}
			if credited, err := rewards.CreditRewardsToWallet(1); err != nil || !credited.Decimal.IsZero() {
				t.Fatalf("refunded reward credited again: %s %v", credited, err)
			}
			check(0, constants.AffiliateCommissionStatusRejected)
			var ledger []walletdomain.Transaction
			if err := db.Where("user_id = ?", 1).Order("id").Find(&ledger).Error; err != nil {
				t.Fatal(err)
			}
			if len(ledger) != 3 {
				t.Fatalf("ledger rows=%d want=3", len(ledger))
			}
			for i, want := range []struct {
				kind                  string
				amount, before, after int64
			}{{"affiliate_reward", 5, 0, 5}, {"affiliate_reward_reversal", 2, 5, 3}, {"affiliate_reward_reversal", 3, 3, 0}} {
				row := ledger[i]
				if row.Type != want.kind || !row.Amount.Decimal.Equal(decimal.NewFromInt(want.amount)) || !row.BalanceBefore.Decimal.Equal(decimal.NewFromInt(want.before)) || !row.BalanceAfter.Decimal.Equal(decimal.NewFromInt(want.after)) {
					t.Fatalf("ledger %d: %+v", i, row)
				}
			}
			t.Log("transfer +5: 0->5; refund 40/100: 5->3; refund remaining 60: 3->0; excess refund rejected; retransfer=0; three ledger entries verified")
		})
	}
}
