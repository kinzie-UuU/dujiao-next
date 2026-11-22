package application

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestCalculateMarkedUpPrice(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		markup   string
		rounding string
		expected string
	}{
		{"zero markup returns original", "10.00", "0", "none", "10.00"},
		{"50% markup", "10.00", "50", "none", "15.00"},
		{"100% markup doubles price", "10.00", "100", "none", "20.00"},
		{"200% markup triples price", "10.00", "200", "none", "30.00"},
		{"small price with markup", "0.01", "100", "none", "0.02"},
		{"fractional result rounds to 2 decimals", "10.00", "33", "none", "13.30"},
		{"negative markup floors at zero", "5.00", "-200", "none", "0"},
		{"ceil_int rounds up", "10.00", "23.4", "ceil_int", "13.00"},
		{"ceil_int exact integer stays", "10.00", "100", "ceil_int", "20"},
		{"ceil_tenth rounds up to 0.1", "10.00", "23.4", "ceil_tenth", "12.40"},
		{"ceil_tenth exact tenth stays", "10.00", "50", "ceil_tenth", "15.00"},
		{"zero price stays zero", "0.00", "100", "none", "0.00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := decimal.RequireFromString(tt.upstream)
			markup := decimal.RequireFromString(tt.markup)
			expected := decimal.RequireFromString(tt.expected)

			result := CalculateMarkedUpPrice(upstream, markup, tt.rounding)
			if !result.Equal(expected) {
				t.Errorf("CalculateMarkedUpPrice(%s, %s%%, %s) = %s, want %s",
					tt.upstream, tt.markup, tt.rounding, result.String(), tt.expected)
			}
		})
	}
}

func TestCalculateLocalPrice(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		rate     string
		markup   string
		rounding string
		expected string
	}{
		{"USD to CNY with markup", "1.00", "7.2", "50", "none", "10.80"},
		{"rate 1 same as no conversion", "10.00", "1", "50", "none", "15.00"},
		{"rate 0 treated as 1", "10.00", "0", "50", "none", "15.00"},
		{"negative rate treated as 1", "10.00", "-1", "50", "none", "15.00"},
		{"rate with no markup", "1.00", "7.2", "0", "none", "7.20"},
		{"rate with ceil_int", "1.00", "7.2", "50", "ceil_int", "11.00"},
		{"rate with ceil_tenth", "1.00", "7.35", "0", "ceil_tenth", "7.35"},
		{"rate with ceil_tenth fractional", "1.00", "7.34", "1", "ceil_tenth", "7.50"},
		{"high precision rate", "100.00", "0.138462", "0", "none", "13.85"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := decimal.RequireFromString(tt.upstream)
			rate := decimal.RequireFromString(tt.rate)
			markup := decimal.RequireFromString(tt.markup)
			expected := decimal.RequireFromString(tt.expected)

			result := CalculateLocalPrice(upstream, rate, markup, tt.rounding)
			if !result.Equal(expected) {
				t.Errorf("CalculateLocalPrice(%s, rate=%s, %s%%, %s) = %s, want %s",
					tt.upstream, tt.rate, tt.markup, tt.rounding, result.String(), tt.expected)
			}
		})
	}
}

func TestCalculateBackupQuotePrice(t *testing.T) {
	d := decimal.RequireFromString
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	base := BackupQuotePricingInput{
		CurrentPrice: d("108"), SupplierPrice: d("5.741"), ExchangeRate: d("7.12"),
		MarkupPercent: d("25"), MinimumPrice: d("10"), Stock: 1,
		QuotedAt: now.Add(-time.Minute), Now: now, MaxAge: time.Minute,
	}
	tests := []struct {
		name             string
		edit             func(*BackupQuotePricingInput)
		want             string
		applied, wantErr bool
	}{
		{"exact decimal and ceiling", func(*BackupQuotePricingInput) {}, "51.10", true, false},
		{"minimum rounds upward", func(in *BackupQuotePricingInput) { in.MinimumPrice = d("80.001") }, "80.01", true, false},
		{"no stock preserves price", func(in *BackupQuotePricingInput) { in.Stock = 0 }, "108", false, false},
		{"expired preserves price", func(in *BackupQuotePricingInput) { in.QuotedAt = in.QuotedAt.Add(-time.Nanosecond) }, "108", false, false},
		{"zero quote preserves price", func(in *BackupQuotePricingInput) { in.SupplierPrice = decimal.Zero }, "108", false, true},
		{"missing exchange rate preserves price", func(in *BackupQuotePricingInput) { in.ExchangeRate = decimal.Zero }, "108", false, true},
		{"negative markup preserves price", func(in *BackupQuotePricingInput) { in.MarkupPercent = d("-1") }, "108", false, true},
		{"negative floor preserves price", func(in *BackupQuotePricingInput) { in.MinimumPrice = d("-1") }, "108", false, true},
		{"invalid stock preserves price", func(in *BackupQuotePricingInput) { in.Stock = -1 }, "108", false, true},
		{"future quote preserves price", func(in *BackupQuotePricingInput) { in.QuotedAt = in.Now.Add(time.Nanosecond) }, "108", false, true},
		{"missing clock preserves price", func(in *BackupQuotePricingInput) { in.Now = time.Time{} }, "108", false, true},
		{"invalid age preserves price", func(in *BackupQuotePricingInput) { in.MaxAge = 0 }, "108", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.edit(&in)
			got, applied, err := CalculateBackupQuotePrice(in)
			if !got.Equal(d(tt.want)) || applied != tt.applied || (err != nil) != tt.wantErr {
				t.Fatalf("price=%s applied=%v err=%v; want price=%s applied=%v error=%v", got, applied, err, tt.want, tt.applied, tt.wantErr)
			}
		})
	}
}

