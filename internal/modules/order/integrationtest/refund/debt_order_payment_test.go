package refund_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dujiao-next/internal/constants"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Exercise the real order/wallet transaction bridge, including persisted payment
// allocation and ledger values, after recharge has offset a negative balance.
func TestDebtOrderPaymentAfterRecharge(t *testing.T) {
	cases := []struct {
		name                                   string
		recharge, total, paid, online, balance int64
	}{
		{"debt_no_recharge", 0, 99, 0, 99, -5},
		{"partial_debt_repayment", 2, 99, 0, 99, -3},
		{"exact_debt_repayment", 5, 99, 0, 99, 0},
		{"surplus_fully_pays_small_order", 8, 2, 2, 0, 1},
		{"surplus_partially_pays_large_order", 8, 99, 3, 96, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 201)
			if err := db.Create(&walletdomain.Account{UserID: 201, Balance: money.FromDecimal(decimal.NewFromInt(-5))}).Error; err != nil {
				t.Fatal(err)
			}
			wallets := walletServiceForTest(db)
			if tc.recharge > 0 {
				_, txn, err := wallets.Recharge(walletcontract.RechargeInput{UserID: 201, Amount: money.FromDecimal(decimal.NewFromInt(tc.recharge)), Remark: "debt repayment regression"})
				if err != nil {
					t.Fatal(err)
				}
				if txn == nil || txn.Direction != constants.WalletTxnDirectionIn || !txn.Amount.Decimal.Equal(decimal.NewFromInt(tc.recharge)) || !txn.BalanceBefore.Decimal.Equal(decimal.NewFromInt(-5)) || !txn.BalanceAfter.Decimal.Equal(decimal.NewFromInt(tc.recharge-5)) {
					t.Fatalf("incorrect recharge ledger: %+v", txn)
				}
			}
			order := createTestOrder(t, db, 201, "DEBT-"+tc.name, decimal.NewFromInt(tc.total))
			// Reload the order for every attempt, as a new payment request would do.
			for attempt := 0; attempt < 2; attempt++ {
				if err := db.Transaction(func(tx *gorm.DB) error {
					var current orderdomain.Order
					if err := tx.First(&current, order.ID).Error; err != nil {
						return err
					}
					paid, err := orderapp.ApplyWalletBalance(wallets, ordergormstore.UseTransaction(tx, "test-guest-credential-secret-with-32-bytes"), &current, true)
					if tc.recharge < 5 {
						if !errors.Is(err, walletcontract.ErrOutstandingDebt) {
							return fmt.Errorf("expected outstanding debt error, got %v", err)
						}
						return nil
					}
					if err != nil {
						return err
					}
					if !paid.Equal(decimal.NewFromInt(tc.paid)) {
						return fmt.Errorf("paid=%s want=%d", paid, tc.paid)
					}
					return nil
				}); err != nil {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
			}
			var persisted orderdomain.Order
			if err := db.First(&persisted, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !persisted.WalletPaidAmount.Decimal.Equal(decimal.NewFromInt(tc.paid)) || !persisted.OnlinePaidAmount.Decimal.Equal(decimal.NewFromInt(tc.online)) {
				t.Fatalf("allocation wallet=%s online=%s; want %d/%d", persisted.WalletPaidAmount, persisted.OnlinePaidAmount, tc.paid, tc.online)
			}
			account, err := wallets.GetAccount(201)
			if err != nil {
				t.Fatal(err)
			}
			if !account.Balance.Decimal.Equal(decimal.NewFromInt(tc.balance)) {
				t.Fatalf("balance=%s want=%d", account.Balance, tc.balance)
			}
			var payments []walletdomain.Transaction
			if err := db.Where("user_id = ? AND direction = ?", 201, constants.WalletTxnDirectionOut).Find(&payments).Error; err != nil {
				t.Fatal(err)
			}
			if tc.paid == 0 {
				if len(payments) != 0 {
					t.Fatalf("nonpositive wallet created debit: %+v", payments)
				}
			} else {
				if len(payments) != 1 {
					t.Fatalf("expected exactly one debit after repeated payment, got %d", len(payments))
				}
				txn := payments[0]
				if txn.Type != constants.WalletTxnTypeOrderPay || txn.OrderID == nil || *txn.OrderID != order.ID || !txn.Amount.Decimal.Equal(decimal.NewFromInt(tc.paid)) || !txn.BalanceBefore.Decimal.Equal(decimal.NewFromInt(tc.recharge-5)) || !txn.BalanceAfter.Decimal.Equal(decimal.NewFromInt(tc.balance)) {
					t.Fatalf("incorrect payment ledger: %+v", txn)
				}
			}
			t.Logf("debt=-5 recharge=%d order=%d wallet_paid=%d online_due=%d final_balance=%d; repeat did not add a debit", tc.recharge, tc.total, tc.paid, tc.online, tc.balance)
		})
	}
}
