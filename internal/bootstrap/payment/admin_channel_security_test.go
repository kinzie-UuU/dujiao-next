package paymentbootstrap

import (
	"context"
	"errors"
	"github.com/alicebob/miniredis/v2"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymenttransport "github.com/dujiao-next/internal/modules/payment/transport/http"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubPaymentTOTP struct{ err error }

func (s stubPaymentTOTP) VerifyChallengeCode(uint, string) error { return s.err }
func testStepUp(t *testing.T) (*paymentStepUp, *miniredis.Miniredis) {
	t.Helper()
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return &paymentStepUp{redis: client, totp: stubPaymentTOTP{}, key: "test-secret", ready: func(context.Context) error { return nil }, audit: func(uint) error { return nil }}, r
}
func TestPaymentStepUpReplayAndConcurrency(t *testing.T) {
	g, _ := testStepUp(t)
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Verify(context.Background(), 1, "123456") == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("same code accepted %d times", passed.Load())
	}
	if g.Verify(context.Background(), 2, "123456") != nil {
		t.Fatal("other admin should be independent")
	}
}
func TestPaymentStepUpFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		change func(*paymentStepUp, *miniredis.Miniredis)
		code   string
	}{
		{"missing", func(*paymentStepUp, *miniredis.Miniredis) {}, ""},
		{"recovery", func(*paymentStepUp, *miniredis.Miniredis) {}, "ABCD-EFGH"},
		{"no2fa_or_invalid", func(g *paymentStepUp, _ *miniredis.Miniredis) { g.totp = stubPaymentTOTP{errors.New("not enabled")} }, "123456"},
		{"redis_missing", func(g *paymentStepUp, _ *miniredis.Miniredis) { g.redis = nil }, "123456"},
		{"redis_down", func(_ *paymentStepUp, r *miniredis.Miniredis) { r.Close() }, "123456"},
		{"notice_unavailable", func(g *paymentStepUp, _ *miniredis.Miniredis) {
			g.ready = func(context.Context) error { return errors.New("disabled") }
		}, "123456"},
		{"audit_unavailable", func(g *paymentStepUp, _ *miniredis.Miniredis) {
			g.audit = func(uint) error { return errors.New("read only") }
		}, "123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, r := testStepUp(t)
			tt.change(g, r)
			if g.Verify(context.Background(), 1, tt.code) == nil {
				t.Fatal("must fail closed")
			}
		})
	}
}
func TestPaymentStepUpLimitsInvalidAttempts(t *testing.T) {
	g, r := testStepUp(t)
	g.totp = stubPaymentTOTP{errors.New("invalid")}
	for i := 0; i < 10; i++ {
		_ = g.Verify(context.Background(), 1, "000000")
	}
	g.totp = stubPaymentTOTP{}
	if g.Verify(context.Background(), 1, "123456") == nil {
		t.Fatal("rate limit bypassed")
	}
	r.FastForward(5 * time.Minute)
	if g.Verify(context.Background(), 1, "123456") != nil {
		t.Fatal("limiter did not expire")
	}
}
func TestPaymentStepUpReplayTTLExceedsAcceptanceWindow(t *testing.T) {
	g, r := testStepUp(t)
	if g.Verify(context.Background(), 1, "123456") != nil {
		t.Fatal("first code failed")
	}
	r.FastForward(91 * time.Second)
	if g.Verify(context.Background(), 1, "123456") == nil {
		t.Fatal("replay guard expired too soon")
	}
}

type pendingDebtStore struct {
	paymentcontract.Store
	tx      paymentcontract.Transaction
	exposed bool
}

func (s *pendingDebtStore) WithinTransaction(fn func(paymentcontract.Transaction) error) error {
	return fn(s.tx)
}
func (s *pendingDebtStore) GetLatestPendingByOrder(uint, time.Time) (*paymentdomain.Payment, error) {
	s.exposed = true
	return &paymentdomain.Payment{PayURL: "https://example.invalid/pay"}, nil
}

type pendingDebtOrders struct{ ordercontract.Store }

func (pendingDebtOrders) GetByIDForUpdateWithChildren(uint) (*orderdomain.Order, error) {
	return &orderdomain.Order{UserID: 1}, nil
}

type pendingDebtWallet struct {
	walletcontract.Repository
	balance int64
}

func (w pendingDebtWallet) GetAccountByUserIDForUpdate(uint) (*walletdomain.Account, error) {
	return &walletdomain.Account{Balance: money.FromDecimal(decimal.NewFromInt(w.balance))}, nil
}

type pendingDebtWalletTx struct{ repo walletcontract.Repository }

func (w pendingDebtWalletTx) Wallets() walletcontract.Repository { return w.repo }

type pendingDebtTx struct {
	paymentcontract.Transaction
	store   *pendingDebtStore
	balance int64
}

func (t pendingDebtTx) Orders() ordercontract.Store     { return pendingDebtOrders{} }
func (t pendingDebtTx) Payments() paymentcontract.Store { return t.store }
func (t pendingDebtTx) Wallets() walletcontract.Transaction {
	return pendingDebtWalletTx{pendingDebtWallet{balance: t.balance}}
}
func TestPendingLinkBlockedUntilDebtCleared(t *testing.T) {
	for _, balance := range []int64{-5, 0, 3} {
		s := &pendingDebtStore{}
		s.tx = pendingDebtTx{store: s, balance: balance}
		result, err := (pendingLookupAdapter{payments: s}).GetLatestPendingByOrder(1, time.Now())
		if balance < 0 {
			if !errors.Is(err, paymenttransport.ErrWalletOutstandingDebt) || result != nil || s.exposed {
				t.Fatalf("debt exposed pending payment: %+v %v", result, err)
			}
		} else if err != nil || result == nil || !s.exposed {
			t.Fatalf("cleared account blocked: %v", err)
		}
	}
}