func TestCalculateAggregatedQuotePrice(t *testing.T) {
	d := decimal.RequireFromString
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	base := AggregatedQuotePricingInput{
		PrimaryPrice: d("20"), PrimaryQuotedAt: now.Add(-time.Minute), PrimaryMaxAge: time.Minute,
		ExchangeRateAt: now.Add(-time.Minute), ExchangeRateMaxAge: time.Minute,
		Backup: BackupQuotePricingInput{CurrentPrice: d("30"), SupplierPrice: d("4"), ExchangeRate: d("6"),
			MarkupPercent: d("25"), MinimumPrice: d("1"), Stock: 2, QuotedAt: now, Now: now, MaxAge: time.Minute},
	}
	tests := []struct {
		name            string
		edit            func(*AggregatedQuotePricingInput)
		want            string
		usable, wantErr bool
	}{
		{"higher backup", func(*AggregatedQuotePricingInput) {}, "30", true, false},
		{"higher primary", func(in *AggregatedQuotePricingInput) { in.PrimaryPrice = d("32.001") }, "32.01", true, false},
		{"both fresh can reduce", func(in *AggregatedQuotePricingInput) { in.Backup.SupplierPrice = d("2") }, "20", true, false},
		{"no backup stock cannot reduce", func(in *AggregatedQuotePricingInput) { in.Backup.Stock = 0 }, "30", false, false},
		{"no backup stock permits primary increase", func(in *AggregatedQuotePricingInput) { in.Backup.Stock = 0; in.PrimaryPrice = d("35") }, "35", false, false},
		{"expired backup cannot reduce", func(in *AggregatedQuotePricingInput) { in.Backup.QuotedAt = now.Add(-time.Minute - time.Nanosecond) }, "30", false, false},
		{"invalid backup cannot reduce", func(in *AggregatedQuotePricingInput) { in.Backup.SupplierPrice = decimal.Zero }, "30", false, false},
		{"invalid exchange rate cannot reduce", func(in *AggregatedQuotePricingInput) { in.Backup.ExchangeRate = decimal.Zero }, "30", false, false},
		{"expired exchange rate cannot reduce", func(in *AggregatedQuotePricingInput) { in.ExchangeRateAt = now.Add(-time.Minute - time.Nanosecond) }, "30", false, false},
		{"future exchange rate cannot reduce", func(in *AggregatedQuotePricingInput) { in.ExchangeRateAt = now.Add(time.Nanosecond) }, "30", false, false},
		{"expired primary holds current", func(in *AggregatedQuotePricingInput) { in.PrimaryQuotedAt = now.Add(-time.Minute - time.Nanosecond) }, "30", false, true},
		{"future primary holds current", func(in *AggregatedQuotePricingInput) { in.PrimaryQuotedAt = now.Add(time.Nanosecond) }, "30", false, true},
		{"invalid primary holds current", func(in *AggregatedQuotePricingInput) { in.PrimaryPrice = decimal.Zero }, "30", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.edit(&in)
			got, usable, err := CalculateAggregatedQuotePrice(in)
			if !got.Equal(d(tt.want)) || usable != tt.usable || (err != nil) != tt.wantErr {
				t.Fatalf("price=%s backupUsable=%v err=%v; want %s %v %v", got, usable, err, tt.want, tt.usable, tt.wantErr)
			}
		})
	}
}
