package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	alipayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/alipay"
	bepusdtadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/bepusdt"
	dujiaopayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/dujiaopay"
	epayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/epay"
	epusdtadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/epusdt"
	okpayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/okpay"
	paypaladapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/paypal"
	stripeadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/stripe"
	tokenpayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/tokenpay"
	wechatpayadapter "github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/wechatpay"

	paymentgormstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"

	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"

	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletgormstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"

	"github.com/dujiao-next/internal/constants"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/provider"
	"github.com/dujiao-next/internal/platform/database/gormdb"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupPaymentServiceWalletTest(t *testing.T) (*PaymentService, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:payment_service_wallet_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&userdomain.User{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&fulfillmentdomain.Fulfillment{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&walletdomain.Account{},
		&walletdomain.Transaction{},
		&walletdomain.RechargeOrder{},
		&paymentdomain.PaymentChannel{},
		&paymentdomain.Payment{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	gormdb.DB = db

	orderRepo := ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes")
	productRepo := productgormstore.NewProductStore(db)
	productSKURepo := productgormstore.NewSKUStore(db)
	paymentRepo := paymentgormstore.New(db, "test-guest-credential-secret-with-32-bytes")
	channelRepo := paymentgormstore.NewChannelStore(db)
	walletRepo := walletgormstore.New(db)
	walletSvc := walletapp.NewService(walletapp.Options{
		Repository:   walletRepo,
		Transactions: walletRepo,
	})

	// 构建与 Container.BuildRunner 相同的 PaymentProviderRegistry，
	// 确保 applyProviderPayment 通过 Registry 路由时各 adapter 可以被找到。
	reg := provider.NewRegistry()
	reg.Register(constants.PaymentProviderOfficial, constants.PaymentChannelTypeStripe, stripeadapter.NewStripeAdapter())
	reg.Register(constants.PaymentProviderOfficial, constants.PaymentChannelTypePaypal, paypaladapter.NewPaypalAdapter())
	reg.Register(constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, wechatpayadapter.NewWechatpayAdapter())
	reg.Register(constants.PaymentProviderOfficial, constants.PaymentChannelTypeAlipay, alipayadapter.NewAlipayAdapter())
	reg.Register(constants.PaymentProviderEpay, "", epayadapter.NewEpayAdapter())
	reg.Register(constants.PaymentProviderEpusdt, "", epusdtadapter.NewEpusdtAdapter())
	reg.Register(constants.PaymentProviderBepusdt, "", bepusdtadapter.NewBepusdtAdapter())
	reg.Register(constants.PaymentProviderDujiaoPay, "", dujiaopayadapter.NewDujiaoPayAdapter())
	reg.Register(constants.PaymentProviderTokenpay, "", tokenpayadapter.NewTokenpayAdapter())
	reg.Register(constants.PaymentProviderOkpay, "", okpayadapter.NewOkpayAdapter())

	paymentSvc := NewPaymentService(PaymentServiceOptions{
		OrderStore:              orderRepo,
		ProductRepo:             productRepo,
		ProductSKURepo:          productSKURepo,
		PaymentStore:            paymentRepo,
		ChannelStore:            channelRepo,
		WalletRepo:              walletRepo,
		WalletService:           walletSvc,
		ExpireMinutes:           15,
		PaymentProviderRegistry: reg,
	})

	return paymentSvc, db
}

func TestCreatePaymentWalletFullAmountCreatesPaymentRecord(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	user := &userdomain.User{
		Email:        "wallet_pay_user@example.com",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTWALLETPAY001",
		UserID:                  user.ID,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(50)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(50)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(50)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	account := &walletdomain.Account{
		UserID:    user.ID,
		Balance:   money.FromDecimal(decimal.NewFromInt(100)),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(account).Error; err != nil {
		t.Fatalf("create wallet account failed: %v", err)
	}

	result, err := svc.CreatePayment(CreatePaymentInput{
		OrderID:    order.ID,
		ChannelID:  0,
		UseBalance: true,
	})
	if err != nil {
		t.Fatalf("create payment failed: %v", err)
	}
	if !result.OrderPaid {
		t.Fatalf("expected order_paid=true")
	}
	if result.Payment != nil {
		t.Fatalf("expected response payment to be nil for wallet full payment")
	}
	if !result.WalletPaidAmount.Decimal.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("wallet_paid_amount want 50 got %s", result.WalletPaidAmount.String())
	}
	if !result.OnlinePayAmount.Decimal.Equal(decimal.Zero) {
		t.Fatalf("online_pay_amount want 0 got %s", result.OnlinePayAmount.String())
	}

	var payment paymentdomain.Payment
	if err := db.Where("order_id = ?", order.ID).First(&payment).Error; err != nil {
		t.Fatalf("wallet payment record not found: %v", err)
	}
	if payment.ProviderType != constants.PaymentProviderWallet {
		t.Fatalf("provider_type want %s got %s", constants.PaymentProviderWallet, payment.ProviderType)
	}
	if payment.ChannelType != constants.PaymentChannelTypeBalance {
		t.Fatalf("channel_type want %s got %s", constants.PaymentChannelTypeBalance, payment.ChannelType)
	}
	if payment.InteractionMode != constants.PaymentInteractionBalance {
		t.Fatalf("interaction_mode want %s got %s", constants.PaymentInteractionBalance, payment.InteractionMode)
	}
	if payment.ChannelID != 0 {
		t.Fatalf("channel_id want 0 got %d", payment.ChannelID)
	}
	if payment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, payment.Status)
	}
	if !payment.Amount.Decimal.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("payment amount want 50 got %s", payment.Amount.String())
	}
	if payment.PaidAt == nil {
		t.Fatalf("wallet payment should set paid_at")
	}

	var refreshedOrder orderdomain.Order
	if err := db.First(&refreshedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	if refreshedOrder.Status != constants.OrderStatusPaid {
		t.Fatalf("order status want %s got %s", constants.OrderStatusPaid, refreshedOrder.Status)
	}
	if refreshedOrder.PaidAt == nil {
		t.Fatalf("order should set paid_at")
	}

	var refreshedAccount walletdomain.Account
	if err := db.Where("user_id = ?", user.ID).First(&refreshedAccount).Error; err != nil {
		t.Fatalf("reload wallet account failed: %v", err)
	}
	if !refreshedAccount.Balance.Decimal.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("wallet balance want 50 got %s", refreshedAccount.Balance.String())
	}
}

func TestExpireWalletRechargePaymentPendingToExpired(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected updated payment")
	}
	if updated.Status != constants.PaymentStatusExpired {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusExpired, updated.Status)
	}
	if updated.ExpiredAt == nil {
		t.Fatalf("expected payment expired_at set")
	}

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusExpired {
		t.Fatalf("reloaded payment status want %s got %s", constants.PaymentStatusExpired, refreshedPayment.Status)
	}
	if refreshedPayment.ExpiredAt == nil {
		t.Fatalf("reloaded payment expected expired_at set")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusExpired {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusExpired, refreshedRecharge.Status)
	}
}

