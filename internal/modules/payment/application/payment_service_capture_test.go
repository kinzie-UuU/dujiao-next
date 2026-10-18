package application

import (
	"crypto/sha256"
	"fmt"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"strings"

	"context"
	"errors"
	"testing"
	"time"

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

func receiptEvidence(t *testing.T, db *gorm.DB, id uint) paymentdomain.ManualPaymentEvidence {
	t.Helper()
	var payment paymentdomain.Payment
	if err := db.First(&payment, id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS payment_risk_reservations (order_no TEXT PRIMARY KEY, identity_hash TEXT, amount_cents INTEGER, status TEXT, policy_version TEXT, created_at INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	if payment.OrderID > 0 {
		if err := db.Exec(`INSERT OR REPLACE INTO payment_risk_reservations SELECT order_no, '', CAST(ROUND(total_amount*100) AS INTEGER),'allowed','2026-10-18',? FROM orders WHERE id=?`, time.Now().Unix(), payment.OrderID).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		if err := db.Exec(`INSERT OR REPLACE INTO payment_risk_reservations SELECT recharge_no, '', CAST(ROUND(amount*100) AS INTEGER),'allowed','2026-10-18',? FROM wallet_recharge_orders WHERE payment_id=?`, time.Now().Unix(), payment.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var identity string
	var number string
	if payment.OrderID > 0 {
		var o orderdomain.Order
		db.First(&o, payment.OrderID)
		if o.UserID == 0 && o.GuestEmail == "" {
			o.GuestEmail = "receipt-fixture@example.test"
			db.Model(&o).Update("guest_email", o.GuestEmail)
		}
		number = o.OrderNo
		identity = fmt.Sprintf("user:%d", o.UserID)
		if o.UserID == 0 {
			identity = "guest:" + strings.ToLower(strings.TrimSpace(o.GuestEmail))
		}
	} else {
		var recharge struct {
			UserID     uint
			RechargeNo string
		}
		db.Table("wallet_recharge_orders").Where("payment_id = ?", payment.ID).Take(&recharge)
		identity = fmt.Sprintf("user:%d", recharge.UserID)
		number = recharge.RechargeNo
	}
	if err := db.Exec("UPDATE payment_risk_reservations SET identity_hash=? WHERE order_no=?", fmt.Sprintf("%x", sha256.Sum256([]byte(identity))), number).Error; err != nil {
		t.Fatal(err)
	}
	if payment.ChannelType == "binance_usdt" {
		quote := map[string]interface{}{"currency": "USDT", "orderAmount": payment.Amount.String(), "rate": "7", "amount": payment.Amount.Decimal.Div(decimal.NewFromInt(7)).Round(2).String()}
		if payment.ProviderPayload == nil {
			payment.ProviderPayload = map[string]interface{}{}
		}
		payment.ProviderPayload["crypto_quote"] = quote
		if err := db.Model(&payment).Update("provider_payload", payment.ProviderPayload).Error; err != nil {
			t.Fatal(err)
		}
	}
	settlement, currency, err := manualSettlement(&payment)
	if err != nil {
		t.Fatal(err)
	}
	return paymentdomain.ManualPaymentEvidence{TransactionID: fmt.Sprintf("fixture-receipt-%d", id), PayerVerified: true, Amount: settlement.String(), Currency: currency}
}

func TestManualReceiptRequiredAndAmountChecked(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	p := paymentdomain.Payment{ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: money.FromDecimal(decimal.NewFromInt(100)), Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmManualPayment(p.ID); !errors.Is(err, ErrPaymentInvalid) {
		t.Fatalf("missing evidence accepted: %v", err)
	}
	for _, mutate := range []func(*paymentdomain.ManualPaymentEvidence){
		func(e *paymentdomain.ManualPaymentEvidence) { e.PayerVerified = false },
		func(e *paymentdomain.ManualPaymentEvidence) { e.TransactionID = "bad" },
		func(e *paymentdomain.ManualPaymentEvidence) { e.Amount = "100.01" },
		func(e *paymentdomain.ManualPaymentEvidence) { e.Amount = "100.001" },
		func(e *paymentdomain.ManualPaymentEvidence) { e.Currency = "USD" },
	} {
		e := receiptEvidence(t, db, p.ID)
		mutate(&e)
		if _, err := svc.ConfirmManualPayment(p.ID, e); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	var got paymentdomain.Payment
	db.First(&got, p.ID)
	if got.ManualReceiptKey != nil || got.Status != constants.PaymentStatusPending {
		t.Fatal("invalid proof mutated payment")
	}
}

func TestManualReceiptUniqueAcrossPaymentsAndRetries(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	makePayment := func() paymentdomain.Payment {
		p := paymentdomain.Payment{ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: money.FromDecimal(decimal.NewFromInt(100)), Currency: "CNY", Status: constants.PaymentStatusPending}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p
	}
	first, second := makePayment(), makePayment()
	for _, p := range []*paymentdomain.Payment{&first, &second} {
		order := orderdomain.Order{OrderNo: fmt.Sprintf("receipt-order-%d", p.ID), Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: p.Amount}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		db.Model(p).Update("order_id", order.ID)
		receiptEvidence(t, db, p.ID)
	}
	proof := receiptEvidence(t, db, first.ID)
	// No order/recharge exists: capture fails, but the receipt remains reserved.
	svc.ConfirmManualPayment(first.ID, proof)
	var got paymentdomain.Payment
	db.First(&got, first.ID)
	if got.ManualReceiptKey == nil {
		t.Fatal("receipt not reserved")
	}
	if _, err := svc.ConfirmManualPayment(second.ID, proof); err == nil {
		t.Fatal("receipt reused")
	}
	changed := proof
	changed.TransactionID = "fixture-another-receipt"
	if _, err := svc.ConfirmManualPayment(first.ID, changed); !errors.Is(err, ErrPaymentInvalid) {
		t.Fatalf("receipt replaced: %v", err)
	}
	// Unique database constraint also protects paths racing after the initial read.
	second.ManualReceiptKey = got.ManualReceiptKey
	if err := db.Save(&second).Error; err == nil {
		t.Fatal("database accepted duplicate receipt")
	}
}

func TestManualReceiptRiskHoldMissingPolicyAndChangedAmount(t *testing.T) {
	for _, scenario := range []string{"missing", "hold", "stale", "amount_changed", "identity_changed", "expired_reservation", "future_reservation"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			order := orderdomain.Order{OrderNo: "risk-order", Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(100))}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			p := paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
			if err := db.Create(&p).Error; err != nil {
				t.Fatal(err)
			}
			proof := receiptEvidence(t, db, p.ID)
			switch scenario {
			case "expired_reservation":
				db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Add(-25*time.Hour).Unix())
			case "future_reservation":
				db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Add(time.Hour).Unix())
			case "missing":
				db.Exec("DELETE FROM payment_risk_reservations")
			case "hold":
				db.Exec("UPDATE payment_risk_reservations SET status='review_required'")
			case "stale":
				db.Exec("UPDATE payment_risk_reservations SET policy_version='old'")
			case "identity_changed":
				db.Exec("UPDATE payment_risk_reservations SET identity_hash='wrong'")
			case "amount_changed":
				db.Exec("UPDATE payment_risk_reservations SET amount_cents=1")
			}
			if _, err := svc.ConfirmManualPayment(p.ID, proof); !errors.Is(err, ErrPaymentStatusInvalid) {
				t.Fatalf("risk bypass accepted: %v", err)
			}
			var actual paymentdomain.Payment
			db.First(&actual, p.ID)
			if actual.Status != constants.PaymentStatusPending || actual.ManualReceiptKey != nil {
				t.Fatal("held payment mutated")
			}
		})
	}
}

func TestManualCallbackCannotBypassReceiptReview(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	order := orderdomain.Order{OrderNo: "risk-callback-order", Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(100))}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	p := paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	receiptEvidence(t, db, p.ID)
	if _, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: p.ID, ChannelID: 1, Status: constants.PaymentStatusSuccess, Amount: p.Amount, Currency: "CNY", ProviderRef: "fabricated-receipt"}); !errors.Is(err, ErrPaymentInvalid) {
		t.Fatalf("unreviewed callback accepted: %v", err)
	}
}

