package backupservice

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PreflightDecision only advises the caller. A backup may be purchased only
// after a transaction rechecks the evidence and claims the paid order before
// any primary or backup supplier POST. This function does not create a claim,
// reserve inventory, set a price, or establish a hard supplier charge limit.
type PreflightDecision string

const (
	KeepPrimary PreflightDecision = "keep_primary"
	AllowBackup PreflightDecision = "allow_backup"
)

// BackupPreflight contains trusted observations, not values from a customer
// request. Callers must verify and bind supplier identities, exact order and
// product/SKU, quotes, and the business policy under the claim transaction.
type BackupPreflight struct {
	Now                     time.Time
	MaxQuoteAge             time.Duration
	Quantity                int
	NeverSubmitted          bool // affirmative durable evidence, not absence of a receipt
	PrimaryIdentityVerified bool
	PrimaryResponseValid    bool // successful authenticated and complete inventory response
	ExpectedPrimarySKUID    uint
	ObservedPrimarySKUID    uint
	PrimaryStockKnown       bool
	PrimaryStock            int
	PrimaryObservedAt       time.Time
	BackupGateEnabled       bool
	BackupContractVerified  bool
	BackupIdentityVerified  bool
	ExpectedBackupProductID string
	ObservedBackupProductID string
	BackupResponseValid     bool
	BackupStockKnown        bool
	BackupStock             int
	BackupCurrency          string
	BackupObservedAt        time.Time
	BackupUnitPriceUSD      decimal.Decimal
	MaxOrderAmountUSD       decimal.Decimal // pre-send check, not an atomic upstream limit
	PricePolicyApproved     bool
	FXVerified              bool
	FXUSDToCNY              decimal.Decimal
	FXObservedAt            time.Time
}

func isFreshObservation(now, observed time.Time, age time.Duration) bool {
	return !now.IsZero() && !observed.IsZero() && age > 0 && !observed.After(now) && now.Sub(observed) <= age
}

func validPreflightAmount(v decimal.Decimal) bool {
	// Bound exponents and precision before decimal multiplication or comparison;
	// supplier data must not trigger unbounded big-integer allocations.
	return v.Exponent() >= -8 && v.Exponent() <= 18 && v.Coefficient().BitLen() <= 128 && v.IsPositive()
}

// EvaluateBackupPreflight keeps the primary path for absent, malformed,
// ambiguous, stale, mismatched, or unauthenticated observations. It returns
// AllowBackup only for explicit shortage of the exact primary SKU for the
// entire order and a sufficiently stocked, validated backup under policy.
func EvaluateBackupPreflight(in BackupPreflight) PreflightDecision {
	if in.Quantity <= 0 || !in.NeverSubmitted || !in.PrimaryIdentityVerified || !in.PrimaryResponseValid ||
		in.ExpectedPrimarySKUID == 0 || in.ObservedPrimarySKUID != in.ExpectedPrimarySKUID ||
		!in.PrimaryStockKnown || in.PrimaryStock < 0 || in.PrimaryStock >= in.Quantity ||
		!isFreshObservation(in.Now, in.PrimaryObservedAt, in.MaxQuoteAge) {
		return KeepPrimary
	}
	if !in.BackupGateEnabled || !in.BackupContractVerified || !in.BackupIdentityVerified ||
		strings.TrimSpace(in.ExpectedBackupProductID) == "" || in.ObservedBackupProductID != in.ExpectedBackupProductID ||
		!in.BackupResponseValid || !in.BackupStockKnown || in.BackupStock < in.Quantity ||
		in.BackupCurrency != "USD" || !isFreshObservation(in.Now, in.BackupObservedAt, in.MaxQuoteAge) ||
		!validPreflightAmount(in.BackupUnitPriceUSD) || !validPreflightAmount(in.MaxOrderAmountUSD) ||
		!in.PricePolicyApproved || !in.FXVerified || !validPreflightAmount(in.FXUSDToCNY) ||
		!isFreshObservation(in.Now, in.FXObservedAt, in.MaxQuoteAge) {
		return KeepPrimary
	}
	if in.BackupUnitPriceUSD.Mul(decimal.NewFromInt(int64(in.Quantity))).GreaterThan(in.MaxOrderAmountUSD) {
		return KeepPrimary
	}
	return AllowBackup
}
