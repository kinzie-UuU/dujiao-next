package application_test

import (
	"errors"
	"github.com/dujiao-next/internal/constants"
	coupondomain "github.com/dujiao-next/internal/modules/coupon/domain"
	. "github.com/dujiao-next/internal/modules/order/application"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"testing"
	"time"
)

type beforeCancelTransactionStore struct {
	ordercontract.Store
	before func()
}

func (s *beforeCancelTransactionStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	s.before()
	return s.Store.WithinTransaction(fn)
}
func TestCancelRechecksPaidStateInsideTransaction(t *testing.T) {
	db := setupCancelPaymentTestDB(t, "cancel_paid_race")
	order := newPendingOrderForCancel("CANCEL-PAID-RACE", 42, nil, time.Now())
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	store := &beforeCancelTransactionStore{Store: ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes"), before: func() {
		if err := db.Model(&orderdomain.Order{}).Where("id = ?", order.ID).Updates(map[string]interface{}{"status": constants.OrderStatusPaid, "paid_at": time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}}
	svc := NewOrderService(OrderServiceOptions{OrderStore: store})
	if _, err := svc.CancelOrder(order.ID, 42); !errors.Is(err, ErrOrderCancelNotAllowed) {
		t.Fatalf("got %v", err)
	}
	var saved orderdomain.Order
	if err := db.First(&saved, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != constants.OrderStatusPaid {
		t.Fatalf("paid order changed to %s", saved.Status)
	}
}
func TestUserCancellationReturnsCouponUsage(t *testing.T) {
	db := setupCancelPaymentTestDB(t, "cancel_coupon_return")
	order := newPendingOrderForCancel("CANCEL-COUPON", 42, nil, time.Now())
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	coupon := &coupondomain.Coupon{Code: "CANCEL-COUPON", Type: constants.CouponTypeFixed, UsedCount: 1, UsageLimit: 1, IsActive: true}
	if err := db.Create(coupon).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&coupondomain.CouponUsage{CouponID: coupon.ID, OrderID: order.ID, UserID: 42}).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(OrderServiceOptions{OrderStore: ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes")})
	if _, err := svc.CancelOrder(order.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := db.First(coupon, coupon.ID).Error; err != nil {
		t.Fatal(err)
	}
	if coupon.UsedCount != 0 {
		t.Fatalf("coupon used count %d", coupon.UsedCount)
	}
	var count int64
	if err := db.Model(&coupondomain.CouponUsage{}).Where("order_id = ? AND deleted_at IS NULL", order.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("usage count %d", count)
	}
}