func TestManualReceiptConcurrentReuseCreditsOnlyOneOrder(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	payments := make([]paymentdomain.Payment, 2)
	proofs := make([]paymentdomain.ManualPaymentEvidence, 2)
	for i := range payments {
		order := orderdomain.Order{OrderNo: fmt.Sprintf("race-receipt-%d", i), Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(100))}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		payments[i] = paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
		if err := db.Create(&payments[i]).Error; err != nil {
			t.Fatal(err)
		}
		proofs[i] = receiptEvidence(t, db, payments[i].ID)
		proofs[i].TransactionID = "same-platform-receipt"
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range payments {
		go func(i int) { <-start; _, err := svc.ConfirmManualPayment(payments[i].ID, proofs[i]); results <- err }(i)
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d, want 1", successes)
	}
	var paid int64
	db.Model(&paymentdomain.Payment{}).Where("status = ?", constants.PaymentStatusSuccess).Count(&paid)
	if paid != 1 {
		t.Fatalf("paid=%d, want 1", paid)
	}
}

func TestManualSettlementUsesLockedTWDAndUSDTQuotes(t *testing.T) {
	base := money.FromDecimal(decimal.NewFromInt(100))
	for _, tc := range []struct{ channel, key, currency, ledger, rateKey, rate, amount string }{
		{"twqr", "twqr_quote", "TWD", "cnyAmount", "exchangeRate", "4.5", "450"},
		{"binance_usdt", "crypto_quote", "USDT", "orderAmount", "rate", "7", "14.29"},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			p := paymentdomain.Payment{ChannelType: tc.channel, Currency: "CNY", Amount: base}
			if _, _, err := manualSettlement(&p); err == nil {
				t.Fatal("missing settlement accepted")
			}
			quote := map[string]interface{}{"currency": tc.currency, tc.ledger: "100", tc.rateKey: tc.rate, "amount": tc.amount}
			p.ProviderPayload = map[string]interface{}{tc.key: quote}
			amount, currency, err := manualSettlement(&p)
			if err != nil || amount.String() != tc.amount || currency != tc.currency {
				t.Fatalf("settlement=%s %s err=%v", amount, currency, err)
			}
			quote["amount"] = "100"
			if _, _, err := manualSettlement(&p); err == nil {
				t.Fatal("incorrect conversion accepted")
			}
			quote["amount"] = tc.amount
			quote[tc.ledger] = "99"
			if _, _, err := manualSettlement(&p); err == nil {
				t.Fatal("stale CNY ledger quote accepted")
			}
		})
	}
}

