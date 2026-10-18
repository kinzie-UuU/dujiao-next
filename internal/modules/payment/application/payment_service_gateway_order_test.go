package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/jsonslice"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

func TestValidateOrderChannelEligibilityUsesPersistedOrderIdentity(t *testing.T) {
	levelID := uint(2)
	channel := paymentdomain.PaymentChannel{
		PaymentRoles: jsonslice.Strings{constants.PaymentRoleMember},
		MemberLevels: jsonslice.Uints{levelID},
		PaymentTypes: jsonslice.Strings{constants.PaymentTypeOrder},
	}
	memberOrder := &orderdomain.Order{UserID: 10, MemberLevelID: &levelID}
	if err := validateOrderChannelEligibility(channel, memberOrder); err != nil {
		t.Fatalf("eligible persisted member order rejected: %v", err)
	}
	if err := validateOrderChannelEligibility(channel, &orderdomain.Order{}); err != ErrPaymentChannelNotAllowedForProduct {
		t.Fatalf("guest bypass error = %v, want channel rejection", err)
	}
	wrongLevel := uint(3)
	if err := validateOrderChannelEligibility(channel, &orderdomain.Order{UserID: 10, MemberLevelID: &wrongLevel}); err != ErrPaymentChannelNotAllowedForProduct {
		t.Fatalf("member-level bypass error = %v, want channel rejection", err)
	}
}

func TestValidateWalletChannelEligibilityRejectsOrderOnlyChannel(t *testing.T) {
	channel := paymentdomain.PaymentChannel{
		PaymentRoles: jsonslice.Strings{constants.PaymentRoleMember},
		PaymentTypes: jsonslice.Strings{constants.PaymentTypeOrder},
	}
	user := &userdomain.User{ID: 10}
	if err := validateWalletChannelEligibility(channel, user); err != ErrPaymentChannelNotAllowedForRecharge {
		t.Fatalf("wallet payment-type bypass error = %v, want recharge rejection", err)
	}
}

type emptyProviderRefProvider struct {
	onCreate           func(paymentcontract.GatewayCreateInput)
	displayChannelType string
}

func registerTestGateway(t *testing.T, svc *PaymentService, providerType, channelType string, gateway paymentcontract.GatewayProvider) {
	t.Helper()
	registry, ok := svc.paymentProviderRegistry.(interface {
		Register(string, string, paymentcontract.GatewayProvider)
	})
	if !ok {
		t.Fatal("payment provider registry does not support test registration")
	}
	registry.Register(providerType, channelType, gateway)
}

func (p emptyProviderRefProvider) Type() string {
	return constants.PaymentProviderOfficial + ":" + constants.PaymentChannelTypeWechat
}

func (p emptyProviderRefProvider) ValidateConfig(jsonmap.JSON, string) error {
	return nil
}

func (p emptyProviderRefProvider) CreatePayment(_ context.Context, _ jsonmap.JSON, input paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	if p.onCreate != nil {
		p.onCreate(input)
	}
	return &paymentcontract.GatewayCreateResult{
		QRCodeURL:          "weixin://wxpay/bizpayurl?pr=test",
		DisplayChannelType: p.displayChannelType,
	}, nil
}

