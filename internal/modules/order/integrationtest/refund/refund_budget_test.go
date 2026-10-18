package refund_test

import (
	"errors"
	"github.com/dujiao-next/internal/constants"
	. "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"sync"
	"testing"
	"time"
)

func TestRefundBudgetSerializesParentAndChild(t *testing.T) {
	for _, wallet := range []bool{true} {
		t.Run(map[bool]string{false: "manual", true: "wallet"}[wallet], func(t *testing.T) {
			svc, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 902)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			now := time.Now()
			fifty := money.FromDecimal(decimal.NewFromInt(50))
			parent := orderdomain.Order{OrderNo: "BUDGET-P", UserID: 902, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: fifty, WalletPaidAmount: fifty, PaidAt: &now, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			child := orderdomain.Order{OrderNo: "BUDGET-C", ParentID: &parent.ID, UserID: 902, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: fifty, WalletPaidAmount: fifty, PaidAt: &now, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&child).Error; err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, id := range []uint{parent.ID, child.ID} {
				wg.Add(1)
				go func(id uint) {
					defer wg.Done()
					<-start
					var err error
					if wallet {
						_, _, _, err = svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: id, Amount: fifty})
					} else {
						_, _, err = svc.AdminManualRefund(AdminManualRefundInput{OrderID: id, Amount: fifty})
					}
					results <- err
				}(id)
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else if !errors.Is(err, walletcontract.ErrRefundExceeded) {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if successes != 1 {
				t.Fatalf("expected one refund, got %d", successes)
			}
			var total struct{ Amount string }
			if err := db.Table("order_refund_records").Select("sum(amount) AS amount").Scan(&total).Error; err != nil {
				t.Fatal(err)
			}
			if total.Amount != "50" {
				t.Fatalf("refunded=%s", total.Amount)
			}
		})
	}
}

func TestRefundBudgetCountsHistoricalChildRecordsWithoutDoubleCounting(t *testing.T) {
	svc, db := setupOrderRefundWalletTest(t)
	createTestUser(t, db, 903)
	now := time.Now()
	amount := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
	parent := orderdomain.Order{OrderNo: "HISTORY-P", UserID: 903, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: amount(100), WalletPaidAmount: amount(100), PaidAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	child := orderdomain.Order{OrderNo: "HISTORY-C", ParentID: &parent.ID, UserID: 903, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: amount(100), WalletPaidAmount: amount(100), RefundedAmount: amount(25), PaidAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orderdomain.OrderRefundRecord{OrderID: child.ID, UserID: 903, Amount: amount(25), Currency: "CNY"}).Error; err != nil {
		t.Fatal(err)
	}
	// 25 historical + 25 child wallet + 50 parent wallet exhausts the real 100.
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: child.ID, Amount: amount(25)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: parent.ID, Amount: amount(50)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: child.ID, Amount: amount(1)}); !errors.Is(err, walletcontract.ErrRefundExceeded) {
		t.Fatalf("extra refund err=%v", err)
	}
}
