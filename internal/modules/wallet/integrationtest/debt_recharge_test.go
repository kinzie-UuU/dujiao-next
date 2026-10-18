package integrationtest

import (
	"fmt"
	"testing"

	"github.com/dujiao-next/internal/constants"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// Exercise the same credit entry point used by successful online payment callbacks,
// including replaying its stable recharge reference after the transaction commits.
func TestDebtRechargeOffsetsBalanceAndRecordsLedger(t *testing.T) {
	for _, online := range []bool{true, false} {
		for _, amount := range []int64{2, 5, 8} {
			t.Run(fmt.Sprintf("online_%t/credit_%d", online, amount), func(t *testing.T) {
				store, db := setupWalletRepositoryTest(t)
				service := walletapp.NewService(walletapp.Options{Repository: store, Transactions: store})
				const userID = uint(9301)
				if err := db.Create(&walletdomain.Account{UserID: userID, Balance: money.FromDecimal(decimal.NewFromInt(-5))}).Error; err != nil {
					t.Fatal(err)
				}
				var transaction *walletdomain.Transaction
				if online {
					recharge := &walletdomain.RechargeOrder{
						RechargeNo: "DEBT-RECHARGE", UserID: userID, PaymentID: 9301,
						Amount: money.FromDecimal(decimal.NewFromInt(amount)), Currency: "CNY",
						Status: constants.WalletRechargeStatusSuccess,
					}
					if err := db.Create(recharge).Error; err != nil {
						t.Fatal(err)
					}
					var firstID uint
					for attempt := 0; attempt < 2; attempt++ {
						if err := store.WithinTransaction(func(tx walletcontract.Transaction) error {
							var err error
							transaction, err = service.ApplyRechargePayment(tx, recharge)
							return err
						}); err != nil {
							t.Fatalf("online credit attempt %d: %v", attempt, err)
						}
						if attempt == 0 {
							firstID = transaction.ID
						} else if transaction.ID != firstID {
							t.Fatalf("duplicate callback created transaction %d instead of returning %d", transaction.ID, firstID)
						}
					}
				} else {
					var err error
					_, transaction, err = service.Recharge(walletcontract.RechargeInput{
						UserID: userID, Amount: money.FromDecimal(decimal.NewFromInt(amount)), Currency: "CNY",
					})
					if err != nil {
						t.Fatalf("recharge: %v", err)
					}
				}
				account, err := service.GetAccount(userID)
				if err != nil {
					t.Fatal(err)
				}
				if !account.Balance.Decimal.Equal(decimal.NewFromInt(amount - 5)) {
					t.Fatalf("balance %s, want %d", account.Balance.String(), amount-5)
				}
				var ledger []walletdomain.Transaction
				if err := db.Where("user_id = ?", userID).Find(&ledger).Error; err != nil {
					t.Fatal(err)
				}
				if len(ledger) != 1 {
					t.Fatalf("ledger has %d rows, want one", len(ledger))
				}
				row := ledger[0]
				if row.ID != transaction.ID || row.Type != constants.WalletTxnTypeRecharge || row.Direction != constants.WalletTxnDirectionIn || row.Currency != "CNY" ||
					!row.Amount.Decimal.Equal(decimal.NewFromInt(amount)) || !row.BalanceBefore.Decimal.Equal(decimal.NewFromInt(-5)) || !row.BalanceAfter.Decimal.Equal(decimal.NewFromInt(amount-5)) {
					t.Fatalf("incorrect recharge ledger: %+v", row)
				}
				t.Logf("debt -5 + recharge %d = %d; ledger +%d, before -5, after %d, rows 1", amount, amount-5, amount, amount-5)
			})
		}
	}
}
