package refund_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	. "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"

	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	affiliategormstore "github.com/dujiao-next/internal/modules/affiliate/infrastructure/gormstore"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"

	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"

	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"

	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/platform/database/gormdb"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupOrderRefundServiceTest(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:order_service_refund_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&userdomain.User{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&fulfillmentdomain.Fulfillment{},
		&siteconnectiondomain.Connection{},
		&procurementdomain.Order{},
		&affiliatedomain.Profile{},
		&affiliatedomain.Commission{},
		&affiliatedomain.WithdrawRequest{},
		&walletdomain.Account{},
		&walletdomain.Transaction{},
		&orderdomain.OrderRefundRecord{},
		&settingsstore.SettingRecord{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	gormdb.DB = db

	orderStore := ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes")
	affiliateSvc := affiliateapp.NewService(affiliategormstore.New(db), nil, nil, nil, nil)
	userRepo := userstore.New(db)
	settingSvc := settingsapp.NewService(settingsstore.New(db))
	return New(orderStore, userRepo, affiliateSvc, settingSvc, nil), db
}

func createOrderRefundTestSiteConnection(t *testing.T, db *gorm.DB, id uint) *siteconnectiondomain.Connection {
	t.Helper()
	conn := &siteconnectiondomain.Connection{
		ID:        id,
		Name:      fmt.Sprintf("refund-conn-%d", id),
		BaseURL:   "https://upstream.example.com",
		ApiKey:    fmt.Sprintf("refund-key-%d", id),
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
		Status:    constants.ConnectionStatusActive,
		RetryMax:  3,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(conn).Error; err != nil {
		t.Fatalf("create site connection failed: %v", err)
	}
	return conn
}

func TestOrderRefundServiceAdminManualRefundGuestCreatesRecord(t *testing.T) {
	svc, db := setupOrderRefundServiceTest(t)
	now := time.Now()
	order := &orderdomain.Order{
		OrderNo:          "REFUND-MANUAL-GUEST-001",
		UserID:           0,
		GuestEmail:       "guest-refund-record@example.com",
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create guest order failed: %v", err)
	}
	conn := createOrderRefundTestSiteConnection(t, db, 1)
	proc := &procurementdomain.Order{
		ConnectionID:    conn.ID,
		LocalOrderID:    order.ID,
		LocalOrderNo:    order.OrderNo,
		Status:          constants.ProcurementStatusFulfilled,
		LocalSellAmount: money.FromDecimal(order.TotalAmount.Decimal),
		Currency:        order.Currency,
		TraceID:         "manual-refund-proc-sync",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(proc).Error; err != nil {
		t.Fatalf("create procurement order failed: %v", err)
	}

	updatedOrder, createdRecord, err := svc.AdminManualRefund(AdminManualRefundInput{
		OrderID: order.ID,
		Amount:  money.FromDecimal(decimal.NewFromInt(20)),
		Remark:  "manual partial refund",
	})
	if err != nil {
		t.Fatalf("admin manual refund failed: %v", err)
	}
	if updatedOrder == nil || updatedOrder.Status != constants.OrderStatusPartiallyRefunded {
		t.Fatalf("expected partially_refunded order, got %+v", updatedOrder)
	}
	if createdRecord == nil || createdRecord.ID == 0 {
		t.Fatalf("expected created refund record, got %+v", createdRecord)
	}

	var record orderdomain.OrderRefundRecord
	if err := db.Order("id DESC").First(&record).Error; err != nil {
		t.Fatalf("query refund record failed: %v", err)
	}
	if record.OrderID != order.ID {
		t.Fatalf("unexpected refund record order id: %d", record.OrderID)
	}
	if record.UserID != 0 {
		t.Fatalf("unexpected refund record user id: %d", record.UserID)
	}
	if record.GuestEmail != "guest-refund-record@example.com" {
		t.Fatalf("unexpected refund record guest email: %s", record.GuestEmail)
	}
	if record.Type != constants.OrderRefundTypeManual {
		t.Fatalf("unexpected refund record type: %s", record.Type)
	}
	if !record.Amount.Decimal.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("unexpected refund record amount: %s", record.Amount.String())
	}
	if record.Remark != "manual partial refund" {
		t.Fatalf("unexpected refund record remark: %s", record.Remark)
	}
	var refreshedProc procurementdomain.Order
	if err := db.First(&refreshedProc, proc.ID).Error; err != nil {
		t.Fatalf("reload procurement order failed: %v", err)
	}
	if refreshedProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("expected procurement status fulfilled, got: %s", refreshedProc.Status)
	}
}

