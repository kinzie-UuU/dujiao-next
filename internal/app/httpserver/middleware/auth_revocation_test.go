package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	admincontract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	adminauthapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	usercontract "github.com/dujiao-next/internal/modules/identity/user/contract"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userauthapp "github.com/dujiao-next/internal/modules/identity/userauth/application"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

// This hook keeps the test independent of a running Redis server. The repositories
// below complete a revocation after a database read but before its result returns.
type authRaceCache struct {
	mu     sync.Mutex
	values map[string]string
}

func (h *authRaceCache) DialHook(next redis.DialHook) redis.DialHook       { return next }
func (h *authRaceCache) ProcessHook(_ redis.ProcessHook) redis.ProcessHook { return h.process }
func (h *authRaceCache) ProcessPipelineHook(_ redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if err := h.process(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}
}
func (h *authRaceCache) process(_ context.Context, cmd redis.Cmder) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	args := cmd.Args()
	key := fmt.Sprint(args[1])
	switch cmd.Name() {
	case "set":
		value := fmt.Sprint(args[2])
		if raw, ok := args[2].([]byte); ok {
			value = string(raw)
		}
		h.values[key] = value
		cmd.(*redis.StatusCmd).SetVal("OK")
	case "get":
		value, ok := h.values[key]
		if !ok {
			cmd.SetErr(redis.Nil)
			return redis.Nil
		}
		cmd.(*redis.StringCmd).SetVal(value)
	case "del":
		delete(h.values, key)
		cmd.(*redis.IntCmd).SetVal(1)
	default:
		return fmt.Errorf("unexpected command %s", cmd.Name())
	}
	return nil
}
func setupAuthRaceCache(t *testing.T) {
	t.Helper()
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: "127.0.0.1", Port: 1, Prefix: "auth-revocation-isolated"}); err != nil {
		t.Fatal(err)
	}
	cache.Client().AddHook(&authRaceCache{values: map[string]string{}})
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
}

type racingAdminStore struct {
	admincontract.Store
	current   *admindomain.Admin
	afterRead func()
	reads     int
}

func (r *racingAdminStore) GetByID(id uint) (*admindomain.Admin, error) {
	r.reads++
	if r.current == nil || r.current.ID != id {
		return nil, nil
	}
	snapshot := *r.current
	if r.afterRead != nil {
		callback := r.afterRead
		r.afterRead = nil
		callback()
	}
	return &snapshot, nil
}

type racingUserStore struct {
	usercontract.Store
	current   *userdomain.User
	afterRead func()
	reads     int
}

func (r *racingUserStore) GetByID(id uint) (*userdomain.User, error) {
	r.reads++
	if r.current == nil || r.current.ID != id {
		return nil, nil
	}
	snapshot := *r.current
	if r.afterRead != nil {
		callback := r.afterRead
		r.afterRead = nil
		callback()
	}
	return &snapshot, nil
}
func runAdminRaceRequest(t *testing.T, repo admincontract.Store, token string) (int, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(JWTAuthMiddleware(middlewareTestSecret, repo))
	router.GET("/admin/secret", func(c *gin.Context) {
		c.JSON(200, gin.H{"status_code": 200, "super": c.GetBool(adminIsSuperContextKey)})
	})
	request := httptest.NewRequest(http.MethodGet, "/admin/secret", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, request)
	var result struct {
		Code  int  `json:"status_code"`
		Super bool `json:"super"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Code, result.Super
}
func authRaceClaims() jwt.RegisteredClaims {
	return jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}
}
func TestAdminRevocationCannotBeUndoneByAnInFlightAuthRead(t *testing.T) {
	setupAuthRaceCache(t)
	for _, event := range []string{"password", "mfa_enable", "mfa_reset", "delete", "role_demotion"} {
		t.Run(event, func(t *testing.T) {
			_ = cache.DelAdminAuthState(context.Background(), 1)
			repo := &racingAdminStore{current: &admindomain.Admin{ID: 1, Username: "fixture", IsSuper: true}}
			token := signToken(t, adminauthapp.JWTClaims{AdminID: 1, TokenVersion: 0, Typ: "access", RegisteredClaims: authRaceClaims()})
			repo.afterRead = func() {
				if event == "delete" {
					repo.current = nil
				} else if event == "role_demotion" {
					repo.current.IsSuper = false
				} else {
					repo.current.TokenVersion++
				}
				if err := cache.DelAdminAuthState(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
			}
			// The already-authorized in-flight request may finish; later requests must
			// see the completed revocation, regardless of whether Redis was repopulated.
			runAdminRaceRequest(t, repo, token)
			code, super := runAdminRaceRequest(t, repo, token)
			if event == "role_demotion" {
				if code != 200 || super {
					t.Fatalf("demoted role restored from cache: status=%d super=%v", code, super)
				}
			} else if code != 401 {
				t.Fatalf("revoked token accepted after %s: %d", event, code)
			}
			if repo.reads != 2 {
				t.Fatalf("expected authoritative reads per request, got %d", repo.reads)
			}
		})
	}
}
func TestUserRevocationCannotBeUndoneByAnInFlightAuthRead(t *testing.T) {
	setupAuthRaceCache(t)
	for _, event := range []string{"password", "mfa_enable", "mfa_reset", "disable", "delete"} {
		t.Run(event, func(t *testing.T) {
			_ = cache.DelUserAuthState(context.Background(), 1)
			repo := &racingUserStore{current: &userdomain.User{ID: 1, Status: "active"}}
			token := signToken(t, userauthapp.UserJWTClaims{UserID: 1, TokenVersion: 0, Typ: "access", RegisteredClaims: authRaceClaims()})
			repo.afterRead = func() {
				if event == "delete" {
					repo.current = nil
				} else if event == "disable" {
					repo.current.Status = "disabled"
				} else {
					repo.current.TokenVersion++
				}
				if err := cache.DelUserAuthState(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
			}
			runUserMiddleware(t, repo, token)
			if code := runUserMiddleware(t, repo, token); code != 401 {
				t.Fatalf("revoked token accepted after %s: %d", event, code)
			}
			if repo.reads != 2 {
				t.Fatalf("expected authoritative reads per request, got %d", repo.reads)
			}
		})
	}
}
