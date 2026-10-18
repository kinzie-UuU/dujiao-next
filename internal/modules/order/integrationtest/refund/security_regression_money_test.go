package refund_test

import (
	"errors"
	"github.com/dujiao-next/internal/constants"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	. "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

func TestSecurityRegressionOrderRefundServiceChildRefundCountsAgainstParent(t *testing.T) {
	svc, db := setupOrderRefundServiceTest(t)
	now := time.Now()
	parent := &orderdomain.Order{
		OrderNo: "REFUND-CHILD-PARENT", GuestEmail: "guest-child-parent@example.com",
		Status: constants.OrderStatusDelivered, Currency: "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(50)), WalletPaidAmount: money.FromDecimal(decimal.NewFromInt(50)), PaidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent failed: %v", err)
	}
	child := &orderdomain.Order{
		OrderNo: "REFUND-CHILD-PARENT-1", ParentID: &parent.ID, GuestEmail: parent.GuestEmail,
		Status: constants.OrderStatusDelivered, Currency: "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(50)), WalletPaidAmount: money.FromDecimal(decimal.NewFromInt(50)), PaidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child failed: %v", err)
	}
	fifty := money.FromDecimal(decimal.NewFromInt(50))
	for _, id := range []uint{child.ID, parent.ID} {
		if _, _, err := svc.AdminManualRefund(AdminManualRefundInput{OrderID: id, Amount: fifty}); !errors.Is(err, orderapp.ErrOriginalRouteRefundRequired) {
			t.Fatalf("unverified manual refund accepted: %v", err)
		}
	}
	var records int64
	db.Model(&orderdomain.OrderRefundRecord{}).Count(&records)
	if records != 0 {
		t.Fatalf("rejected refunds wrote %d records", records)
	}
	for _, id := range []uint{child.ID, parent.ID} {
		var persisted orderdomain.Order
		if err := db.First(&persisted, id).Error; err != nil {
			t.Fatal(err)
		}
		if !persisted.RefundedAmount.Decimal.IsZero() || persisted.Status != constants.OrderStatusDelivered {
			t.Fatalf("rejected refund changed order: %+v", persisted)
		}
	}
}

func TestSecurityRegressionWalletChildRefundCountsAgainstParent(t *testing.T) {
	svc, db := setupOrderRefundWalletTest(t)
	createTestUser(t, db, 901)
	now := time.Now()
	parent := &orderdomain.Order{
		UserID: 901, OrderNo: "REFUND-CHILD-PARENT", GuestEmail: "guest-child-parent@example.com",
		Status: constants.OrderStatusDelivered, Currency: "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(50)), WalletPaidAmount: money.FromDecimal(decimal.NewFromInt(50)), PaidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent failed: %v", err)
	}
	child := &orderdomain.Order{
		UserID: 901, OrderNo: "REFUND-CHILD-PARENT-1", ParentID: &parent.ID, GuestEmail: parent.GuestEmail,
		Status: constants.OrderStatusDelivered, Currency: "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(50)), WalletPaidAmount: money.FromDecimal(decimal.NewFromInt(50)), PaidAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child failed: %v", err)
	}
	fifty := money.FromDecimal(decimal.NewFromInt(50))
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: child.ID, Amount: fifty}); err != nil {
		t.Fatalf("child refund failed: %v", err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: parent.ID, Amount: fifty}); !errors.Is(err, walletcontract.ErrRefundExceeded) {
		var account struct{ Balance string }
		db.Table("wallet_accounts").Select("balance").Where("user_id = ?", 901).Take(&account)
		t.Fatalf("parent refund after full child refund must be rejected, got %v; wallet balance=%s for original order total=50", err, account.Balance)
	}
}