func TestShouldUseGatewayOrderNo(t *testing.T) {
	tests := []struct {
		name    string
		channel *paymentdomain.PaymentChannel
		want    bool
	}{
		{
			name: "epay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderEpay,
			},
			want: true,
		},
		{
			name: "epusdt",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderBepusdt,
			},
			want: true,
		},
		{
			name: "okpay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderOkpay,
			},
			want: true,
		},
		{
			name: "tokenpay",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderTokenpay,
			},
			want: true,
		},
		{
			name: "official wechat",
			channel: &paymentdomain.PaymentChannel{
				ProviderType: constants.PaymentProviderOfficial,
				ChannelType:  constants.PaymentChannelTypeWechat,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldUseGatewayOrderNo(tc.channel); got != tc.want {
				t.Fatalf("shouldUseGatewayOrderNo() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveGatewayOrderNo(t *testing.T) {
	channel := &paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderBepusdt}
	payment := &paymentdomain.Payment{ID: 123}

	gotFirst := resolveGatewayOrderNo(channel, payment)
	if gotFirst == "" {
		t.Fatalf("resolveGatewayOrderNo() should generate gateway order no")
	}
	if gotFirst == "DJP123" {
		t.Fatalf("resolveGatewayOrderNo() should not leak payment id, got %s", gotFirst)
	}
	if !strings.HasPrefix(gotFirst, "DJP") {
		t.Fatalf("resolveGatewayOrderNo() should keep DJP prefix, got %s", gotFirst)
	}

	payment.GatewayOrderNo = "CUSTOM-1"
	if got := resolveGatewayOrderNo(channel, payment); got != "CUSTOM-1" {
		t.Fatalf("resolveGatewayOrderNo() should reuse stored value, got %s", got)
	}
	payment.GatewayOrderNo = ""

	gotFallback := resolveGatewayOrderNo(channel, payment)
	if gotFallback == "" {
		t.Fatalf("resolveGatewayOrderNo() fallback should not be empty")
	}
	if gotFallback == "DJP123" {
		t.Fatalf("resolveGatewayOrderNo() fallback should not leak payment id, got %s", gotFallback)
	}
	if !strings.HasPrefix(gotFallback, "DJP") {
		t.Fatalf("resolveGatewayOrderNo() fallback should keep DJP prefix, got %s", gotFallback)
	}
	if gotFallback == gotFirst {
		t.Fatalf("resolveGatewayOrderNo() should generate new gateway order no for a new payment attempt")
	}

	official := &paymentdomain.PaymentChannel{ProviderType: constants.PaymentProviderOfficial}
	if got := resolveGatewayOrderNo(official, payment); got == "" {
		t.Fatalf("official provider should also use gateway order no")
	}
}

func TestApplyProviderPaymentFallsBackToWechatGatewayOrderNoWhenProviderRefEmpty(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTWECHAT001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(1)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(1)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(1)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("1.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	var providerInputOrderNo string
	registerTestGateway(t, svc, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, emptyProviderRefProvider{
		onCreate: func(input paymentcontract.GatewayCreateInput) {
			providerInputOrderNo = strings.TrimSpace(input.OrderNo)
		},
	})

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if !strings.HasPrefix(payment.GatewayOrderNo, "DJP") {
		t.Fatalf("payment gateway order no should keep DJP prefix, got %s", payment.GatewayOrderNo)
	}
	if providerInputOrderNo != payment.GatewayOrderNo {
		t.Fatalf("wechat create order no = %s, want %s", providerInputOrderNo, payment.GatewayOrderNo)
	}
	if payment.ProviderRef != payment.GatewayOrderNo {
		t.Fatalf("provider ref fallback = %s, want gateway order no %s", payment.ProviderRef, payment.GatewayOrderNo)
	}
	if payment.ProviderRef == order.OrderNo {
		t.Fatalf("provider ref should not fall back to business order no")
	}
}

func TestApplyProviderPaymentStoresDisplayChannelType(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTDISPLAY001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOfficial,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("10.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	registerTestGateway(t, svc, constants.PaymentProviderOfficial, constants.PaymentChannelTypeWechat, emptyProviderRefProvider{
		displayChannelType: "usdt.arbitrum",
	})

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.ProviderPayload == nil {
		t.Fatalf("provider payload should be recorded")
	}
	if got := payment.ProviderPayload["display_channel_type"]; got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type = %v, want usdt.arbitrum", got)
	}
}

func TestPaymentDisplayChannelTypeLifecycle(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTDISPLAYLIFECYCLE001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		Name:            "BEpusdt Arbitrum",
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentProviderBepusdt,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"notify_url": "https://api.example.com/api/v1/payments/callback",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.NewFromInt(10)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	registerTestGateway(t, svc, constants.PaymentProviderBepusdt, "", emptyProviderRefProvider{
		displayChannelType: "usdt.arbitrum",
	})
	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("create provider payment failed: %v", err)
	}
	if got := notificationPayloadString(payment.ProviderPayload, "display_channel_type"); got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type after create = %q, want usdt.arbitrum", got)
	}

	paidAt := time.Now()
	if _, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     payment.GatewayOrderNo,
		ChannelID:   channel.ID,
		Status:      constants.PaymentStatusSuccess,
		ProviderRef: "BEP-CALLBACK-1",
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      &paidAt,
		Payload: jsonmap.JSON{
			"trade_id": "BEP-CALLBACK-1",
			"status":   float64(2),
		},
	}); err != nil {
		t.Fatalf("handle callback failed: %v", err)
	}

	storedPayment, err := svc.GetPayment(payment.ID)
	if err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if got := notificationPayloadString(storedPayment.ProviderPayload, "display_channel_type"); got != "usdt.arbitrum" {
		t.Fatalf("display_channel_type after callback reload = %q, want usdt.arbitrum", got)
	}
	if got := notificationPayloadString(storedPayment.ProviderPayload, "trade_id"); got != "BEP-CALLBACK-1" {
		t.Fatalf("callback trade_id after reload = %q, want BEP-CALLBACK-1", got)
	}

	notificationPayload := svc.buildOrderNotificationPayload(order, storedPayment)
	if got := notificationPayloadString(notificationPayload, "payment_channel"); got != "bepusdt/usdt.arbitrum" {
		t.Fatalf("notification payment_channel = %q, want bepusdt/usdt.arbitrum", got)
	}

	adminPayments, _, err := svc.ListPayments(paymentcontract.ListFilter{
		Page:        1,
		PageSize:    10,
		OrderID:     order.ID,
		Lightweight: true,
	})
	if err != nil {
		t.Fatalf("list admin payments failed: %v", err)
	}
	if len(adminPayments) != 1 {
		t.Fatalf("admin payment count = %d, want 1", len(adminPayments))
	}
	if got := adminPayments[0].DisplayChannelType; got != "usdt.arbitrum" {
		t.Fatalf("admin display_channel_type = %q, want usdt.arbitrum", got)
	}
}

