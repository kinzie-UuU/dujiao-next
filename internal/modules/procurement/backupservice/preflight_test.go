package backupservice

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func validBackupPreflight() BackupPreflight {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	return BackupPreflight{
		Now: now, MaxQuoteAge: time.Minute, Quantity: 2, NeverSubmitted: true,
		PrimaryIdentityVerified: true, PrimaryResponseValid: true, ExpectedPrimarySKUID: 17, ObservedPrimarySKUID: 17,
		PrimaryStockKnown: true, PrimaryStock: 1, PrimaryObservedAt: now.Add(-time.Second),
		BackupGateEnabled: true, BackupContractVerified: true, BackupIdentityVerified: true,
		ExpectedBackupProductID: "reviewed-id", ObservedBackupProductID: "reviewed-id", BackupResponseValid: true,
		BackupStockKnown: true, BackupStock: 2, BackupCurrency: "USD", BackupObservedAt: now.Add(-time.Second),
		BackupUnitPriceUSD: decimal.RequireFromString("1.25"), MaxOrderAmountUSD: decimal.RequireFromString("2.50"),
		PricePolicyApproved: true, FXVerified: true, FXUSDToCNY: decimal.RequireFromString("7.2"), FXObservedAt: now.Add(-time.Second),
	}
}

func TestBackupPreflightExplicitWholeOrderShortage(t *testing.T) {
	in := validBackupPreflight()
	if got := EvaluateBackupPreflight(in); got != AllowBackup {
		t.Fatalf("valid shortage = %q", got)
	}
	in.PrimaryStock = 2
	if got := EvaluateBackupPreflight(in); got != KeepPrimary {
		t.Fatalf("enough primary inventory = %q", got)
	}
	in.PrimaryStock = 0
	if got := EvaluateBackupPreflight(in); got != AllowBackup {
		t.Fatalf("zero primary inventory = %q", got)
	}
}

func TestBackupPreflightRejectsUncertainConditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*BackupPreflight)
	}{
		{"already submitted or unknown", func(s *BackupPreflight) { s.NeverSubmitted = false }},
		{"zero quantity", func(s *BackupPreflight) { s.Quantity = 0 }},
		{"unverified primary identity", func(s *BackupPreflight) { s.PrimaryIdentityVerified = false }},
		{"failed primary response", func(s *BackupPreflight) { s.PrimaryResponseValid = false }},
		{"missing primary SKU", func(s *BackupPreflight) { s.ExpectedPrimarySKUID, s.ObservedPrimarySKUID = 0, 0 }},
		{"different primary SKU", func(s *BackupPreflight) { s.ObservedPrimarySKUID++ }},
		{"unknown primary stock", func(s *BackupPreflight) { s.PrimaryStockKnown = false }},
		{"negative primary stock", func(s *BackupPreflight) { s.PrimaryStock = -1 }},
		{"stale primary quote", func(s *BackupPreflight) { s.PrimaryObservedAt = s.Now.Add(-s.MaxQuoteAge - time.Nanosecond) }},
		{"future primary quote", func(s *BackupPreflight) { s.PrimaryObservedAt = s.Now.Add(time.Second) }},
		{"zero clock", func(s *BackupPreflight) { s.Now = time.Time{} }},
		{"zero freshness window", func(s *BackupPreflight) { s.MaxQuoteAge = 0 }},
		{"gate closed", func(s *BackupPreflight) { s.BackupGateEnabled = false }},
		{"contract unverified", func(s *BackupPreflight) { s.BackupContractVerified = false }},
		{"backup identity unverified", func(s *BackupPreflight) { s.BackupIdentityVerified = false }},
		{"missing backup product", func(s *BackupPreflight) { s.ExpectedBackupProductID, s.ObservedBackupProductID = "", "" }},
		{"different backup product", func(s *BackupPreflight) { s.ObservedBackupProductID = "different" }},
		{"failed backup response", func(s *BackupPreflight) { s.BackupResponseValid = false }},
		{"unknown backup stock", func(s *BackupPreflight) { s.BackupStockKnown = false }},
		{"insufficient backup stock", func(s *BackupPreflight) { s.BackupStock = 1 }},
		{"wrong currency", func(s *BackupPreflight) { s.BackupCurrency = "CNY" }},
		{"stale backup quote", func(s *BackupPreflight) { s.BackupObservedAt = s.Now.Add(-s.MaxQuoteAge - time.Nanosecond) }},
		{"zero price", func(s *BackupPreflight) { s.BackupUnitPriceUSD = decimal.Zero }},
		{"unbounded price exponent", func(s *BackupPreflight) { s.BackupUnitPriceUSD = decimal.New(1, 1000000) }},
		{"zero cap", func(s *BackupPreflight) { s.MaxOrderAmountUSD = decimal.Zero }},
		{"unbounded cap exponent", func(s *BackupPreflight) { s.MaxOrderAmountUSD = decimal.New(1, 1000000) }},
		{"entire order above cap", func(s *BackupPreflight) { s.MaxOrderAmountUSD = decimal.RequireFromString("2.49") }},
		{"price policy not approved", func(s *BackupPreflight) { s.PricePolicyApproved = false }},
		{"unverified exchange rate", func(s *BackupPreflight) { s.FXVerified = false }},
		{"nonpositive exchange rate", func(s *BackupPreflight) { s.FXUSDToCNY = decimal.Zero }},
		{"unbounded exchange rate exponent", func(s *BackupPreflight) { s.FXUSDToCNY = decimal.New(1, -1000000) }},
		{"stale exchange rate", func(s *BackupPreflight) { s.FXObservedAt = s.Now.Add(-s.MaxQuoteAge - time.Nanosecond) }},
		{"future exchange rate", func(s *BackupPreflight) { s.FXObservedAt = s.Now.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validBackupPreflight()
			tc.change(&in)
			if got := EvaluateBackupPreflight(in); got != KeepPrimary {
				t.Fatalf("decision = %q", got)
			}
		})
	}
}

func TestBackupPreflightAcceptsFreshnessBoundary(t *testing.T) {
	in := validBackupPreflight()
	in.PrimaryObservedAt, in.BackupObservedAt, in.FXObservedAt = in.Now.Add(-in.MaxQuoteAge), in.Now.Add(-in.MaxQuoteAge), in.Now.Add(-in.MaxQuoteAge)
	if got := EvaluateBackupPreflight(in); got != AllowBackup {
		t.Fatalf("freshness boundary = %q", got)
	}
}
