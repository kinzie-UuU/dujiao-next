package integrationtest

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

type revocationCacheHook struct{ values map[string]string }

func (h *revocationCacheHook) DialHook(next redis.DialHook) redis.DialHook       { return next }
func (h *revocationCacheHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook { return h.process }
func (h *revocationCacheHook) ProcessPipelineHook(_ redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, c := range cmds {
			if err := h.process(ctx, c); err != nil {
				return err
			}
		}
		return nil
	}
}
func (h *revocationCacheHook) process(_ context.Context, c redis.Cmder) error {
	a := c.Args()
	switch c.Name() {
	case "set":
		value := fmt.Sprint(a[2])
		if b, ok := a[2].([]byte); ok {
			value = string(b)
		}
		h.values[fmt.Sprint(a[1])] = value
		c.(*redis.StatusCmd).SetVal("OK")
	case "get":
		v, ok := h.values[fmt.Sprint(a[1])]
		if !ok {
			c.SetErr(redis.Nil)
			return redis.Nil
		}
		c.(*redis.StringCmd).SetVal(v)
	case "del":
		delete(h.values, fmt.Sprint(a[1]))
		c.(*redis.IntCmd).SetVal(1)
	default:
		return fmt.Errorf("unexpected redis command %s", c.Name())
	}
	return nil
}
func TestEnableUser2FARevokesCachedSessions(t *testing.T) {
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: "127.0.0.1", Port: 1, Prefix: "audit-isolated"}); err != nil {
		t.Fatal(err)
	}
	cache.Client().AddHook(&revocationCacheHook{values: map[string]string{}})
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	svc, repo, _ := newUserTOTPTestService(t)
	a := createUserTOTPTestUser(t, repo, "audit-cache@example.invalid")
	ctx := context.Background()
	if err := cache.SetUserAuthState(ctx, cache.BuildUserAuthState(a)); err != nil {
		t.Fatal(err)
	}
	setup, err := svc.Setup(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Enable(a.ID, code); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetByID(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, hit, err := cache.GetUserAuthState(ctx, a.ID)
	if err != nil || hit {
		t.Fatalf("old cache retained %v %v", hit, err)
	}
	if updated.TokenVersion != a.TokenVersion+1 {
		t.Fatalf("token version did not advance: %d", updated.TokenVersion)
	}

}