func TestExpireWalletRechargePaymentDoesNotOverrideSuccess(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusSuccess, constants.WalletRechargeStatusSuccess)

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected updated payment")
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("reloaded payment status want %s got %s", constants.PaymentStatusSuccess, refreshedPayment.Status)
	}
	if refreshedPayment.PaidAt == nil {
		t.Fatalf("success payment should keep paid_at")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusSuccess, refreshedRecharge.Status)
	}
}

func TestExpireWalletRechargePaymentSkipsOrderPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	payment := &paymentdomain.Payment{
		OrderID:         99,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(100)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected payment result")
	}
	if updated.Status != constants.PaymentStatusPending {
		t.Fatalf("order payment should remain pending, got %s", updated.Status)
	}
}

func TestWalletRechargeCallbackSuccessAfterExpireCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.ExpireWalletRechargePayment(payment.ID); err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}

	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-1"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
}

func TestWalletRechargeCallbackSuccessThenExpireKeepsSuccess(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-2")); err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}

	updated, err := svc.ExpireWalletRechargePayment(payment.ID)
	if err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
}

func TestWalletRechargeCallbackDuplicateSuccessDoesNotDuplicateCredit(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-FIRST")); err != nil {
		t.Fatalf("handle first wallet recharge callback failed: %v", err)
	}
	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-SECOND")); err != nil {
		t.Fatalf("handle second wallet recharge callback failed: %v", err)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)

	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var txnCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&txnCount).Error; err != nil {
		t.Fatalf("count wallet transaction failed: %v", err)
	}
	if txnCount != 1 {
		t.Fatalf("wallet recharge success transaction count want 1 got %d", txnCount)
	}
}

