package refund_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	refundapp "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"github.com/dujiao-next/internal/modules/procurement/backupstate"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestBackupClaimRefundFence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   backupstate.BackupState
		blocked bool
		child   bool
	}{
		{"no claim", "", false, false},
		{"claimed", backupstate.Claimed, true, false},
		{"submitted", backupstate.Submitted, true, false},
		{"uncertain", backupstate.Uncertain, true, false},
		{"completed", backupstate.Completed, false, false},
		{"child claimed", backupstate.Claimed, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupOrderRefundWalletTest(t)
			createTestUser(t, db, 104)
			order := createTestWalletOrder(t, db, 104, fmt.Sprintf("BACKUP-REFUND-%s", tc.name), decimal.NewFromInt(40))
			if err := db.Model(order).Updates(map[string]interface{}{"status": constants.OrderStatusPaid, "paid_at": time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
			targetID := order.ID
			if tc.child {
				child := createTestChildOrderWithFulfillmentType(t, db, order, "BACKUP-REFUND-CHILD", constants.OrderStatusPaid, decimal.NewFromInt(40), constants.FulfillmentTypeManual, false)
				targetID = child.ID
			}
			if tc.state != "" {
				if err := db.Create(&backupstate.Claim{LocalOrderID: targetID, ProductID: 1, SKUID: 1, Product: backupstate.Plus, State: tc.state}).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, _, _, err := svc.AdminRefundToWallet(refundapp.AdminRefundToWalletInput{OrderID: order.ID, Amount: money.FromDecimal(decimal.NewFromInt(40)), Remark: "test backup fence"})
			if tc.blocked && err == nil {
				t.Fatal("refund crossed active backup claim fence")
			}
			if !tc.blocked && err != nil {
				t.Fatalf("refund unexpectedly blocked: %v", err)
			}
			var latest orderdomain.Order
			if err := db.First(&latest, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&walletdomain.Transaction{}).Where("user_id = ?", 104).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if tc.blocked && (latest.RefundedAmount.IsPositive() || count != 0) {
				t.Fatal("blocked refund mutated money or order")
			}
			if !tc.blocked && (!latest.RefundedAmount.Decimal.Equal(decimal.NewFromInt(40)) || count != 1) {
				t.Fatal("allowed refund did not commit exactly once")
			}
		})
	}
}
