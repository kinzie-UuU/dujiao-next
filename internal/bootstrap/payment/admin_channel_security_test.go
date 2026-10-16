package paymentbootstrap

import (
	"context"
	"errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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