func TestWalletRechargeCallbackConcurrentSuccessCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	const callbacks = 2
	var wait sync.WaitGroup
	errorsCh := make(chan error, callbacks)
	for index := 0; index < callbacks; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			input := buildWalletRechargeCallbackInput(
				payment,
				recharge,
				constants.PaymentStatusSuccess,
				fmt.Sprintf("CALLBACK-CONCURRENT-%d", index),
			)
			if _, callbackErr := svc.HandleCallback(input); callbackErr != nil {
				errorsCh <- callbackErr
			}
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for callbackErr := range errorsCh {
		t.Errorf("concurrent callback failed: %v", callbackErr)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var transactionCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&transactionCount).Error; err != nil {
		t.Fatalf("count wallet transaction: %v", err)
	}
	if transactionCount != 1 {
		t.Fatalf("concurrent success callbacks created %d credits, want 1", transactionCount)
	}
}

func TestWalletRechargeCallbackPendingAfterExpireDoesNotReopen(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	if _, err := svc.ExpireWalletRechargePayment(payment.ID); err != nil {
		t.Fatalf("expire wallet recharge payment failed: %v", err)
	}
	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusPending, "CALLBACK-PENDING-LATE"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusExpired {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusExpired, updated.Status)
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusExpired {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusExpired, refreshedRecharge.Status)
	}
}

func TestWalletRechargeCallbackTerminalStateMatrixDoesNotReopen(t *testing.T) {
	cases := []struct {
		name              string
		paymentStatus     string
		rechargeStatus    string
		callbackStatus    string
		wantPaymentStatus string
		wantRechargeState string
	}{
		{
			name:              "expired_to_pending",
			paymentStatus:     constants.PaymentStatusExpired,
			rechargeStatus:    constants.WalletRechargeStatusExpired,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusExpired,
			wantRechargeState: constants.WalletRechargeStatusExpired,
		},
		{
			name:              "failed_to_pending",
			paymentStatus:     constants.PaymentStatusFailed,
			rechargeStatus:    constants.WalletRechargeStatusFailed,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusFailed,
			wantRechargeState: constants.WalletRechargeStatusFailed,
		},
		{
			name:              "failed_to_expired",
			paymentStatus:     constants.PaymentStatusFailed,
			rechargeStatus:    constants.WalletRechargeStatusFailed,
			callbackStatus:    constants.PaymentStatusExpired,
			wantPaymentStatus: constants.PaymentStatusFailed,
			wantRechargeState: constants.WalletRechargeStatusFailed,
		},
		{
			name:              "expired_to_failed",
			paymentStatus:     constants.PaymentStatusExpired,
			rechargeStatus:    constants.WalletRechargeStatusExpired,
			callbackStatus:    constants.PaymentStatusFailed,
			wantPaymentStatus: constants.PaymentStatusExpired,
			wantRechargeState: constants.WalletRechargeStatusExpired,
		},
		{
			name:              "success_to_pending",
			paymentStatus:     constants.PaymentStatusSuccess,
			rechargeStatus:    constants.WalletRechargeStatusSuccess,
			callbackStatus:    constants.PaymentStatusPending,
			wantPaymentStatus: constants.PaymentStatusSuccess,
			wantRechargeState: constants.WalletRechargeStatusSuccess,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			payment, recharge := createWalletRechargeFixture(t, db, tc.paymentStatus, tc.rechargeStatus)

			providerRef := fmt.Sprintf("CALLBACK-%s-%d", tc.name, time.Now().UnixNano())
			updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, tc.callbackStatus, providerRef))
			if err != nil {
				t.Fatalf("handle wallet recharge callback failed: %v", err)
			}
			if updated.Status != tc.wantPaymentStatus {
				t.Fatalf("payment status want %s got %s", tc.wantPaymentStatus, updated.Status)
			}

			var refreshedPayment paymentdomain.Payment
			if err := db.First(&refreshedPayment, payment.ID).Error; err != nil {
				t.Fatalf("reload payment failed: %v", err)
			}
			if refreshedPayment.Status != tc.wantPaymentStatus {
				t.Fatalf("reloaded payment status want %s got %s", tc.wantPaymentStatus, refreshedPayment.Status)
			}

			var refreshedRecharge walletdomain.RechargeOrder
			if err := db.First(&refreshedRecharge, recharge.ID).Error; err != nil {
				t.Fatalf("reload recharge failed: %v", err)
			}
			if refreshedRecharge.Status != tc.wantRechargeState {
				t.Fatalf("recharge status want %s got %s", tc.wantRechargeState, refreshedRecharge.Status)
			}
		})
	}
}

