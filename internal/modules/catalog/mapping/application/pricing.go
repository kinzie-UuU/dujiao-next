package application

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	hundred    = decimal.NewFromInt(100)
	pointOne   = decimal.NewFromFloat(0.1)
	roundScale = int32(2)
)

// convertCurrency 仅按汇率转换，不应用加价。
// 用于计算成本价：上游价格 × 汇率 = 本地币种成本（不含加价利润）。
// exchangeRate 为 0 或负数时视为 1（同币种）。
func convertCurrency(upstreamPrice, exchangeRate decimal.Decimal) decimal.Decimal {
	if exchangeRate.LessThanOrEqual(decimal.Zero) {
		exchangeRate = decimal.NewFromInt(1)
	}
	return upstreamPrice.Mul(exchangeRate)
}

// CalculateLocalPrice 先按汇率转换，再应用加价和取整。
// 计算链路：上游价格 × 汇率 → 加价 → 取整 → 本地售价。
// exchangeRate 为 0 或负数时视为 1（同币种）。
func CalculateLocalPrice(upstreamPrice, exchangeRate, markupPercent decimal.Decimal, roundingMode string) decimal.Decimal {
	if exchangeRate.LessThanOrEqual(decimal.Zero) {
		exchangeRate = decimal.NewFromInt(1)
	}
	converted := upstreamPrice.Mul(exchangeRate)
	return CalculateMarkedUpPrice(converted, markupPercent, roundingMode)
}

// CalculateMarkedUpPrice 根据加价百分比计算本地售价。
// markupPercent=100 表示上浮 100%，即 upstream × 2。
// markupPercent=0 时直接返回原价（向后兼容）。
func CalculateMarkedUpPrice(upstreamPrice, markupPercent decimal.Decimal, roundingMode string) decimal.Decimal {
	if markupPercent.IsZero() {
		return upstreamPrice.Round(roundScale)
	}

	// result = upstreamPrice * (1 + markupPercent / 100)
	multiplier := decimal.NewFromInt(1).Add(markupPercent.Div(hundred))
	result := upstreamPrice.Mul(multiplier)

	if result.IsNegative() {
		return decimal.Zero
	}

	switch roundingMode {
	case "ceil_int":
		// 向上取整到整数：12.01 → 13
		if result.Equal(result.Floor()) {
			return result
		}
		return result.Ceil()
	case "ceil_tenth":
		// 向上取整到 0.1：12.34 → 12.40
		scaled := result.Div(pointOne)
		if scaled.Equal(scaled.Floor()) {
			return result.Round(1)
		}
		return scaled.Ceil().Mul(pointOne)
	default:
		return result.Round(roundScale)
	}
}

// BackupQuotePricingInput contains caller-validated exchange-rate data and the
// supplier quote. The function does not fetch, persist, or choose a rate.
type BackupQuotePricingInput struct {
	CurrentPrice  decimal.Decimal
	SupplierPrice decimal.Decimal
	ExchangeRate  decimal.Decimal
	MarkupPercent decimal.Decimal
	MinimumPrice  decimal.Decimal
	Stock         int
	QuotedAt      time.Time
	Now           time.Time
	MaxAge        time.Duration
}

// CalculateBackupQuotePrice returns the current price unchanged and applied=false
// when the quote cannot safely drive pricing. All amounts are decimal, and the
// final local price is rounded upward to the next cent, including the floor.
// The caller must validate the rate's origin, currency and freshness separately.
func CalculateBackupQuotePrice(in BackupQuotePricingInput) (price decimal.Decimal, applied bool, err error) {
	price = in.CurrentPrice
	if !in.CurrentPrice.IsPositive() || !in.SupplierPrice.IsPositive() || !in.ExchangeRate.IsPositive() || in.MarkupPercent.IsNegative() || in.MinimumPrice.IsNegative() || in.Stock < 0 || in.Now.IsZero() || in.QuotedAt.IsZero() || in.MaxAge <= 0 || in.QuotedAt.After(in.Now) {
		return price, false, errors.New("invalid backup quote pricing input")
	}
	if in.Stock == 0 || in.Now.Sub(in.QuotedAt) > in.MaxAge {
		return price, false, nil
	}
	multiplier := decimal.NewFromInt(1).Add(in.MarkupPercent.Shift(-2))
	calculated := in.SupplierPrice.Mul(in.ExchangeRate).Mul(multiplier)
	if calculated.LessThan(in.MinimumPrice) {
		calculated = in.MinimumPrice
	}
	return calculated.Shift(2).Ceil().Shift(-2), true, nil
}

// AggregatedQuotePricingInput is a pure pricing snapshot. PrimaryPrice must be
// calculated by the existing primary supplier rule; callers must verify both
// quote identities, currencies and the exchange rate source before using it.
type AggregatedQuotePricingInput struct {
	PrimaryPrice       decimal.Decimal
	PrimaryQuotedAt    time.Time
	PrimaryMaxAge      time.Duration
	ExchangeRateAt     time.Time
	ExchangeRateMaxAge time.Duration
	Backup             BackupQuotePricingInput
}

// CalculateAggregatedQuotePrice gives priority to the higher of the primary
// rule price and a usable backup price. If backup or exchange data is missing,
// stale or invalid, a verified primary increase is allowed but the current
// price cannot be reduced. It neither fetches quotes nor writes catalog prices.
func CalculateAggregatedQuotePrice(in AggregatedQuotePricingInput) (price decimal.Decimal, backupUsable bool, err error) {
	price = in.Backup.CurrentPrice
	now := in.Backup.Now
	if !price.IsPositive() || !in.PrimaryPrice.IsPositive() || now.IsZero() || in.PrimaryQuotedAt.IsZero() || in.PrimaryMaxAge <= 0 || in.PrimaryQuotedAt.After(now) || now.Sub(in.PrimaryQuotedAt) > in.PrimaryMaxAge {
		return price, false, errors.New("invalid primary quote pricing input")
	}
	primary := in.PrimaryPrice.Shift(2).Ceil().Shift(-2)
	backup, usable, _ := CalculateBackupQuotePrice(in.Backup)
	if in.ExchangeRateAt.IsZero() || in.ExchangeRateMaxAge <= 0 || in.ExchangeRateAt.After(now) || now.Sub(in.ExchangeRateAt) > in.ExchangeRateMaxAge {
		usable = false
	}
	if !usable {
		if primary.GreaterThan(price) {
			price = primary
		}
		return price, false, nil
	}
	if backup.GreaterThan(primary) {
		return backup, true, nil
	}
	return primary, true, nil
}