func TestApplyProviderPaymentUsesGatewayOrderNoForBepusdt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTGATEWAY001",
		UserID:                  1,
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

	var gotOrderID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request failed: %v", err)
		}
		gotOrderID = strings.TrimSpace(payload["order_id"].(string))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":200,"message":"ok","data":{"trade_id":"TRX-1001","order_id":"` + gotOrderID + `","amount":"50.00","actual_amount":"7.00","token":"USDT","expiration_time":1800,"payment_url":"https://pay.example.com/checkout"}}`))
	}))
	defer server.Close()

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentChannelTypeUsdtTrc20,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url": server.URL,
			"auth_token":  "token-001",
			"trade_type":  "usdt.trc20",
			"fiat":        "CNY",
			"notify_url":  "https://example.com/callback",
			"return_url":  "https://example.com/pay-return",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("50.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if payment.GatewayOrderNo == order.OrderNo {
		t.Fatalf("payment gateway order no should differ from business order no, got %s", payment.GatewayOrderNo)
	}
	if !strings.HasPrefix(payment.GatewayOrderNo, "DJP") {
		t.Fatalf("payment gateway order no should keep DJP prefix, got %s", payment.GatewayOrderNo)
	}
	if gotOrderID != payment.GatewayOrderNo {
		t.Fatalf("epusdt order_id = %s, want %s", gotOrderID, payment.GatewayOrderNo)
	}
	if payment.ProviderRef != "TRX-1001" {
		t.Fatalf("provider ref = %s, want TRX-1001", payment.ProviderRef)
	}
}