func TestOrderRefundServiceAdminManualRefundGuestReversesCommission(t *testing.T) {
	for _, status := range []string{constants.AffiliateCommissionStatusPendingConfirm, constants.AffiliateCommissionStatusAvailable} {
		t.Run(status, func(t *testing.T) {
			svc, db := setupOrderRefundServiceTest(t)
			createTestUser(t, db, 1)
			profile := affiliatedomain.Profile{UserID: 1, AffiliateCode: "GUESTREF", Status: constants.AffiliateProfileStatusActive}
			if err := db.Create(&profile).Error; err != nil {
				t.Fatalf("create affiliate profile: %v", err)
			}
			order := createTestOrder(t, db, 0, "GUEST-COMMISSION-REFUND", decimal.NewFromInt(100))
			now := time.Now()
			if err := db.Model(order).Updates(map[string]interface{}{
				"status": constants.OrderStatusCompleted, "paid_at": now,
				"guest_email": "guest-affiliate@example.com", "affiliate_profile_id": profile.ID,
			}).Error; err != nil {
				t.Fatalf("complete guest order: %v", err)
			}
			commission := affiliatedomain.Commission{
				AffiliateProfileID: profile.ID, OrderID: order.ID,
				CommissionType:   constants.AffiliateCommissionTypeOrder,
				BaseAmount:       money.FromDecimal(decimal.NewFromInt(100)),
				RatePercent:      money.FromDecimal(decimal.NewFromInt(10)),
				CommissionAmount: money.FromDecimal(decimal.NewFromInt(10)), Status: status,
			}
			if err := db.Create(&commission).Error; err != nil {
				t.Fatalf("create commission: %v", err)
			}
			for _, step := range []struct {
				refund, remainingCommission, remainingBase int64
				wantStatus                                 string
			}{
				{20, 8, 80, status},
				{80, 0, 0, constants.AffiliateCommissionStatusRejected},
			} {
				_, record, err := svc.AdminManualRefund(AdminManualRefundInput{
					OrderID: order.ID, Amount: money.FromDecimal(decimal.NewFromInt(step.refund)),
				})
				if err != nil {
					t.Fatalf("refund guest order: %v", err)
				}
				if record == nil || record.UserID != 0 {
					t.Fatalf("expected guest refund record, got %+v", record)
				}
				var actual affiliatedomain.Commission
				if err := db.First(&actual, commission.ID).Error; err != nil {
					t.Fatalf("reload commission: %v", err)
				}
				if !actual.CommissionAmount.Decimal.Equal(decimal.NewFromInt(step.remainingCommission)) ||
					!actual.BaseAmount.Decimal.Equal(decimal.NewFromInt(step.remainingBase)) || actual.Status != step.wantStatus {
					t.Fatalf("unexpected refunded commission: amount=%s base=%s status=%s", actual.CommissionAmount, actual.BaseAmount, actual.Status)
				}
			}
		})
	}
}

func TestOrderRefundServiceAdminManualRefundExpiredWindow(t *testing.T) {
	svc, db := setupOrderRefundServiceTest(t)
	paidAt := time.Now().AddDate(0, 0, -31)
	order := &orderdomain.Order{
		OrderNo:          "REFUND-MANUAL-EXPIRED-001",
		UserID:           0,
		GuestEmail:       "guest-refund-expired@example.com",
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           &paidAt,
		CreatedAt:        paidAt,
		UpdatedAt:        paidAt,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create guest order failed: %v", err)
	}

	_, _, err := svc.AdminManualRefund(AdminManualRefundInput{
		OrderID: order.ID,
		Amount:  money.FromDecimal(decimal.NewFromInt(20)),
		Remark:  "manual refund expired",
	})
	if !errors.Is(err, ErrOrderRefundExpired) {
		t.Fatalf("expected refund expired, got: %v", err)
	}
}

