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
	"sync"
	"sync/atomic"

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
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS payment_risk_reservations (order_no TEXT PRIMARY KEY, identity_hash TEXT, amount_cents INTEGER, status TEXT, policy_version TEXT, created_at REAL)`).Error; err != nil {
		t.Fatal(err)
	}
	if payment.OrderID > 0 {
		if err := db.Exec(`INSERT OR REPLACE INTO payment_risk_reservations SELECT order_no, '', CAST(ROUND(total_amount*100) AS INTEGER),'allowed','2026-10-18',? FROM orders WHERE id=?`, float64(time.Now().UnixNano())/1e9, payment.OrderID).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		if err := db.Exec(`INSERT OR REPLACE INTO payment_risk_reservations SELECT recharge_no, '', CAST(ROUND(amount*100) AS INTEGER),'allowed','2026-10-18',? FROM wallet_recharge_orders WHERE payment_id=?`, float64(time.Now().UnixNano())/1e9, payment.ID).Error; err != nil {
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
		func(e *paymentdomain.ManualPaymentEvidence) { e.Amount = "99.99" },
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
			p := paymentdomain.Payment{ProviderType: manualQRProviderType, ChannelType: tc.channel, Currency: "CNY", Amount: base}
			if _, _, err := manualSettlement(&p); err == nil {
				t.Fatal("missing settlement accepted")
			}
			quote := map[string]interface{}{"currency": tc.currency, tc.ledger: "100", tc.rateKey: tc.rate, "amount": tc.amount}
			p.ProviderPayload = map[string]interface{}{tc.key: quote}
			amount, currency, err := manualSettlement(&p)
			if err != nil || amount.String() != tc.amount || currency != tc.currency {
				t.Fatalf("settlement=%s %s err=%v", amount, currency, err)
			}
			populateManualSettlement(&p)
			if p.SettlementAmount != tc.amount || p.SettlementCurrency != tc.currency {
				t.Fatal("safe admin settlement missing")
			}
			quote["amount"] = "100"
			if _, _, err := manualSettlement(&p); err == nil {
				t.Fatal("incorrect conversion accepted")
			}
			populateManualSettlement(&p)
			if p.SettlementAmount != "" || p.SettlementCurrency != "" {
				t.Fatal("invalid quote exposed to admin")
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

func TestManualAdminAttestationWithoutTransaction(t *testing.T) {
	for _, rechargeCase := range []bool{false, true} {
		t.Run(fmt.Sprintf("recharge=%v", rechargeCase), func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			var p paymentdomain.Payment
			var userID uint
			if rechargeCase {
				payment, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
				p, userID = *payment, recharge.UserID
				db.Model(&p).Update("provider_type", manualQRProviderType)
			} else {
				order := createUnderpaidOrder(t, db, "ADMIN-ATTESTATION", 0, 88)
				p = paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
				if err := db.Create(&p).Error; err != nil {
					t.Fatal(err)
				}
			}
			proof := receiptEvidence(t, db, p.ID)
			proof.TransactionID = ""
			for _, field := range []string{"payer", "amount", "currency", "hold"} {
				bad := proof
				switch field {
				case "payer":
					bad.PayerVerified = false
				case "amount":
					bad.Amount = "0.01"
				case "currency":
					bad.Currency = "USD"
				case "hold":
					db.Exec("UPDATE payment_risk_reservations SET status='review_required'")
				}
				if _, err := svc.ConfirmManualPayment(p.ID, bad); err == nil {
					t.Fatalf("accepted invalid %s without transaction", field)
				}
			}
			receiptEvidence(t, db, p.ID)
			start, done := make(chan struct{}), make(chan error, 8)
			for i := 0; i < 8; i++ {
				go func() { <-start; _, err := svc.ConfirmManualPayment(p.ID, proof); done <- err }()
			}
			close(start)
			for i := 0; i < 8; i++ {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			var got paymentdomain.Payment
			db.First(&got, p.ID)
			if got.Status != constants.PaymentStatusSuccess || got.ManualReceiptKey == nil || *got.ManualReceiptKey != fmt.Sprintf("admin-confirm:%d", p.ID) || got.ProviderRef != "" {
				t.Fatalf("invalid attestation state: status=%s key=%v ref=%q", got.Status, got.ManualReceiptKey, got.ProviderRef)
			}
			if got.ProviderPayload["receipt_verification"] != "admin_attestation" {
				t.Fatal("attestation mislabeled as platform receipt")
			}
			if rechargeCase && walletBalance(t, db, userID) != "88.00" {
				t.Fatal("repeated confirmation credited wallet more than once")
			}
		})
	}
}

func TestManualAdminAttestationPreservesLegacyReceipt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	for i := 0; i < 2; i++ {
		order := createUnderpaidOrder(t, db, fmt.Sprintf("LEGACY-ATTESTATION-%d", i), 0, 88)
		p := paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: order.TotalAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		proof := receiptEvidence(t, db, p.ID)
		originalRef := proof.TransactionID
		if _, err := svc.ConfirmManualPayment(p.ID, proof); err != nil {
			t.Fatal(err)
		}
		proof.TransactionID = ""
		got, err := svc.ConfirmManualPayment(p.ID, proof)
		if err != nil {
			t.Fatal(err)
		}
		if got.ProviderRef != originalRef || got.ManualReceiptKey == nil || *got.ManualReceiptKey != "wechat:"+originalRef {
			t.Fatal("retry lost original platform receipt")
		}
	}
}

type receiptQueue struct {
	paymentcontract.Queue
	fulfilled atomic.Int32
}

func (q *receiptQueue) Enabled() bool                                  { return true }
func (q *receiptQueue) EnqueueOrderAutoFulfill(uint) error             { q.fulfilled.Add(1); return nil }
func (q *receiptQueue) EnqueueStatusEmail(uint, string) error          { return nil }
func (q *receiptQueue) EnqueueTimeoutCancel(uint, time.Duration) error { return nil }

type receiptProcurement struct{ calls atomic.Int32 }

func (p *receiptProcurement) CreateForOrder(uint) error { p.calls.Add(1); return nil }

func TestManualOverpaymentPreservesQuotedAccounting(t *testing.T) {
	for _, tt := range []struct{ name, channel, quote, fee, wallet, received, surplus string }{
		{"invoice8percent", "wechat", "143.64", "10.64", "0", "146", "2.36"},
		{"standard2percent", "alipay", "135.66", "2.66", "0", "136", "0.34"},
		{"walletMixedInvoice", "wechat", "108", "8", "33", "109", "1.00"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			order := createUnderpaidOrder(t, db, "OVER-"+tt.name, 0, 133)
			wallet := decimal.RequireFromString(tt.wallet)
			if err := db.Model(order).Updates(map[string]interface{}{"wallet_paid_amount": tt.wallet, "online_paid_amount": decimal.NewFromInt(133).Sub(wallet).String()}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, TitleJSON: jsonmap.JSON{"en": "test"}, Quantity: 1, FulfillmentType: constants.FulfillmentTypeAuto}).Error; err != nil {
				t.Fatal(err)
			}
			payment := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: tt.channel, Amount: money.FromDecimal(decimal.RequireFromString(tt.quote)), FeeAmount: money.FromDecimal(decimal.RequireFromString(tt.fee)), Currency: "CNY", Status: constants.PaymentStatusPending}
			if err := db.Create(payment).Error; err != nil {
				t.Fatal(err)
			}
			proof := receiptEvidence(t, db, payment.ID)
			proof.Amount = tt.received
			queue := &receiptQueue{}
			svc.queue = queue
			procurement := &receiptProcurement{}
			svc.procurementSvc = procurement
			var wg sync.WaitGroup
			results := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := svc.ConfirmManualPayment(payment.ID, proof); results <- err }()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			if procurement.calls.Load() != 1 {
				t.Fatalf("procurement count=%d", procurement.calls.Load())
			}
			if queue.fulfilled.Load() != 1 {
				t.Fatalf("fulfillment count=%d", queue.fulfilled.Load())
			}
			if err := db.First(payment, payment.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !payment.Amount.Decimal.Equal(decimal.RequireFromString(tt.quote)) || !payment.FeeAmount.Decimal.Equal(decimal.RequireFromString(tt.fee)) {
				t.Fatal("quote or fee changed")
			}
			for key, want := range map[string]string{manualReceivedAmountKey: decimal.RequireFromString(tt.received).StringFixed(2), manualReceivedCurrencyKey: "CNY", manualExpectedAmountKey: decimal.RequireFromString(tt.quote).StringFixed(2), manualOverpaymentAmountKey: tt.surplus} {
				if payment.ProviderPayload[key] != want {
					t.Fatalf("audit %s=%v want %s", key, payment.ProviderPayload[key], want)
				}
			}
			if err := db.First(order, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !order.TotalAmount.Decimal.Equal(decimal.NewFromInt(133)) || !order.WalletPaidAmount.Decimal.Equal(wallet) || !order.OnlinePaidAmount.Decimal.Equal(decimal.NewFromInt(133).Sub(wallet)) {
				t.Fatal("order accounting changed")
			}
			proof.Amount = decimal.RequireFromString(tt.received).Add(decimal.NewFromInt(1)).String()
			if _, err := svc.ConfirmManualPayment(payment.ID, proof); !errors.Is(err, ErrPaymentInvalid) {
				t.Fatalf("changed receipt accepted: %v", err)
			}
			proof.Amount = tt.received
			proof.Currency = "USD"
			if _, err := svc.ConfirmManualPayment(payment.ID, proof); !errors.Is(err, ErrPaymentCurrencyMismatch) {
				t.Fatalf("changed currency accepted: %v", err)
			}
		})
	}
}

func TestManualOverpaymentRejectsUnderpaymentAndInvalidCents(t *testing.T) {
	for _, amount := range []string{"143.63", "146.001", "146.000", "1e3", "+146", " 146", "-146", "0"} {
		t.Run(amount, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			order := createUnderpaidOrder(t, db, "INVALID-AMOUNT", 0, 133)
			p := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: money.FromDecimal(decimal.RequireFromString("143.64")), FeeAmount: money.FromDecimal(decimal.RequireFromString("10.64")), Currency: "CNY", Status: constants.PaymentStatusPending}
			if err := db.Create(p).Error; err != nil {
				t.Fatal(err)
			}
			proof := receiptEvidence(t, db, p.ID)
			proof.Amount = amount
			if _, err := svc.ConfirmManualPayment(p.ID, proof); !errors.Is(err, ErrPaymentAmountMismatch) {
				t.Fatalf("amount accepted: %v", err)
			}
			db.First(p, p.ID)
			if p.ManualReceiptKey != nil || p.Status != constants.PaymentStatusPending {
				t.Fatal("rejected amount claimed or paid")
			}
		})
	}
}

func TestManualOverpaymentClaimSurvivesFailedCapture(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	order := createUnderpaidOrder(t, db, "OVER-FAILED-CAPTURE", 0, 133)
	p := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: money.FromDecimal(decimal.RequireFromString("143.64")), FeeAmount: money.FromDecimal(decimal.RequireFromString("10.64")), Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	proof := receiptEvidence(t, db, p.ID)
	proof.Amount = "146"
	svc.paymentRepo = &expireManualRiskAfterClaimStore{Store: svc.paymentRepo, afterClaim: func() {
		db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Add(-25*time.Hour).Unix())
	}}
	if _, err := svc.ConfirmManualPayment(p.ID, proof); !errors.Is(err, ErrPaymentStatusInvalid) {
		t.Fatalf("expected second-stage failure: %v", err)
	}
	db.Exec("UPDATE payment_risk_reservations SET created_at=?", time.Now().Unix())
	proof.Amount = "147"
	if _, err := svc.ConfirmManualPayment(p.ID, proof); !errors.Is(err, ErrPaymentInvalid) {
		t.Fatalf("changed pending receipt accepted: %v", err)
	}
	db.First(p, p.ID)
	for _, key := range []string{manualReceivedAmountKey, manualExpectedAmountKey, manualOverpaymentAmountKey, manualReceivedCurrencyKey} {
		payload := jsonmap.JSON{"payer_verified_by_admin": true}
		for k, v := range p.ProviderPayload {
			payload[k] = v
		}
		payload[key] = "999"
		_, err := svc.HandleCallback(PaymentCallbackInput{PaymentID: p.ID, ChannelID: 1, Status: constants.PaymentStatusSuccess, ProviderRef: p.ProviderRef, Amount: p.Amount, Currency: "CNY", Payload: payload})
		if err == nil {
			t.Fatalf("changed callback %s accepted", key)
		}
	}
	proof.Amount = "146"
	if _, err := svc.ConfirmManualPayment(p.ID, proof); err != nil {
		t.Fatalf("original proof retry failed: %v", err)
	}
}

func TestManualForeignSettlementStillRejectsOverpayment(t *testing.T) {
	for _, tt := range []struct{ channel, key, currency, ledger, rateKey, rate, amount string }{
		{"twqr", "twqr_quote", "TWD", "cnyAmount", "exchangeRate", "4.5", "450"},
		{"binance_usdt", "crypto_quote", "USDT", "orderAmount", "rate", "7", "14.29"},
	} {
		t.Run(tt.channel, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			order := createUnderpaidOrder(t, db, "FOREIGN-"+tt.channel, 0, 100)
			p := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: tt.channel, Amount: money.FromDecimal(decimal.NewFromInt(100)), Currency: "CNY", Status: constants.PaymentStatusPending, ProviderPayload: jsonmap.JSON{tt.key: map[string]interface{}{"currency": tt.currency, tt.ledger: "100", tt.rateKey: tt.rate, "amount": tt.amount}}}
			if err := db.Create(p).Error; err != nil {
				t.Fatal(err)
			}
			proof := receiptEvidence(t, db, p.ID)
			proof.Amount = decimal.RequireFromString(tt.amount).Add(decimal.NewFromInt(1)).String()
			if _, err := svc.ConfirmManualPayment(p.ID, proof); !errors.Is(err, ErrPaymentAmountMismatch) {
				t.Fatalf("foreign overpayment accepted: %v", err)
			}
			proof.Amount = tt.amount
			if _, err := svc.ConfirmManualPayment(p.ID, proof); err != nil {
				t.Fatalf("exact foreign receipt rejected: %v", err)
			}
		})
	}
}

func TestManualOverpaymentCompetingAmountsBindOneReceipt(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	order := createUnderpaidOrder(t, db, "OVER-RACE", 0, 133)
	p := &paymentdomain.Payment{OrderID: order.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: money.FromDecimal(decimal.RequireFromString("143.64")), FeeAmount: money.FromDecimal(decimal.RequireFromString("10.64")), Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	proof := receiptEvidence(t, db, p.ID)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, amount := range []string{"146", "147"} {
		wg.Add(1)
		go func(amount string) {
			defer wg.Done()
			copy := proof
			copy.Amount = amount
			_, err := svc.ConfirmManualPayment(p.ID, copy)
			results <- err
		}(amount)
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrPaymentInvalid) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
	db.First(p, p.ID)
	other := createUnderpaidOrder(t, db, "OVER-DUPLICATE-RECEIPT", 0, 133)
	duplicate := &paymentdomain.Payment{OrderID: other.ID, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", Amount: p.Amount, FeeAmount: p.FeeAmount, Currency: "CNY", Status: constants.PaymentStatusPending}
	if err := db.Create(duplicate).Error; err != nil {
		t.Fatal(err)
	}
	reused := receiptEvidence(t, db, duplicate.ID)
	reused.TransactionID = proof.TransactionID
	reused.Amount = p.ProviderPayload[manualReceivedAmountKey].(string)
	if _, err := svc.ConfirmManualPayment(duplicate.ID, reused); err == nil {
		t.Fatal("same receipt paid another order")
	}
	db.First(duplicate, duplicate.ID)
	if duplicate.Status != constants.PaymentStatusPending {
		t.Fatal("duplicate paid")
	}
}

func TestManualOverpaidRechargeDoesNotCreditSurplus(t *testing.T) {
	svc, db := setupPaymentServiceWalletTest(t)
	p, recharge := createWalletRechargeFixture(t, db, constants.PaymentStatusPending, constants.WalletRechargeStatusPending)
	if err := db.Model(p).Update("provider_type", manualQRProviderType).Error; err != nil {
		t.Fatal(err)
	}
	proof := receiptEvidence(t, db, p.ID)
	proof.Amount = decimal.RequireFromString(proof.Amount).Add(decimal.NewFromInt(1)).String()
	for i := 0; i < 2; i++ {
		if _, err := svc.ConfirmManualPayment(p.ID, proof); err != nil {
			t.Fatal(err)
		}
	}
	if got := walletBalance(t, db, recharge.UserID); got != "88.00" {
		t.Fatalf("surplus credited or duplicate credit: %s", got)
	}
}

// The gateway, queue and procurement port are in-process fakes. This test never
// sends a payment or a supplier purchase, and uses an isolated in-memory DB.
func TestUpstreamPaymentDispatchNoCharge(t *testing.T) {
	for _, tc := range []struct {
		name, amount, currency string
		valid                  bool
	}{
		{"paid-and-replayed", "81", "CNY", true},
		{"amount-mismatch", "80", "CNY", false},
		{"currency-mismatch", "81", "USD", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			channel := createUnderpaidChannel(t, db, svc, "Mock payment only", constants.PaymentChannelTypeWechat)
			order := createUnderpaidOrder(t, db, "MOCK-UPSTREAM", 0, 81)
			if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"zh-CN": "Figma mock"}, FulfillmentType: constants.FulfillmentTypeUpstream}).Error; err != nil {
				t.Fatal(err)
			}
			q := &receiptQueue{}
			procurement := &receiptProcurement{}
			svc.queue = q
			svc.SetProcurementService(procurement)
			created, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
			if err != nil {
				t.Fatal(err)
			}
			if procurement.calls.Load() != 0 || q.fulfilled.Load() != 0 {
				t.Fatal("unpaid order dispatched fulfillment")
			}
			input := PaymentCallbackInput{PaymentID: created.Payment.ID, OrderNo: order.OrderNo, ChannelID: channel.ID, Status: constants.PaymentStatusSuccess, Amount: money.FromDecimal(decimal.RequireFromString(tc.amount)), Currency: tc.currency, ProviderRef: "MOCK-RECEIPT"}
			_, err = svc.HandleCallback(input)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid callback accepted")
				}
				if procurement.calls.Load() != 0 || q.fulfilled.Load() != 0 {
					t.Fatal("invalid callback dispatched fulfillment")
				}
				if err := db.First(order, order.ID).Error; err != nil {
					t.Fatal(err)
				}
				if order.Status != constants.OrderStatusPendingPayment {
					t.Fatalf("invalid callback changed order to %s", order.Status)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if _, err := svc.HandleCallback(input); err != nil {
					t.Fatal(err)
				}
			}
			if procurement.calls.Load() != 1 {
				t.Fatalf("procurement dispatch count=%d", procurement.calls.Load())
			}
			if q.fulfilled.Load() != 0 {
				t.Fatal("upstream order incorrectly dispatched local-card fulfillment")
			}
			if err := db.First(order, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if order.Status != constants.OrderStatusPaid || order.PaidAt == nil {
				t.Fatalf("order not paid: %s", order.Status)
			}
		})
	}
}