// Expiry between receipt reservation and credit must fail closed in the second transaction.
type expireManualRiskAfterClaimStore struct {
	paymentcontract.Store
	afterClaim func()
}

func (s *expireManualRiskAfterClaimStore) WithinTransaction(fn func(paymentcontract.Transaction) error) error {
	err := s.Store.WithinTransaction(fn)
	if err == nil && s.afterClaim != nil {
		change := s.afterClaim
		s.afterClaim = nil
		change()
	}
	return err
}

func TestManualRechargeReservationExpiryAndSuccessRetry(t *testing.T) {
	for _, scenario := range []string{"expired", "expires_after_claim", "success_retry_after_expiry"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
			if err := db.Model(payment).Update("provider_type", manualQRProviderType).Error; err != nil {
				t.Fatal(err)
			}
			proof := receiptEvidence(t, db, payment.ID)
			expire := func() {
				if err := db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Add(-25*time.Hour).Unix()).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" {
				expire()
			}
			if scenario == "expires_after_claim" {
				svc.paymentRepo = &expireManualRiskAfterClaimStore{Store: svc.paymentRepo, afterClaim: expire}
			}
			got, err := svc.ConfirmManualPayment(payment.ID, proof)
			if scenario == "success_retry_after_expiry" {
				if err != nil || got.Status != constants.PaymentStatusSuccess {
					t.Fatalf("initial confirmation: payment=%+v err=%v", got, err)
				}
				expire()
				if _, err := svc.ConfirmManualPayment(payment.ID, proof); err != nil {
					t.Fatalf("idempotent retry rejected: %v", err)
				}
				if balance := walletBalance(t, db, recharge.UserID); balance != "88.00" {
					t.Fatalf("duplicate credit: %s", balance)
				}
			} else {
				if !errors.Is(err, ErrPaymentStatusInvalid) {
					t.Fatalf("stale policy accepted: %v", err)
				}
				var actual paymentdomain.Payment
				if err := db.First(&actual, payment.ID).Error; err != nil {
					t.Fatal(err)
				}
				if actual.Status != constants.PaymentStatusPending {
					t.Fatalf("stale policy changed status: %s", actual.Status)
				}
				var credits int64
				if err := db.Model(&walletdomain.Transaction{}).Where("user_id=?", recharge.UserID).Count(&credits).Error; err != nil {
					t.Fatal(err)
				}
				if credits != 0 {
					t.Fatalf("stale policy credited wallet: %d", credits)
				}
				if scenario == "expires_after_claim" && actual.ManualReceiptKey == nil {
					t.Fatal("receipt reservation released after failed credit")
				}
				if scenario == "expires_after_claim" {
					if err := db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Unix()).Error; err != nil {
						t.Fatal(err)
					}
					if _, err := svc.ConfirmManualPayment(payment.ID, proof); err != nil {
						t.Fatalf("revalidated retry failed: %v", err)
					}
					if balance := walletBalance(t, db, recharge.UserID); balance != "88.00" {
						t.Fatalf("revalidated retry credit=%s", balance)
					}
				}
			}
		})
	}
}