func TestWalletRechargeCallbackSuccessAfterFailedCreditsOnce(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusFailed, constants.WalletRechargeStatusFailed)

	updated, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-SUCCESS-AFTER-FAILED"))
	if err != nil {
		t.Fatalf("handle wallet recharge callback failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, updated.Status)
	}

	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)

	reference := fmt.Sprintf("recharge:%d:success", recharge.ID)
	var txnCount int64
	if err := db.Model(&walletdomain.Transaction{}).Where("reference = ?", reference).Count(&txnCount).Error; err != nil {
		t.Fatalf("count wallet transaction failed: %v", err)
	}
	if txnCount != 1 {
		t.Fatalf("wallet recharge success transaction count want 1 got %d", txnCount)
	}
}

type failWalletRechargeReloadRepository struct {
	walletcontract.Repository
	getCalls int
}

func (r *failWalletRechargeReloadRepository) GetRechargeOrderByPaymentID(paymentID uint) (*walletdomain.RechargeOrder, error) {
	r.getCalls++
	if r.getCalls > 1 {
		return nil, fmt.Errorf("forced recharge reload failure")
	}
	return r.Repository.GetRechargeOrderByPaymentID(paymentID)
}

type recordingMemberLevelProgressor struct {
	rechargeUserID uint
	rechargeAmount decimal.Decimal
}

func (r *recordingMemberLevelProgressor) OnOrderPaid(uint, decimal.Decimal) error {
	return nil
}

func (r *recordingMemberLevelProgressor) OnRechargeCompleted(userID uint, amount decimal.Decimal) error {
	r.rechargeUserID = userID
	r.rechargeAmount = amount
	return nil
}

func TestWalletRechargeCallbackReloadFailureKeepsNotificationContext(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	svc.walletRepo = &failWalletRechargeReloadRepository{Repository: svc.walletRepo}
	progressor := &recordingMemberLevelProgressor{}
	svc.SetMemberLevelService(progressor)

	if _, err := svc.HandleCallback(buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusSuccess, "CALLBACK-RELOAD-FAILURE")); err != nil {
		t.Fatalf("HandleCallback failed: %v", err)
	}
	if progressor.rechargeUserID != recharge.UserID {
		t.Fatalf("fallback recharge user id = %d, want %d", progressor.rechargeUserID, recharge.UserID)
	}
	if !progressor.rechargeAmount.Equal(recharge.Amount.Decimal) {
		t.Fatalf("fallback recharge amount = %s, want %s", progressor.rechargeAmount, recharge.Amount.String())
	}
}

