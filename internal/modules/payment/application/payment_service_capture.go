package application

import (
	"context"
	"errors"
	"fmt"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	"github.com/shopspring/decimal"
	"regexp"
	"strings"
	"time"

	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

// CapturePaymentInput 捕获支付输入。
type CapturePaymentInput struct {
	PaymentID uint
	Context   context.Context
}

const manualQRProviderType = "manual_qr"

// ConfirmManualPayment 由管理员在核实到账后确认人工二维码支付。
func (s *PaymentService) ConfirmManualPayment(paymentID uint, evidence ...paymentdomain.ManualPaymentEvidence) (*paymentdomain.Payment, error) {
	payment, err := s.paymentRepo.GetByID(paymentID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if payment == nil {
		return nil, ErrPaymentNotFound
	}
	if !strings.EqualFold(strings.TrimSpace(payment.ProviderType), manualQRProviderType) {
		return nil, ErrPaymentProviderNotSupported
	}
	if len(evidence) != 1 {
		return nil, ErrPaymentInvalid
	}
	proof := evidence[0]
	transactionID := strings.TrimSpace(proof.TransactionID)
	if !proof.PayerVerified || (transactionID != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{9,127}$`).MatchString(transactionID)) {
		return nil, ErrPaymentInvalid
	}
	expectedAmount, expectedCurrency, settlementErr := manualSettlement(payment)
	if settlementErr != nil {
		return nil, settlementErr
	}
	received, amountErr := decimal.NewFromString(proof.Amount)
	if amountErr != nil || !received.Equal(expectedAmount) {
		return nil, ErrPaymentAmountMismatch
	}
	if proof.Currency != expectedCurrency {
		return nil, ErrPaymentCurrencyMismatch
	}
	receiptKey := strings.ToLower(strings.TrimSpace(payment.ChannelType)) + ":" + strings.ToLower(transactionID)
	// Supplied platform receipts retain cross-payment uniqueness. Without one,
	// reserve an internal confirmation key only; never invent a platform reference.
	if err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		locked, err := tx.Payments().GetByIDForUpdate(paymentID)
		if err != nil || locked == nil {
			return ErrPaymentUpdateFailed
		}
		if locked.ProviderType != manualQRProviderType || locked.ChannelID != payment.ChannelID || locked.ChannelType != payment.ChannelType {
			return ErrPaymentInvalid
		}
		lockedAmount, lockedCurrency, err := manualSettlement(locked)
		if err != nil {
			return err
		}
		if !received.Equal(lockedAmount) {
			return ErrPaymentAmountMismatch
		}
		if proof.Currency != lockedCurrency {
			return ErrPaymentCurrencyMismatch
		}
		if locked.Status != constants.PaymentStatusSuccess && locked.Status != constants.PaymentStatusPending && locked.Status != constants.PaymentStatusInitiated && locked.Status != constants.PaymentStatusExpired {
			return ErrPaymentStatusInvalid
		}
		if strings.TrimSpace(proof.TransactionID) == "" {
			// Preserve a previous claim on retries, including legacy receipt claims.
			transactionID = locked.ProviderRef
			receiptKey = fmt.Sprintf("admin-confirm:%d", paymentID)
			if locked.ManualReceiptKey != nil {
				receiptKey = *locked.ManualReceiptKey
			} else {
				transactionID = ""
			}
		}
		payment = locked
		if locked.Status == constants.PaymentStatusSuccess {
			if locked.ManualReceiptKey == nil || *locked.ManualReceiptKey != receiptKey || locked.ProviderRef != transactionID {
				return ErrPaymentInvalid
			}
			return nil
		}
		{
			risk, ok := tx.Payments().(interface{ ManualPaymentRiskAllowed(uint) (bool, error) })
			if !ok {
				return ErrPaymentStatusInvalid
			}
			allowed, err := risk.ManualPaymentRiskAllowed(paymentID)
			if err != nil || !allowed {
				return ErrPaymentStatusInvalid
			}
		}
		if locked.ManualReceiptKey != nil {
			if *locked.ManualReceiptKey != receiptKey {
				return ErrPaymentInvalid
			}
			return nil
		}
		if locked.Status == constants.PaymentStatusSuccess {
			return ErrPaymentStatusInvalid
		}
		if transactionID != "" {
			previous, err := tx.Payments().GetLatestByProviderRef(transactionID)
			if err != nil {
				return ErrPaymentUpdateFailed
			}
			if previous != nil && previous.ID != paymentID {
				return ErrPaymentInvalid
			}
		}
		locked.ManualReceiptKey = &receiptKey
		locked.ProviderRef = transactionID
		if err := tx.Payments().Update(locked); err != nil {
			return ErrPaymentInvalid
		}
		return nil
	}); err != nil {
		return nil, err
	}
	payment.ProviderRef = transactionID
	if payment.Status == constants.PaymentStatusSuccess {
		return payment, nil
	}
	if payment.Status != constants.PaymentStatusPending && payment.Status != constants.PaymentStatusInitiated && payment.Status != constants.PaymentStatusExpired {
		return nil, ErrPaymentStatusInvalid
	}

	verification := "manual_platform_record_review"
	if strings.TrimSpace(proof.TransactionID) == "" {
		verification = "admin_attestation"
	}
	paidAt := time.Now()
	return s.HandleCallback(PaymentCallbackInput{
		PaymentID:   payment.ID,
		ChannelID:   payment.ChannelID,
		Status:      constants.PaymentStatusSuccess,
		ProviderRef: payment.ProviderRef,
		Amount:      payment.Amount,
		Currency:    payment.Currency,
		PaidAt:      &paidAt,
		Payload: jsonmap.JSON{
			"manual_confirmed_by_admin":    true,
			"manual_confirmed_at":          paidAt.Format(time.RFC3339),
			"payer_verified_by_admin":      true,
			"receipt_verification":         verification,
			"verified_settlement_amount":   proof.Amount,
			"verified_settlement_currency": proof.Currency,
		},
	})
}

func (s *PaymentService) CapturePayment(input CapturePaymentInput) (*paymentdomain.Payment, error) {
	if input.PaymentID == 0 {
		return nil, ErrPaymentInvalid
	}
	payment, err := s.paymentRepo.GetByID(input.PaymentID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if payment == nil {
		return nil, ErrPaymentNotFound
	}
	if payment.Status == constants.PaymentStatusSuccess {
		return payment, nil
	}

	// A pending product payment must not capture external funds while in debt.
	// Recharge payments have no order and remain available to clear that debt.
	if payment.OrderID != 0 {
		if err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
			order, err := tx.Orders().GetByIDForUpdateWithChildren(payment.OrderID)
			if err != nil {
				return err
			}
			if order == nil {
				return orderapp.ErrOrderNotFound
			}
			return walletapp.CheckPurchaseAllowed(tx.Wallets(), order.UserID)
		}); err != nil {
			return nil, err
		}
	}
	channel, err := s.channelRepo.GetByID(payment.ChannelID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if channel == nil {
		return nil, ErrPaymentChannelNotFound
	}

	providerType := strings.ToLower(strings.TrimSpace(channel.ProviderType))
	if providerType != constants.PaymentProviderOfficial {
		return nil, ErrPaymentProviderNotSupported
	}
	if strings.TrimSpace(payment.ProviderRef) == "" {
		return nil, ErrPaymentInvalid
	}

	// 统一通过 Registry 路由。Registry.Lookup 会返回 channel 对应的 adapter,
	// 如果 adapter 不实现 Capturer,type assertion 失败,返回 ErrPaymentProviderNotSupported。
	// 因此无需在此显式检查 channel 是否支持 capture。
	return s.captureViaRegistry(input, payment, channel)
}

// captureViaRegistry 通过 PaymentProviderRegistry 路由调用 QueryPayment。
// stripe + paypal + wechat 实现了 paymentcontract.GatewayCapturer 接口,其它 channel
// (alipay / epay / epusdt / bepusdt / tokenpay / okpay) 仅实现 webhook 回调,
// type assertion 失败时返回 ErrPaymentProviderNotSupported。
func (s *PaymentService) captureViaRegistry(input CapturePaymentInput, payment *paymentdomain.Payment, channel *paymentdomain.PaymentChannel) (*paymentdomain.Payment, error) {
	logger.Infow("payment_capture_via_registry",
		"payment_id", payment.ID,
		"provider_type", channel.ProviderType,
		"channel_type", channel.ChannelType,
	)
	if s.paymentProviderRegistry == nil {
		return nil, ErrPaymentProviderNotSupported
	}
	p, ok := s.paymentProviderRegistry.Lookup(channel.ProviderType, channel.ChannelType)
	if !ok {
		return nil, ErrPaymentProviderNotSupported
	}
	capturer, ok := p.(paymentcontract.GatewayCapturer)
	if !ok {
		logger.Warnw("payment_provider_capture_not_implemented",
			"provider_type", channel.ProviderType,
			"channel_type", channel.ChannelType,
		)
		return nil, ErrPaymentProviderNotSupported
	}

	// 第二参数是 interactionMode,不是 channelType。stripe/wechat adapter
	// 会拒绝任何非法 mode,传 channelType 会导致  一律 ErrConfigInvalid。
	if err := capturer.ValidateConfig(channel.ConfigJSON, channel.InteractionMode); err != nil {
		return nil, mapProviderErrorToService(err)
	}

	ctx, cancel := detachOutboundRequestContext(input.Context)
	defer cancel()

	queryResult, err := capturer.QueryPayment(ctx, channel.ConfigJSON, payment.ProviderRef)
	if err != nil {
		return nil, mapProviderErrorToService(err)
	}

	payload := jsonmap.JSON{}
	if queryResult.Payload != nil {
		payload = queryResult.Payload
	}
	status := strings.TrimSpace(queryResult.Status)
	if status == "" {
		status = constants.PaymentStatusPending
	}

	callbackInput := PaymentCallbackInput{
		PaymentID:   payment.ID,
		ChannelID:   channel.ID,
		Status:      status,
		ProviderRef: pickFirstNonEmpty(queryResult.ProviderRef, payment.ProviderRef),
		Amount:      queryResult.Amount,
		Currency:    strings.ToUpper(strings.TrimSpace(queryResult.Currency)),
		PaidAt:      queryResult.PaidAt,
		Payload:     payload,
	}
	return s.HandleCallback(callbackInput)
}

// mapProviderErrorToService 把 provider.ErrXxx 转换为 service 层的 ErrPaymentXxx。
func mapProviderErrorToService(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, paymentcontract.ErrGatewayConfigInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentChannelConfigInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayRequestFailed), errors.Is(err, paymentcontract.ErrGatewayAuthFailed):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	case errors.Is(err, paymentcontract.ErrGatewayResponseInvalid), errors.Is(err, paymentcontract.ErrGatewaySignatureInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayResponseInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayUnsupportedChannel), errors.Is(err, paymentcontract.ErrGatewayProviderNotFound):
		return ErrPaymentProviderNotSupported
	default:
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	}
}

// verifyManualCapture runs under the capture transaction, closing the gap between
// receipt reservation and delivery/wallet credit and blocking alternate callbacks.
func verifyManualCapture(store paymentcontract.Store, payment *paymentdomain.Payment, status string, input PaymentCallbackInput) error {
	if payment == nil || payment.ProviderType != manualQRProviderType || status != constants.PaymentStatusSuccess || payment.Status == constants.PaymentStatusSuccess {
		return nil
	}
	if payment.ManualReceiptKey == nil || input.ProviderRef != payment.ProviderRef || input.Payload["payer_verified_by_admin"] != true {
		return ErrPaymentInvalid
	}
	risk, ok := store.(interface{ ManualPaymentRiskAllowed(uint) (bool, error) })
	if !ok {
		return ErrPaymentStatusInvalid
	}
	allowed, err := risk.ManualPaymentRiskAllowed(payment.ID)
	if err != nil || !allowed {
		return ErrPaymentStatusInvalid
	}
	return nil
}

// manualSettlement uses the immutable collection quote, never CNY ledger values
// as proof of a TWD or USDT transfer. Missing or inconsistent quotes fail closed.
func manualSettlement(payment *paymentdomain.Payment) (decimal.Decimal, string, error) {
	if payment.ChannelType == "wechat" || payment.ChannelType == "alipay" {
		if payment.Currency != "CNY" {
			return decimal.Zero, "", ErrPaymentCurrencyMismatch
		}
		return payment.Amount.Decimal, payment.Currency, nil
	}
	key, currency, ledgerKey := "twqr_quote", "TWD", "cnyAmount"
	if payment.ChannelType == "binance_usdt" {
		key, currency, ledgerKey = "crypto_quote", "USDT", "orderAmount"
	} else if payment.ChannelType != "twqr" {
		return decimal.Zero, "", ErrPaymentProviderNotSupported
	}
	var quote map[string]interface{}
	switch value := payment.ProviderPayload[key].(type) {
	case map[string]interface{}:
		quote = value
	case jsonmap.JSON:
		quote = map[string]interface{}(value)
	}
	if quote == nil || quote["currency"] != currency || payment.Currency != "CNY" {
		return decimal.Zero, "", ErrPaymentInvalid
	}
	amount, err := decimal.NewFromString(fmt.Sprint(quote["amount"]))
	if err != nil || !amount.IsPositive() {
		return decimal.Zero, "", ErrPaymentInvalid
	}
	ledger, err := decimal.NewFromString(fmt.Sprint(quote[ledgerKey]))
	if err != nil || !ledger.Equal(payment.Amount.Decimal) {
		return decimal.Zero, "", ErrPaymentAmountMismatch
	}
	rateKey := "exchangeRate"
	if currency == "USDT" {
		rateKey = "rate"
	}
	rate, err := decimal.NewFromString(fmt.Sprint(quote[rateKey]))
	if err != nil || !rate.IsPositive() {
		return decimal.Zero, "", ErrPaymentInvalid
	}
	converted := ledger.Mul(rate).Round(0)
	if currency == "USDT" {
		converted = ledger.Div(rate).Round(2)
	}
	if !amount.Equal(converted) {
		return decimal.Zero, "", ErrPaymentAmountMismatch
	}
	return amount, currency, nil
}

// populateManualSettlement exposes only validated quote totals to the admin UI.
// Raw gateway payloads remain redacted by the HTTP layer.
func populateManualSettlement(payment *paymentdomain.Payment) {
	payment.SettlementAmount, payment.SettlementCurrency = "", ""
	if payment.ProviderType != manualQRProviderType {
		return
	}
	amount, currency, err := manualSettlement(payment)
	if err == nil {
		payment.SettlementAmount, payment.SettlementCurrency = amount.String(), currency
	}
}