func TestApplyProviderPaymentUsesGatewayOrderNoForOkpay(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTOKPAY001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	var gotUniqueID string
	var gotAmount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form failed: %v", err)
		}
		gotUniqueID = strings.TrimSpace(r.PostForm.Get("unique_id"))
		gotAmount = strings.TrimSpace(r.PostForm.Get("amount"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","code":200,"data":{"order_id":"OKPAY-ORDER-1001","pay_url":"https://pay.example.com/okpay"}}`))
	}))
	defer server.Close()

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderOkpay,
		ChannelType:     constants.PaymentChannelTypeUsdt,
		InteractionMode: constants.PaymentInteractionQR,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":    server.URL,
			"merchant_id":    "shop-1001",
			"merchant_token": "token-1001",
			"return_url":     "https://example.com/pay",
			"callback_url":   "https://api.example.com/api/v1/payments/callback",
			"exchange_rate":  "7",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("88.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment failed: %v", err)
	}

	if payment.GatewayOrderNo == "" {
		t.Fatalf("payment gateway order no should not be empty")
	}
	if gotUniqueID != payment.GatewayOrderNo {
		t.Fatalf("okpay unique_id = %s, want %s", gotUniqueID, payment.GatewayOrderNo)
	}
	if gotAmount != "616.00000000" {
		t.Fatalf("okpay amount = %s, want 616.00000000", gotAmount)
	}
	if payment.ProviderRef != "OKPAY-ORDER-1001" {
		t.Fatalf("provider ref = %s, want OKPAY-ORDER-1001", payment.ProviderRef)
	}
	if payment.PayURL != "https://pay.example.com/okpay" {
		t.Fatalf("unexpected pay url: %s", payment.PayURL)
	}
	if payment.QRCode != "https://pay.example.com/okpay" {
		t.Fatalf("unexpected qr code: %s", payment.QRCode)
	}
}

func TestApplyProviderPaymentBuildsRedirectURLForEpay(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTEPAYREDIRECT001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(20)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(20)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(20)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeAlipay,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          money.FromDecimal(decimal.RequireFromString("20.00")),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusInitiated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	if err := svc.applyProviderPayment(CreatePaymentInput{
		ClientIP: "127.0.0.1",
		Context:  context.Background(),
	}, order, channel, payment); err != nil {
		t.Fatalf("applyProviderPayment redirect failed: %v", err)
	}

	if payment.PayURL == "" {
		t.Fatalf("pay url should not be empty")
	}
	if !strings.HasPrefix(payment.PayURL, "https://gateway.example.com/submit.php?") {
		t.Fatalf("unexpected pay url: %s", payment.PayURL)
	}
	if payment.QRCode != "" {
		t.Fatalf("qr code should stay empty in redirect mode")
	}
	if payment.ProviderPayload == nil {
		t.Fatalf("provider payload should be recorded")
	}
}

func TestValidateChannelRejectsInvalidEpayInteractionMode(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionPage,
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
	}
	if err := svc.ValidateChannel(channel); err == nil {
		t.Fatalf("ValidateChannel should reject invalid epay interaction mode")
	}
}

func TestValidateChannelRejectsBepusdtCashierQR(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentProviderBepusdt,
		InteractionMode: constants.PaymentInteractionQR,
		ConfigJSON: jsonmap.JSON{
			"gateway_url": "https://bepusdt.example.com",
			"auth_token":  "token-001",
			"order_mode":  constants.PaymentBepusdtOrderModeCashier,
			"notify_url":  "https://api.example.com/api/v1/payments/callback",
			"return_url":  "https://shop.example.com/pay",
		},
	}
	if err := svc.ValidateChannel(channel); err == nil {
		t.Fatal("ValidateChannel should reject bepusdt cashier + qr")
	}
}