func TestHandleDujiaoPayWebhookAppliesVerifiedWalletRecharge(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, payment, recharge := createDujiaoPayWalletWebhookFixture(t, db)

	body := []byte(fmt.Sprintf(
		`{"event_id":"evt-wallet-paid","event_type":"order.paid","event_version":"v1","created_at":"2026-07-27T00:00:00Z","data":{"order_id":%q,"merchant_order_id":%q,"fiat_currency":"CNY","fiat_amount":"88.00","tx_id":"0xwallet","paid_at":"2026-07-27T00:00:01Z"}}`,
		payment.ProviderRef,
		recharge.RechargeNo,
	))
	headers := signDujiaoPayWebhook(body, "test-dujiaopay-webhook-secret")

	updated, status, err := svc.HandleDujiaoPayWebhook(WebhookCallbackInput{
		ChannelID: channel.ID,
		Headers:   headers,
		Body:      body,
		Context:   context.Background(),
	})
	if err != nil {
		t.Fatalf("HandleDujiaoPayWebhook failed: %v", err)
	}
	if status != constants.PaymentStatusSuccess || updated == nil || updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("webhook result status=%q payment=%+v", status, updated)
	}
	assertWalletRechargeSuccessState(t, db, payment.ID, recharge.ID, recharge.UserID, recharge.Amount.Decimal)
}