func TestOrderRefundServiceAdminManualRefundNoLimitWhenZero(t *testing.T) {
	svc, db := setupOrderRefundServiceTest(t)
	paidAt := time.Now().AddDate(0, 0, -90)
	order := &orderdomain.Order{
		OrderNo:          "REFUND-MANUAL-NOLIMIT-001",
		UserID:           0,
		GuestEmail:       "guest-refund-nolimit@example.com",
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           &paidAt,
		CreatedAt:        paidAt,
		UpdatedAt:        paidAt,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create guest order failed: %v", err)
	}

	if _, err := settingsapp.NewService(settingsstore.New(db)).Update(constants.SettingKeyOrderConfig, map[string]interface{}{
		settingsapp.OrderConfigFieldMaxRefundDays: 0,
	}); err != nil {
		t.Fatalf("update order refund config failed: %v", err)
	}

	updatedOrder, _, err := svc.AdminManualRefund(AdminManualRefundInput{
		OrderID: order.ID,
		Amount:  money.FromDecimal(decimal.NewFromInt(20)),
		Remark:  "manual refund no limit",
	})
	if err != nil {
		t.Fatalf("expected no-limit refund success, got: %v", err)
	}
	if updatedOrder == nil || updatedOrder.Status != constants.OrderStatusPartiallyRefunded {
		t.Fatalf("expected partially_refunded order, got %+v", updatedOrder)
	}
}