func TestValidateChannelAllowsUnlimitedMaxAmount(t *testing.T) {
	svc, _ := setupPaymentServiceWalletTest(t)
	channel := &paymentdomain.PaymentChannel{
		ProviderType:    constants.PaymentProviderEpay,
		ChannelType:     constants.PaymentChannelTypeWechat,
		InteractionMode: constants.PaymentInteractionRedirect,
		MinAmount:       money.FromDecimal(decimal.RequireFromString("10.00")),
		MaxAmount:       money.FromDecimal(decimal.Zero),
		ConfigJSON: jsonmap.JSON{
			"gateway_url":  "https://gateway.example.com",
			"epay_version": "v1",
			"merchant_id":  "1001",
			"merchant_key": "key-001",
			"notify_url":   "https://api.example.com/api/v1/payments/callback",
			"return_url":   "https://shop.example.com/pay",
		},
	}

	if err := svc.ValidateChannel(channel); err != nil {
		t.Fatalf("ValidateChannel should allow max_amount=0 (unlimited): %v", err)
	}
}

func TestHandleCallbackAcceptsGatewayOrderNoForOrderPayment(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "DJTESTCALLBACK001",
		UserID:                  1,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(88)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(88)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(88)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	payment := &paymentdomain.Payment{
		OrderID:         order.ID,
		ChannelID:       1,
		ProviderType:    constants.PaymentProviderBepusdt,
		ChannelType:     constants.PaymentChannelTypeUsdtTrc20,
		InteractionMode: constants.PaymentInteractionRedirect,
		Amount:          money.FromDecimal(decimal.NewFromInt(88)),
		FeeRate:         money.FromDecimal(decimal.Zero),
		FeeAmount:       money.FromDecimal(decimal.Zero),
		Currency:        "CNY",
		Status:          constants.PaymentStatusPending,
		GatewayOrderNo:  "DJP501",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	updated, err := svc.HandleCallback(PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     payment.GatewayOrderNo,
		ChannelID:   payment.ChannelID,
		Status:      constants.PaymentStatusPending,
		ProviderRef: "TRX-PENDING-1",
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      ptrTime(time.Now()),
	})
	if err != nil {
		t.Fatalf("HandleCallback should accept gateway order no, got: %v", err)
	}
	if updated == nil || updated.ID != payment.ID {
		t.Fatalf("expected updated payment")
	}
}

func TestHandleCallbackAcceptsGatewayOrderNoForWalletRecharge(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)

	payment.GatewayOrderNo = "DJP8801"
	if err := db.Save(payment).Error; err != nil {
		t.Fatalf("save payment gateway order no failed: %v", err)
	}

	input := buildWalletRechargeCallbackInput(payment, recharge, constants.PaymentStatusPending, "CALLBACK-PENDING-GATEWAY")
	input.OrderNo = payment.GatewayOrderNo

	updated, err := svc.HandleCallback(input)
	if err != nil {
		t.Fatalf("HandleCallback should accept recharge gateway order no, got: %v", err)
	}
	if updated == nil || updated.ID != payment.ID {
		t.Fatalf("expected updated payment")
	}
}

func manualUSDTChannel() *paymentdomain.PaymentChannel {
	return &paymentdomain.PaymentChannel{ID: 1, ProviderType: "manual_qr", ChannelType: "binance_usdt", InteractionMode: "qr", IsActive: true, ConfigJSON: jsonmap.JSON{"usdt_fixed": map[string]interface{}{"rate": "6.70", "network": "BSC", "address": "0x" + strings.Repeat("1", 40), "quote_ttl_minutes": 30}}}
}

