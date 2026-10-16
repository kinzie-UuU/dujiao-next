package application_test

import (
	"github.com/dujiao-next/internal/constants"
	. "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

func TestSecurityRegressionUpdateOrderStatusRejectsCancelingPaidParent(t *testing.T) {
	db := setupCancelPaymentTestDB(t, "audit_paid_cancel")
	now := time.Now()
	parent := &orderdomain.Order{
		OrderNo:          "PAID-PARENT-NO-CANCEL",
		Status:           constants.OrderStatusDelivered,
		Currency:         "CNY",
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(100)),
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(100)),
		WalletPaidAmount: money.FromDecimal(decimal.NewFromInt(100)),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent failed: %v", err)
	}
	child := &orderdomain.Order{
		OrderNo:     "PAID-PARENT-NO-CANCEL-A",
		ParentID:    &parent.ID,
		Status:      constants.OrderStatusDelivered,
		Currency:    "CNY",
		TotalAmount: money.FromDecimal(decimal.NewFromInt(100)),
		PaidAt:      &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child failed: %v", err)
	}
	svc := NewOrderService(OrderServiceOptions{OrderStore: ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes")})

	if _, err := svc.UpdateOrderStatus(parent.ID, constants.OrderStatusCanceled); err != ErrOrderStatusInvalid {
		t.Fatalf("cancel paid parent error = %v, want %v", err, ErrOrderStatusInvalid)
	}
	var stored orderdomain.Order
	if err := db.First(&stored, parent.ID).Error; err != nil {
		t.Fatalf("reload parent failed: %v", err)
	}
	if stored.Status != constants.OrderStatusDelivered {
		t.Fatalf("paid parent status mutated to %s", stored.Status)
	}
}
