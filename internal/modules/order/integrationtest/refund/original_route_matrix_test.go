package refund_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	refundapp "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// A child can have misleading allocation fields: the original payment belongs
// to its parent. Neither a partial refund nor choosing a child may convert the
// parent's external receipt into spendable wallet funds.
func TestOriginalRouteWalletRefundRejectsExternalAndMixedFunding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wallet     int64
		online     int64
		child      bool
		otherOwner bool
	}{
		{name: "external", online: 100},
		{name: "mixed", wallet: 60, online: 40},
		{name: "external_parent_wallet_child", online: 100, child: true},
		{name: "mixed_parent_wallet_child", wallet: 60, online: 40, child: true},
		{name: "wallet_parent_other_owner", wallet: 100, child: true, otherOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 981)
			createTestUser(t, db, 982)
			now := time.Now()
			amount := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
			parent := orderdomain.Order{OrderNo: "ROUTE-P", UserID: 981, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: amount(100), WalletPaidAmount: amount(tc.wallet), OnlinePaidAmount: amount(tc.online), PaidAt: &now, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			target := parent
			if tc.child {
				owner := uint(981)
				if tc.otherOwner {
					owner = 982
				}
				target = orderdomain.Order{OrderNo: "ROUTE-C", ParentID: &parent.ID, UserID: owner, Status: constants.OrderStatusDelivered, Currency: "CNY", TotalAmount: amount(100), WalletPaidAmount: amount(100), PaidAt: &now, CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&target).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, requested := range []int64{1, 100} {
				order, txn, record, err := svc.AdminRefundToWallet(refundapp.AdminRefundToWalletInput{OrderID: target.ID, Amount: amount(requested), Remark: "原路退款已完成"})
				if !errors.Is(err, orderapp.ErrOriginalRouteRefundRequired) || order != nil || txn != nil || record != nil {
					t.Fatalf("external/other-owner refund accepted: order=%v txn=%v record=%v err=%v", order, txn, record, err)
				}
			}
			for _, model := range []interface{}{&walletdomain.Account{}, &walletdomain.Transaction{}, &orderdomain.OrderRefundRecord{}} {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("rejected refund wrote %T: %d rows", model, count)
				}
			}
			for _, id := range []uint{parent.ID, target.ID} {
				var persisted orderdomain.Order
				if err := db.First(&persisted, id).Error; err != nil {
					t.Fatal(err)
				}
				if persisted.Status != constants.OrderStatusDelivered || !persisted.RefundedAmount.Decimal.IsZero() {
					t.Fatalf("rejected refund changed order %d", id)
				}
			}
		})
	}
}