func TestHandleDujiaoPayWebhookMarksOrderPaid(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel := createDujiaoPayChannelFixture(t, db)
	now := time.Now()
	order := &orderdomain.Order{
		OrderNo:          fmt.Sprintf("DJ-DUJIAOPAY-%d", now.UnixNano()),
		UserID:           1,
		Status:           constants.OrderStatusPendingPayment,
		Currency:         "CNY",
		OriginalAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		TotalAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		OnlinePaidAmount: money.FromDecimal(decimal.NewFromInt(88)),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    constants.PaymentProviderDujiaoPay,
		ChannelType:     channel.ChannelType,
		InteractionMode: constants.PaymentInteractionRedirect,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		GatewayOrderNo:  fmt.Sprintf("PAY-DUJIAOPAY-%d", now.UnixNano()),
		ProviderRef:     fmt.Sprintf("do_order_%d", now.UnixNano()),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	body := []byte(fmt.Sprintf(
		`{"event_id":"evt-order-paid","event_type":"order.paid","event_version":"v1","created_at":"2026-07-27T00:00:00Z","data":{"order_id":%q,"merchant_order_id":%q,"fiat_currency":"CNY","fiat_amount":"88.00","tx_id":"0xorder"}}`,
		payment.ProviderRef,
		payment.GatewayOrderNo,
	))
	updated, status, err := svc.HandleDujiaoPayWebhook(WebhookCallbackInput{
		ChannelID: channel.ID,
		Headers:   signDujiaoPayWebhook(body, "test-dujiaopay-webhook-secret"),
		Body:      body,
		Context:   context.Background(),
	})
	if err != nil {
		t.Fatalf("HandleDujiaoPayWebhook failed: %v", err)
	}
	if status != constants.PaymentStatusSuccess || updated == nil || updated.Status != constants.PaymentStatusSuccess {
		t.Fatalf("webhook result status=%q payment=%+v", status, updated)
	}
	var storedOrder orderdomain.Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	if storedOrder.Status != constants.OrderStatusPaid || storedOrder.PaidAt == nil {
		t.Fatalf("DujiaoPay webhook did not mark order paid: %+v", storedOrder)
	}
}

func TestHandleDujiaoPayWebhookRejectsMismatchedMerchantOrderID(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, payment, recharge := createDujiaoPayWalletWebhookFixture(t, db)

	body := []byte(fmt.Sprintf(
		`{"event_id":"evt-wallet-mismatch","event_type":"order.paid","event_version":"v1","created_at":"2026-07-27T00:00:00Z","data":{"order_id":%q,"merchant_order_id":"WRONG-MERCHANT-ORDER","fiat_currency":"CNY","fiat_amount":"88.00","tx_id":"0xmismatch"}}`,
		payment.ProviderRef,
	))
	headers := signDujiaoPayWebhook(body, "test-dujiaopay-webhook-secret")

	if _, _, err := svc.HandleDujiaoPayWebhook(WebhookCallbackInput{
		ChannelID: channel.ID,
		Headers:   headers,
		Body:      body,
		Context:   context.Background(),
	}); err != ErrPaymentInvalid {
		t.Fatalf("mismatched merchant order error = %v, want ErrPaymentInvalid", err)
	}

	var storedRecharge walletdomain.RechargeOrder
	if err := db.First(&storedRecharge, recharge.ID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if storedRecharge.Status != constants.WalletRechargeStatusPending {
		t.Fatalf("mismatched webhook changed recharge status to %s", storedRecharge.Status)
	}
}

func TestHandleDujiaoPayWebhookAdoptsSignedCurrencyForLegacyPendingPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, payment, recharge := createDujiaoPayWalletWebhookFixture(t, db)
	channel.ConfigJSON["fiat_currency"] = "USD"
	if err := db.Model(channel).Update("config_json", channel.ConfigJSON).Error; err != nil {
		t.Fatalf("update channel fiat currency: %v", err)
	}

	body := []byte(fmt.Sprintf(
		`{"event_id":"evt-wallet-legacy-currency","event_type":"order.paid","event_version":"v1","created_at":"2026-07-27T00:00:00Z","data":{"order_id":%q,"merchant_order_id":%q,"fiat_currency":"USD","fiat_amount":"88.00","tx_id":"0xlegacycurrency"}}`,
		payment.ProviderRef,
		recharge.RechargeNo,
	))
	updated, status, err := svc.HandleDujiaoPayWebhook(WebhookCallbackInput{
		ChannelID: channel.ID,
		Headers:   signDujiaoPayWebhook(body, "test-dujiaopay-webhook-secret"),
		Body:      body,
		Context:   context.Background(),
	})
	if err != nil {
		t.Fatalf("legacy DujiaoPay webhook failed: %v", err)
	}
	if status != constants.PaymentStatusSuccess || updated == nil || updated.Currency != "USD" {
		t.Fatalf("legacy webhook status=%q payment=%+v", status, updated)
	}

	var stored paymentdomain.Payment
	if err := db.First(&stored, payment.ID).Error; err != nil {
		t.Fatalf("reload payment: %v", err)
	}
	if stored.Currency != "USD" {
		t.Fatalf("stored currency = %q, want signed legacy currency USD", stored.Currency)
	}
	if got := stored.ProviderPayload[paymentcontract.GatewayPayloadFiatCurrencySent]; got != "USD" {
		t.Fatalf("stored fiat currency snapshot = %#v, want USD", got)
	}
}

func TestHandleDujiaoPayWebhookKeepsStrictCurrencyCheckForSnapshottedPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel, payment, recharge := createDujiaoPayWalletWebhookFixture(t, db)
	payment.ProviderPayload = jsonmap.JSON{
		paymentcontract.GatewayPayloadFiatCurrencySent: "CNY",
	}
	if err := db.Model(payment).Update("provider_payload", payment.ProviderPayload).Error; err != nil {
		t.Fatalf("store payment fiat currency snapshot: %v", err)
	}

	body := []byte(fmt.Sprintf(
		`{"event_id":"evt-wallet-strict-currency","event_type":"order.paid","event_version":"v1","created_at":"2026-07-27T00:00:00Z","data":{"order_id":%q,"merchant_order_id":%q,"fiat_currency":"USD","fiat_amount":"88.00","tx_id":"0xstrictcurrency"}}`,
		payment.ProviderRef,
		recharge.RechargeNo,
	))
	if _, _, err := svc.HandleDujiaoPayWebhook(WebhookCallbackInput{
		ChannelID: channel.ID,
		Headers:   signDujiaoPayWebhook(body, "test-dujiaopay-webhook-secret"),
		Body:      body,
		Context:   context.Background(),
	}); err != ErrPaymentCurrencyMismatch {
		t.Fatalf("snapshotted currency mismatch error = %v, want ErrPaymentCurrencyMismatch", err)
	}

	var stored paymentdomain.Payment
	if err := db.First(&stored, payment.ID).Error; err != nil {
		t.Fatalf("reload payment: %v", err)
	}
	if stored.Status != constants.PaymentStatusPending || stored.Currency != "CNY" {
		t.Fatalf("strict mismatch changed payment: %+v", stored)
	}
}