func TestManualUSDTValidation(t *testing.T) {
	svc := &PaymentService{}
	if err := svc.ValidateChannel(manualUSDTChannel()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*paymentdomain.PaymentChannel){
		func(c *paymentdomain.PaymentChannel) { c.ChannelType = "alipay" },
		func(c *paymentdomain.PaymentChannel) { c.InteractionMode = "redirect" },
		func(c *paymentdomain.PaymentChannel) { c.ConfigJSON["usdt_fixed"] = map[string]interface{}{} },
		func(c *paymentdomain.PaymentChannel) {
			c.ConfigJSON["usdt_fixed"].(map[string]interface{})["rate"] = "0"
		},
		func(c *paymentdomain.PaymentChannel) {
			c.ConfigJSON["usdt_fixed"].(map[string]interface{})["address"] = "0x" + strings.Repeat("0", 40)
		},
		func(c *paymentdomain.PaymentChannel) {
			c.ConfigJSON["usdt_fixed"].(map[string]interface{})["network"] = "TRC20"
		},
		func(c *paymentdomain.PaymentChannel) {
			c.ConfigJSON["usdt_fixed"].(map[string]interface{})["quote_ttl_minutes"] = 0
		},
	} {
		channel := manualUSDTChannel()
		mutate(channel)
		if svc.ValidateChannel(channel) == nil {
			t.Fatalf("accepted invalid channel: %+v", channel)
		}
	}
	channel := manualUSDTChannel()
	channel.ProviderType = "unknown"
	if svc.ValidateChannel(channel) == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestManualUSDTCreatesPendingWithoutGatewayOrCNYConversion(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	svc.paymentProviderRegistry = nil // This path must not require an external gateway.
	channel := manualUSDTChannel()
	order := &orderdomain.Order{ID: 1, OrderNo: "DJ-MANUAL-1", Currency: "CNY", Status: constants.OrderStatusPendingPayment}
	payment := &paymentdomain.Payment{OrderID: 1, ChannelID: 1, ProviderType: "manual_qr", ChannelType: "binance_usdt", InteractionMode: "qr", Currency: "CNY", Amount: money.FromDecimal(decimal.NewFromInt(100)), Status: constants.PaymentStatusInitiated}
	if err := db.Create(payment).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.applyProviderPayment(CreatePaymentInput{}, order, channel, payment); err != nil {
		t.Fatal(err)
	}
	var stored paymentdomain.Payment
	if err := db.First(&stored, payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != constants.PaymentStatusPending || stored.Currency != "CNY" || !stored.Amount.Decimal.Equal(decimal.NewFromInt(100)) || stored.QRCode != "manual://order/DJ-MANUAL-1" || stored.PayURL != "" {
		t.Fatalf("bad pending state: %+v", stored)
	}
	if strings.Contains(stored.QRCode, "0x") {
		t.Fatal("placeholder exposed deposit address")
	}
	for _, mutate := range []func(){func() { order.Currency = "USD" }, func() { order.Currency = "CNY"; channel.IsActive = false }, func() { channel.IsActive = true; payment.OrderID = 2 }} {
		mutate()
		payment.Status = constants.PaymentStatusInitiated
		if svc.applyProviderPayment(CreatePaymentInput{}, order, channel, payment) == nil {
			t.Fatal("invalid payment accepted")
		}
	}
}

func TestManualUSDTCoreCreateAndAdminConfirm(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel := manualUSDTChannel()
	if err := db.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
	order := &orderdomain.Order{OrderNo: "DJMANUAL-CREATE", Currency: "CNY", Status: constants.OrderStatusPendingPayment, TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), OriginalAmount: money.FromDecimal(decimal.NewFromInt(100))}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderPaid || result.Payment == nil || result.Payment.Status != constants.PaymentStatusPending {
		t.Fatalf("unexpected create result: %+v", result)
	}
	if err := db.First(order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != constants.OrderStatusPendingPayment {
		t.Fatal("creation marked order paid")
	}
	if _, err := svc.ConfirmManualPayment(result.Payment.ID, receiptEvidence(t, db, result.Payment.ID)); err != nil {
		t.Fatal(err)
	}
	var payment paymentdomain.Payment
	if err := db.First(&payment, result.Payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if payment.Status != constants.PaymentStatusSuccess {
		t.Fatal("admin confirmation did not settle")
	}
}

func TestManualUSDTKeepsLegacyProvidersAndRejectsRecharge(t *testing.T) {
	svc := &PaymentService{}
	order := &orderdomain.Order{OrderNo: "DJLEGACY"}
	for _, kind := range []string{"alipay", "wechat", "unknown"} {
		channel := manualUSDTChannel()
		channel.ChannelType = kind
		if err := svc.ValidateChannel(channel); err != ErrPaymentProviderNotSupported {
			t.Fatalf("legacy validation changed for %s: %v", kind, err)
		}
		if err := svc.applyProviderPayment(CreatePaymentInput{}, order, channel, &paymentdomain.Payment{}); err != ErrPaymentProviderNotSupported {
			t.Fatalf("legacy creation changed for %s: %v", kind, err)
		}
	}
	channel := manualUSDTChannel()
	if err := svc.applyProviderPayment(CreatePaymentInput{ReturnBizType: "recharge"}, &orderdomain.Order{OrderNo: "WRTEST"}, channel, &paymentdomain.Payment{ID: 1, ChannelID: 1, ProviderType: "manual_qr", ChannelType: "binance_usdt", Status: constants.PaymentStatusInitiated, Currency: "CNY", Amount: money.FromDecimal(decimal.NewFromInt(100))}); err == nil {
		t.Fatal("wallet virtual order accepted")
	}
}

func TestManualUSDTAdminConfirmIncludesFeesAndIsIdempotent(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	channel := manualUSDTChannel()
	if err := db.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
	order := &orderdomain.Order{OrderNo: "DJMANUAL-FEE", Currency: "CNY", Status: constants.OrderStatusPendingPayment, TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), OriginalAmount: money.FromDecimal(decimal.NewFromInt(100))}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Mirror the proxy's atomic CNY fee snapshot, not a USDT accounting conversion.
	if err := db.Model(result.Payment).Updates(map[string]interface{}{"amount": 108, "fee_amount": 8, "fee_rate": 8}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.ConfirmManualPayment(result.Payment.ID, receiptEvidence(t, db, result.Payment.ID)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	var payment paymentdomain.Payment
	if err := db.First(&payment, result.Payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != constants.OrderStatusPaid || payment.Status != constants.PaymentStatusSuccess || payment.ExceptionCode != "" || !payment.Amount.Decimal.Equal(decimal.NewFromInt(108)) {
		t.Fatalf("fee settlement incorrect: order=%s payment=%+v", order.Status, payment)
	}
}

func TestManualUSDTCreatePaymentWithSQLiteConfigStorageTypes(t *testing.T) {
	for _, storageType := range []string{"text", "blob"} {
		t.Run(storageType, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			svc.paymentProviderRegistry = nil
			channel := manualUSDTChannel()
			if err := db.Create(channel).Error; err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(channel.ConfigJSON)
			if err != nil {
				t.Fatal(err)
			}
			// Python deployment scripts bind json.dumps output as TEXT; GORM's
			// driver.Valuer binds JSON as BLOB. Both must load the same config.
			var config interface{} = encoded
			if storageType == "text" {
				config = string(encoded)
			}
			if err := db.Exec("UPDATE payment_channels SET config_json = ? WHERE id = ?", config, channel.ID).Error; err != nil {
				t.Fatal(err)
			}
			var actualType string
			if err := db.Raw("SELECT typeof(config_json) FROM payment_channels WHERE id = ?", channel.ID).Scan(&actualType).Error; err != nil {
				t.Fatal(err)
			}
			if actualType != storageType {
				t.Fatalf("SQLite storage type = %s, want %s", actualType, storageType)
			}
			order := &orderdomain.Order{OrderNo: "DJMANUAL-CONFIG", Currency: "CNY", Status: constants.OrderStatusPendingPayment, TotalAmount: money.FromDecimal(decimal.NewFromInt(100)), OriginalAmount: money.FromDecimal(decimal.NewFromInt(100))}
			if err := db.Create(order).Error; err != nil {
				t.Fatal(err)
			}
			result, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID})
			if err != nil {
				t.Fatalf("CreatePayment with SQLite %s config: %v", storageType, err)
			}
			if result.OrderPaid || result.Payment == nil || result.Payment.Status != constants.PaymentStatusPending || result.Payment.QRCode != "manual://order/"+order.OrderNo {
				t.Fatalf("unexpected payment result: %+v", result)
			}
			if err := db.First(order, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if order.Status != constants.OrderStatusPendingPayment {
				t.Fatalf("creation changed order status to %s", order.Status)
			}
		})
	}
}
