package application

import (
	"fmt"
	"strings"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	"github.com/dujiao-next/internal/constants"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	"github.com/dujiao-next/internal/shared/jsonmap"

	"github.com/shopspring/decimal"
)

// paymentExchangeRate 读取创建支付时存下的渠道换汇汇率快照
// （ProviderPayload["exchange_rate"]）。存在即说明这笔支付发生过法币换汇（渠道配置了
// target_currency + exchange_rate，见各 adapter 的 cfg.NeedsCurrencyConversion 分支），
// applyProviderPayment 回写后 payment.Amount/Currency 已经不是订单原始币种。返回零值
// 和 false 表示未换汇或快照缺失/无效。
func paymentExchangeRate(payment *paymentdomain.Payment) (decimal.Decimal, bool) {
	if payment == nil || payment.ProviderPayload == nil {
		return decimal.Zero, false
	}
	rateRaw, ok := payment.ProviderPayload["exchange_rate"]
	if !ok {
		return decimal.Zero, false
	}
	rateStr := strings.TrimSpace(fmt.Sprint(rateRaw))
	if rateStr == "" {
		return decimal.Zero, false
	}
	rate, err := decimal.NewFromString(rateStr)
	if err != nil || !rate.IsPositive() {
		return decimal.Zero, false
	}
	return rate, true
}

// paymentCoveredOrderAmount 按订单币种计算到账金额，扣除本版本由用户承担的手续费。
// 换汇使用创建时汇率快照，兼容仅保留 original_amount 的历史支付。
func paymentCoveredOrderAmount(payment *paymentdomain.Payment, callbackAmount decimal.Decimal) decimal.Decimal {
	if payment == nil {
		return decimal.Zero
	}
	covered := payment.Amount.Decimal
	if rate, ok := paymentExchangeRate(payment); ok {
		source := callbackAmount
		if source.IsZero() {
			source = payment.Amount.Decimal
		}
		covered = source.Div(rate)
	}
	// 原币金额是创建时的可信快照。结算币舍入不可逆，已验证实付与创建金额一致时
	// 优先使用它，避免 0.10 CNY -> 0.01 GBP -> 0.09 CNY 被误判欠付。
	if callbackAmount.IsZero() || callbackAmount.Equal(payment.Amount.Decimal) {
		if raw, ok := payment.ProviderPayload["original_amount"]; ok {
			if original, err := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(raw))); err == nil && original.IsPositive() {
				covered = original
			}
		}
	}
	fee := payment.FeeAmount.Decimal
	if fee.IsPositive() {
		covered = covered.Sub(fee)
	}
	return normalizeOrderAmount(covered)
}

// normalizeOrderAmount 归一化金额精度与下限。
func normalizeOrderAmount(amount decimal.Decimal) decimal.Decimal {
	normalized := amount.Round(2)
	if normalized.LessThan(decimal.Zero) {
		return decimal.Zero
	}
	return normalized
}

func pickFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shouldMarkFulfilling(order *orderdomain.Order) bool {
	if order == nil {
		return false
	}
	if len(order.Items) == 0 {
		return false
	}
	for _, item := range order.Items {
		fulfillmentType := strings.TrimSpace(item.FulfillmentType)
		if fulfillmentType == constants.FulfillmentTypeUpstream {
			return true
		}
	}
	return false
}

func shouldUseCNYPaymentCurrency(channel *paymentdomain.PaymentChannel) bool {
	if channel == nil {
		return false
	}
	providerType := strings.ToLower(strings.TrimSpace(channel.ProviderType))
	if providerType != constants.PaymentProviderOfficial {
		return false
	}
	channelType := strings.ToLower(strings.TrimSpace(channel.ChannelType))
	return channelType == constants.PaymentChannelTypeWechat || channelType == constants.PaymentChannelTypeAlipay
}

func validatePaymentAmountForChannel(amount decimal.Decimal, channel *paymentdomain.PaymentChannel) error {
	if channel == nil {
		return nil
	}
	amountOverflow20_2 := decimal.NewFromInt(1000000000000000000)
	if amount.GreaterThanOrEqual(amountOverflow20_2) {
		return ErrPaymentAmountTooLarge
	}
	minAmount := channel.MinAmount.Decimal
	maxAmount := channel.MaxAmount.Decimal
	if minAmount.GreaterThan(decimal.Zero) && amount.LessThan(minAmount) {
		return ErrPaymentAmountTooSmall
	}
	if maxAmount.GreaterThan(decimal.Zero) && amount.GreaterThan(maxAmount) {
		return ErrPaymentAmountTooLarge
	}
	return nil
}

func validatePaymentCurrencyForChannel(currency string, channel *paymentdomain.PaymentChannel) error {
	normalized := strings.ToUpper(strings.TrimSpace(currency))
	if !settingsapp.IsCurrencyCode(normalized) {
		return ErrPaymentCurrencyMismatch
	}
	if shouldUseCNYPaymentCurrency(channel) && normalized != constants.SiteCurrencyDefault {
		return ErrPaymentCurrencyMismatch
	}
	return nil
}

func (s *PaymentService) resolveExpireMinutes() int {
	return orderapp.ResolvePaymentExpireMinutes(s.settingService, s.expireMinutes)
}

func normalizePaymentStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func isPaymentStatusValid(status string) bool {
	switch status {
	case constants.PaymentStatusInitiated, constants.PaymentStatusPending, constants.PaymentStatusSuccess, constants.PaymentStatusFailed, constants.PaymentStatusExpired:
		return true
	default:
		return false
	}
}

func shouldAutoFulfill(order *orderdomain.Order) bool {
	if order == nil || len(order.Items) == 0 {
		return false
	}
	for _, item := range order.Items {
		if strings.TrimSpace(item.FulfillmentType) != constants.FulfillmentTypeAuto {
			return false
		}
	}
	return true
}

// isOrderFullyAutoFulfill 判断订单是否完全为自动交付。
// 父订单：所有子订单均满足 shouldAutoFulfill；单订单：自身满足 shouldAutoFulfill。
// 用于支付成功时跳过"已支付"邮件——自动交付会紧接着发送含卡密内容的"已完成"邮件，避免重复打扰。
func isOrderFullyAutoFulfill(order *orderdomain.Order) bool {
	if order == nil {
		return false
	}
	if len(order.Children) > 0 {
		for i := range order.Children {
			if !shouldAutoFulfill(&order.Children[i]) {
				return false
			}
		}
		return true
	}
	return shouldAutoFulfill(order)
}

func buildOrderSubject(order *orderdomain.Order) string {
	if order == nil {
		return ""
	}
	if len(order.Items) > 0 {
		title := pickOrderItemTitle(order.Items[0].TitleJSON)
		if title != "" {
			return title
		}
	}
	return order.OrderNo
}

func pickOrderItemTitle(title jsonmap.JSON) string {
	if title == nil {
		return ""
	}
	for _, key := range constants.SupportedLocales {
		if val, ok := title[key]; ok {
			if str, ok := val.(string); ok && strings.TrimSpace(str) != "" {
				return strings.TrimSpace(str)
			}
		}
	}
	for _, val := range title {
		if str, ok := val.(string); ok && strings.TrimSpace(str) != "" {
			return strings.TrimSpace(str)
		}
	}
	return ""
}