func createDujiaoPayWalletWebhookFixture(t *testing.T, db *gorm.DB) (*paymentdomain.PaymentChannel, *paymentdomain.Payment, *walletdomain.RechargeOrder) {
	t.Helper()
	channel := createDujiaoPayChannelFixture(t, db)
	now := time.Now()
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	providerRef := fmt.Sprintf("do_wallet_%d", now.UnixNano())
	if err := db.Model(payment).Updates(map[string]interface{}{
		"channel_id":       channel.ID,
		"provider_type":    constants.PaymentProviderDujiaoPay,
		"channel_type":     channel.ChannelType,
		"interaction_mode": constants.PaymentInteractionRedirect,
		"gateway_order_no": recharge.RechargeNo,
		"provider_ref":     providerRef,
	}).Error; err != nil {
		t.Fatalf("update DujiaoPay payment fixture failed: %v", err)
	}
	payment.ChannelID = channel.ID
	payment.ProviderType = constants.PaymentProviderDujiaoPay
	payment.ChannelType = channel.ChannelType
	payment.InteractionMode = constants.PaymentInteractionRedirect
	payment.GatewayOrderNo = recharge.RechargeNo
	payment.ProviderRef = providerRef
	return channel, payment, recharge
}

func createDujiaoPayChannelFixture(t *testing.T, db *gorm.DB) *paymentdomain.PaymentChannel {
	t.Helper()
	now := time.Now()
	channel := &paymentdomain.PaymentChannel{
		Name:            "DujiaoPay test",
		ProviderType:    constants.PaymentProviderDujiaoPay,
		ChannelType:     "tron-usdt",
		InteractionMode: constants.PaymentInteractionRedirect,
		ConfigJSON: jsonmap.JSON{
			"api_base_url":   "https://pay.example.test",
			"api_key_id":     "test-key",
			"api_secret":     "test-api-secret",
			"webhook_secret": "test-dujiaopay-webhook-secret",
			"order_mode":     constants.PaymentDujiaoPayOrderModeTransaction,
			"chain":          "tron",
			"token_id":       "tron-usdt",
			"fiat_currency":  "CNY",
		},
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create DujiaoPay channel failed: %v", err)
	}
	return channel
}

func signDujiaoPayWebhook(body []byte, secret string) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return map[string]string{
		"DJP-Webhook-Timestamp": timestamp,
		"DJP-Webhook-Signature": hex.EncodeToString(mac.Sum(nil)),
	}
}

