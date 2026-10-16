package application

import (
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"

	"context"
	"errors"
	"testing"

	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/provider"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

// recordingCapturer 捕获传入 ValidateConfig 的第二参数,用于回归测试。
type recordingCapturer struct {
	receivedMode string
}

func (r *recordingCapturer) Type() string { return "test_capturer" }

func (r *recordingCapturer) ValidateConfig(_ jsonmap.JSON, mode string) error {
	r.receivedMode = mode
	return errors.New("stop-test")
}

func (r *recordingCapturer) CreatePayment(_ context.Context, _ jsonmap.JSON, _ paymentcontract.GatewayCreateInput) (*paymentcontract.GatewayCreateResult, error) {
	return nil, errors.New("not-implemented")
}

func (r *recordingCapturer) QueryPayment(_ context.Context, _ jsonmap.JSON, _ string) (*paymentcontract.GatewayQueryResult, error) {
	return nil, errors.New("not-implemented")
}

// 防止重新引入 stripe/wechat capture 全部失败的回归:
// captureViaRegistry 必须把 channel.InteractionMode(而非 channel.ChannelType)
// 传给 Capturer.ValidateConfig,否则 stripe/wechat adapter 会判定 mode 非法。
func TestCaptureViaRegistry_PassesInteractionModeNotChannelType(t *testing.T) {
	cap := &recordingCapturer{}
	reg := provider.NewRegistry()
	reg.Register("test_provider", "test_channel", cap)

	svc := &PaymentService{paymentProviderRegistry: reg}

	channel := &paymentdomain.PaymentChannel{
		ID:              1,
		ProviderType:    "test_provider",
		ChannelType:     "test_channel",
		InteractionMode: constants.PaymentInteractionQR,
	}
	payment := &paymentdomain.Payment{
		ID:          1,
		ChannelID:   1,
		ProviderRef: "ref-123",
	}

	_, _ = svc.captureViaRegistry(CapturePaymentInput{}, payment, channel)

	if cap.receivedMode != channel.InteractionMode {
		t.Fatalf("ValidateConfig received mode=%q, want %q (channel.InteractionMode)",
			cap.receivedMode, channel.InteractionMode)
	}
	if cap.receivedMode == channel.ChannelType {
		t.Fatalf("regression: ValidateConfig received channel.ChannelType=%q", cap.receivedMode)
	}
}

func TestPendingPaymentCannotBeReusedOrCapturedWhileInDebt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	user := userdomain.User{Email: "pending-debt@example.test", PasswordHash: "hash", Status: constants.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := createUnderpaidOrder(t, db, "DEBT-OLD-LINK", user.ID, 99)
	channel := createUnderpaidChannel(t, db, svc, "local", constants.PaymentChannelTypeWechat)
	first, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&paymentdomain.Payment{}).Where("id = ?", first.Payment.ID).Update("pay_url", "https://example.test/old-link").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: money.FromDecimal(decimal.NewFromInt(-5))}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID}); !errors.Is(err, walletcontract.ErrOutstandingDebt) {
		t.Fatalf("pending link reused: %v", err)
	}
	if _, err := svc.CapturePayment(CapturePaymentInput{PaymentID: first.Payment.ID}); !errors.Is(err, walletcontract.ErrOutstandingDebt) {
		t.Fatalf("pending capture not blocked: %v", err)
	}
	if err := db.Model(&paymentdomain.Payment{}).Where("id = ?", first.Payment.ID).Update("status", constants.PaymentStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CapturePayment(CapturePaymentInput{PaymentID: first.Payment.ID}); err != nil {
		t.Fatalf("already paid query blocked: %v", err)
	}
}