func TestManualReceiptNamespaceAcrossOrderAndRecharge(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	order := createUnderpaidOrder(t, db, "CROSS-RECEIPT", 0, 88)
	item := paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	recharge, _ := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	if err := db.Model(recharge).Updates(map[string]interface{}{"provider_type": manualQRProviderType, "channel_id": 2}).Error; err != nil {
		t.Fatal(err)
	}
	proofA, proofB := receiptEvidence(t, db, item.ID), receiptEvidence(t, db, recharge.ID)
	proofA.TransactionID = " Mixed-Platform-Receipt "
	proofB.TransactionID = "mixed-platform-receipt"
	start, done := make(chan struct{}), make(chan error, 2)
	for _, test := range []struct {
		id    uint
		proof paymentdomain.ManualPaymentEvidence
	}{{item.ID, proofA}, {recharge.ID, proofB}} {
		go func(id uint, proof paymentdomain.ManualPaymentEvidence) {
			<-start
			_, err := svc.ConfirmManualPayment(id, proof)
			done <- err
		}(test.id, test.proof)
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if <-done == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("same channel receipt across order/recharge successes=%d", successes)
	}
	var payments []paymentdomain.Payment
	if err := db.Find(&payments).Error; err != nil {
		t.Fatal(err)
	}
	for _, p := range payments {
		if p.Status != constants.PaymentStatusSuccess {
			continue
		}
		proof := paymentdomain.ManualPaymentEvidence{TransactionID: p.ProviderRef, PayerVerified: true, Amount: p.Amount.String(), Currency: p.Currency}
		for _, field := range []string{"amount", "currency", "payer"} {
			bad := proof
			switch field {
			case "amount":
				bad.Amount = "89"
			case "currency":
				bad.Currency = "USD"
			case "payer":
				bad.PayerVerified = false
			}
			if _, err := svc.ConfirmManualPayment(p.ID, bad); err == nil {
				t.Fatalf("successful replay accepted invalid %s", field)
			}
		}
	}
}