func createWalletRechargeFixture(t *testing.T, db *gorm.DB, paymentStatus string, rechargeStatus string) (*paymentdomain.Payment, *walletdomain.RechargeOrder) {
	t.Helper()
	now := time.Now()
	payment := &paymentdomain.Payment{
		OrderID:         0,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          paymentStatus,
		ProviderRef:     fmt.Sprintf("RECHARGE-PAY-%d", now.UnixNano()),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if paymentStatus == constants.PaymentStatusSuccess {
		payment.PaidAt = &now
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	recharge := &walletdomain.RechargeOrder{
		RechargeNo:      fmt.Sprintf("WRTEST%d", now.UnixNano()),
		UserID:          1,
		PaymentID:       payment.ID,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		PayableAmount:   money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          rechargeStatus,
		Remark:          "test",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if rechargeStatus == constants.WalletRechargeStatusSuccess {
		recharge.PaidAt = &now
	}
	if err := db.Create(recharge).Error; err != nil {
		t.Fatalf("create recharge failed: %v", err)
	}
	return payment, recharge
}

func buildWalletRechargeCallbackInput(payment *paymentdomain.Payment, recharge *walletdomain.RechargeOrder, status string, providerRef string) PaymentCallbackInput {
	return PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     recharge.RechargeNo,
		ChannelID:   payment.ChannelID,
		Status:      status,
		ProviderRef: providerRef,
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      ptrTime(time.Now()),
		Payload: jsonmap.JSON{
			"provider_ref": providerRef,
			"status":       status,
		},
	}
}

func assertWalletRechargeSuccessState(t *testing.T, db *gorm.DB, paymentID uint, rechargeID uint, userID uint, rechargeAmount decimal.Decimal) {
	t.Helper()

	var refreshedPayment paymentdomain.Payment
	if err := db.First(&refreshedPayment, paymentID).Error; err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if refreshedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status want %s got %s", constants.PaymentStatusSuccess, refreshedPayment.Status)
	}
	if refreshedPayment.PaidAt == nil {
		t.Fatalf("payment should set paid_at")
	}

	var refreshedRecharge walletdomain.RechargeOrder
	if err := db.First(&refreshedRecharge, rechargeID).Error; err != nil {
		t.Fatalf("reload recharge failed: %v", err)
	}
	if refreshedRecharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatalf("recharge status want %s got %s", constants.WalletRechargeStatusSuccess, refreshedRecharge.Status)
	}
	if refreshedRecharge.PaidAt == nil {
		t.Fatalf("recharge should set paid_at")
	}

	var account walletdomain.Account
	if err := db.Where("user_id = ?", userID).First(&account).Error; err != nil {
		t.Fatalf("reload wallet account failed: %v", err)
	}
	if !account.Balance.Decimal.Equal(rechargeAmount) {
		t.Fatalf("wallet balance want %s got %s", rechargeAmount.String(), account.Balance.String())
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func TestConfirmManualPaymentMarksPaymentAndOrderPaid(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()
	order := &orderdomain.Order{
		OrderNo:        "DJMANUALCONFIRM001",
		Status:         constants.OrderStatusPendingPayment,
		Currency:       "CNY",
		OriginalAmount: money.FromDecimal(decimal.NewFromInt(100)),
		TotalAmount:    money.FromDecimal(decimal.NewFromInt(100)),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       1,
		ProviderType:    manualQRProviderType,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		Amount:          money.FromDecimal(decimal.NewFromInt(100)),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	updated, err := svc.ConfirmManualPayment(payment.ID)
	if err != nil {
		t.Fatalf("confirm manual payment failed: %v", err)
	}
	if updated.Status != constants.PaymentStatusSuccess || updated.PaidAt == nil {
		t.Fatalf("payment not marked successful: %#v", updated)
	}

	var refreshedOrder orderdomain.Order
	if err := db.First(&refreshedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	if refreshedOrder.Status != constants.OrderStatusPaid || refreshedOrder.PaidAt == nil {
		t.Fatalf("order not marked paid: %#v", refreshedOrder)
	}
}

func TestConfirmManualPaymentRejectsNonManualProvider(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment := &paymentdomain.Payment{
		ProviderType: constants.PaymentProviderOfficial,
		Amount:       money.FromDecimal(decimal.NewFromInt(100)),
		Currency:     "CNY",
		Status:       constants.PaymentStatusPending,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if _, err := svc.ConfirmManualPayment(payment.ID); !errors.Is(err, ErrPaymentProviderNotSupported) {
		t.Fatalf("expected provider not supported, got %v", err)
	}
}