func createOrderRefundTestChildWithFulfillmentType(
	t *testing.T,
	db *gorm.DB,
	parent *orderdomain.Order,
	orderNo string,
	status string,
	total decimal.Decimal,
	fulfillmentType string,
	withFulfillment bool,
) *orderdomain.Order {
	t.Helper()
	now := time.Now()
	child := &orderdomain.Order{
		OrderNo:          orderNo,
		ParentID:         &parent.ID,
		UserID:           parent.UserID,
		GuestEmail:       parent.GuestEmail,
		GuestPassword:    parent.GuestPassword,
		GuestLocale:      parent.GuestLocale,
		Status:           status,
		Currency:         parent.Currency,
		OriginalAmount:   money.FromDecimal(total),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(total),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(total),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           parent.PaidAt,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child order failed: %v", err)
	}
	item := &orderdomain.OrderItem{
		OrderID:         child.ID,
		ProductID:       child.ID + 2000,
		SKUID:           1,
		TitleJSON:       jsonmap.JSON{"zh-CN": orderNo},
		UnitPrice:       money.FromDecimal(total),
		CostPrice:       money.FromDecimal(decimal.Zero),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(total),
		FulfillmentType: fulfillmentType,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("create child order item failed: %v", err)
	}
	if withFulfillment {
		fulfillment := &fulfillmentdomain.Fulfillment{
			OrderID:     child.ID,
			Type:        fulfillmentType,
			Status:      constants.FulfillmentStatusDelivered,
			Payload:     "DELIVERED-CONTENT",
			DeliveredAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := db.Create(fulfillment).Error; err != nil {
			t.Fatalf("create child fulfillment failed: %v", err)
		}
	}
	return child
}

type orderManualRefundMixedChildrenFixture struct {
	parentOrderNo        string
	guestEmail           string
	refundAmount         decimal.Decimal
	remark               string
	expectedParentStatus string
	expectedChildStatus  string
}

func assertOrderManualRefundMixedChildrenStatus(t *testing.T, fixture orderManualRefundMixedChildrenFixture) {
	t.Helper()
	svc, db := setupOrderRefundServiceTest(t)
	now := time.Now()
	parent := &orderdomain.Order{
		OrderNo:          fixture.parentOrderNo,
		UserID:           0,
		GuestEmail:       fixture.guestEmail,
		Status:           constants.OrderStatusCompleted,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(100)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(100)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent order failed: %v", err)
	}

	manualChild := createOrderRefundTestChildWithFulfillmentType(
		t, db, parent, fixture.parentOrderNo+"-1", constants.OrderStatusPaid,
		decimal.NewFromInt(60), constants.FulfillmentTypeManual, false,
	)
	autoChild := createOrderRefundTestChildWithFulfillmentType(
		t, db, parent, fixture.parentOrderNo+"-2", constants.OrderStatusCompleted,
		decimal.NewFromInt(40), constants.FulfillmentTypeAuto, true,
	)

	updatedOrder, _, err := svc.AdminManualRefund(AdminManualRefundInput{
		OrderID: parent.ID,
		Amount:  money.FromDecimal(fixture.refundAmount),
		Remark:  fixture.remark,
	})
	if err != nil {
		t.Fatalf("admin manual refund failed: %v", err)
	}
	if updatedOrder == nil || updatedOrder.Status != fixture.expectedParentStatus {
		t.Fatalf("expected parent %s, got %+v", fixture.expectedParentStatus, updatedOrder)
	}
	assertOrderRefundOrderStatus(t, db, manualChild.ID, "manual child", fixture.expectedChildStatus)
	assertOrderRefundOrderStatus(t, db, autoChild.ID, "auto child", fixture.expectedChildStatus)
}

func assertOrderRefundOrderStatus(t *testing.T, db *gorm.DB, orderID uint, label, expected string) {
	t.Helper()
	var refreshed orderdomain.Order
	if err := db.First(&refreshed, orderID).Error; err != nil {
		t.Fatalf("reload %s failed: %v", label, err)
	}
	if refreshed.Status != expected {
		t.Fatalf("expected %s %s, got: %s", label, expected, refreshed.Status)
	}
}

func TestOrderRefundServiceAdminManualRefundParentPartialMixedChildrenStatus(t *testing.T) {
	assertOrderManualRefundMixedChildrenStatus(t, orderManualRefundMixedChildrenFixture{
		parentOrderNo:        "REFUND-MANUAL-MIXED-PARTIAL",
		guestEmail:           "guest-mixed-partial@example.com",
		refundAmount:         decimal.NewFromInt(20),
		remark:               "manual mixed partial refund",
		expectedParentStatus: constants.OrderStatusPartiallyRefunded,
		expectedChildStatus:  constants.OrderStatusPartiallyRefunded,
	})
}

func TestOrderRefundServiceAdminManualRefundParentFullMixedChildrenStatus(t *testing.T) {
	assertOrderManualRefundMixedChildrenStatus(t, orderManualRefundMixedChildrenFixture{
		parentOrderNo:        "REFUND-MANUAL-MIXED-FULL",
		guestEmail:           "guest-mixed-full@example.com",
		refundAmount:         decimal.NewFromInt(100),
		remark:               "manual mixed full refund",
		expectedParentStatus: constants.OrderStatusRefunded,
		expectedChildStatus:  constants.OrderStatusRefunded,
	})
}

func TestOrderRefundServiceResolveStatusEmailRefundDetails(t *testing.T) {
	svc, db := setupOrderRefundServiceTest(t)
	now := time.Now()

	parent := &orderdomain.Order{
		OrderNo:          "REFUND-DETAILS-PARENT",
		UserID:           0,
		GuestEmail:       "guest-details-parent@example.com",
		Status:           constants.OrderStatusPartiallyRefunded,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(100)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(100)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(100)),
		RefundedAmount:   money.FromDecimal(decimal.NewFromInt(20)),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent order failed: %v", err)
	}

	child := &orderdomain.Order{
		OrderNo:          "REFUND-DETAILS-PARENT-1",
		ParentID:         &parent.ID,
		UserID:           0,
		GuestEmail:       parent.GuestEmail,
		Status:           constants.OrderStatusPartiallyRefunded,
		Currency:         parent.Currency,
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(60)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(60)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(60)),
		RefundedAmount:   money.FromDecimal(decimal.NewFromInt(20)),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child order failed: %v", err)
	}

	unrelated := &orderdomain.Order{
		OrderNo:          "REFUND-DETAILS-UNRELATED",
		UserID:           0,
		GuestEmail:       "guest-unrelated@example.com",
		Status:           constants.OrderStatusPaid,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(50)),
		DiscountAmount:   money.FromDecimal(decimal.Zero),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(50)),
		WalletPaidAmount: money.FromDecimal(decimal.Zero),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(50)),
		RefundedAmount:   money.FromDecimal(decimal.Zero),
		PaidAt:           &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(unrelated).Error; err != nil {
		t.Fatalf("create unrelated order failed: %v", err)
	}

	parentRecord := &orderdomain.OrderRefundRecord{
		OrderID:    parent.ID,
		Type:       constants.OrderRefundTypeManual,
		Amount:     money.FromDecimal(decimal.NewFromInt(12)),
		Currency:   "CNY",
		Remark:     "parent refund record",
		CreatedAt:  now,
		UpdatedAt:  now,
		GuestEmail: parent.GuestEmail,
	}
	if err := db.Create(parentRecord).Error; err != nil {
		t.Fatalf("create parent refund record failed: %v", err)
	}
	childRecord := &orderdomain.OrderRefundRecord{
		OrderID:    child.ID,
		Type:       constants.OrderRefundTypeManual,
		Amount:     money.FromDecimal(decimal.NewFromInt(8)),
		Currency:   "CNY",
		Remark:     "child refund record",
		CreatedAt:  now,
		UpdatedAt:  now,
		GuestEmail: child.GuestEmail,
	}
	if err := db.Create(childRecord).Error; err != nil {
		t.Fatalf("create child refund record failed: %v", err)
	}

	details, ok, err := svc.ResolveStatusEmailRefundDetails(parent.ID, parentRecord.ID)
	if err != nil {
		t.Fatalf("resolve same order refund details failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected same order refund record to match")
	}
	if !details.Amount.Decimal.Equal(decimal.NewFromInt(12)) || details.Reason != "parent refund record" {
		t.Fatalf("unexpected same order details: %+v", details)
	}

	details, ok, err = svc.ResolveStatusEmailRefundDetails(parent.ID, childRecord.ID)
	if err != nil {
		t.Fatalf("resolve child refund details failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected child order refund record to match parent order")
	}
	if !details.Amount.Decimal.Equal(decimal.NewFromInt(8)) || details.Reason != "child refund record" {
		t.Fatalf("unexpected child order details: %+v", details)
	}

	_, ok, err = svc.ResolveStatusEmailRefundDetails(unrelated.ID, childRecord.ID)
	if err != nil {
		t.Fatalf("resolve unrelated order refund details failed: %v", err)
	}
	if ok {
		t.Fatalf("expected unrelated order to not match refund record")
	}

	parsed, err := svc.ResolveOrderStatusEmailRefundDetails(parent, parentRecord.ID)
	if err != nil {
		t.Fatalf("resolve order status email refund details by record failed: %v", err)
	}
	if !parsed.Amount.Decimal.Equal(decimal.NewFromInt(12)) || parsed.Reason != "parent refund record" {
		t.Fatalf("unexpected parsed details by record: %+v", parsed)
	}

	fallback, err := svc.ResolveOrderStatusEmailRefundDetails(parent, 999999)
	if err != nil {
		t.Fatalf("resolve order status email refund details fallback failed: %v", err)
	}
	if !fallback.Amount.Decimal.Equal(decimal.NewFromInt(20)) || fallback.Reason != "" {
		t.Fatalf("unexpected fallback details: %+v", fallback)
	}

	var nilSvc *Service
	fallbackFromNilSvc, err := nilSvc.ResolveOrderStatusEmailRefundDetails(parent, parentRecord.ID)
	if err != nil {
		t.Fatalf("resolve order status email refund details with nil service failed: %v", err)
	}
	if !fallbackFromNilSvc.Amount.Decimal.Equal(decimal.NewFromInt(20)) || fallbackFromNilSvc.Reason != "" {
		t.Fatalf("unexpected fallback from nil service: %+v", fallbackFromNilSvc)
	}
}
