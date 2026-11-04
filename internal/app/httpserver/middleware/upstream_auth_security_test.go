package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/constants"
	credentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type replayTestCredentials struct{}

func (replayTestCredentials) GetByApiKey(string) (*credentialdomain.ApiCredential, error) {
	now := time.Now()
	return &credentialdomain.ApiCredential{ID: 7, UserID: 4, ApiSecret: "test-secret", IsActive: true, Status: constants.ApiCredentialStatusApproved, LastUsedAt: &now, User: &userdomain.User{Status: constants.UserStatusActive}}, nil
}
func (replayTestCredentials) TouchLastUsedAt(uint, time.Time) error { return nil }

func signedUpstreamRequest(method, path string, stamp int64) *http.Request {
	body := `{"sku_id":1,"quantity":1,"downstream_order_no":"buyer-order"}`
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(upstream.HeaderApiKey, "test-key")
	req.Header.Set(upstream.HeaderTimestamp, fmt.Sprint(stamp))
	req.Header.Set(upstream.HeaderSignature, upstream.Sign("test-secret", method, path, stamp, []byte(body)))
	return req
}

func TestUpstreamMutationReplayConcurrentAndReadRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	var calls atomic.Int32
	router := gin.New()
	router.Use(UpstreamAPIAuthMiddleware(replayTestCredentials{}, client))
	router.Any("/api/v1/upstream/orders", func(c *gin.Context) { calls.Add(1); c.Status(http.StatusNoContent) })
	stamp := time.Now().Unix() + upstream.MaxTimestampSkew
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", stamp))
			if w.Code != 204 && w.Code != 409 {
				t.Errorf("unexpected response: %d %s", w.Code, w.Body)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate mutation reached application %d times", calls.Load())
	}
	keys := server.Keys()
	if len(keys) != 1 || server.TTL(keys[0]) < 2*time.Duration(upstream.MaxTimestampSkew)*time.Second {
		t.Fatal("replay reservation does not cover future timestamp window")
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, signedUpstreamRequest(http.MethodGet, "/api/v1/upstream/orders", stamp))
		if w.Code != 204 {
			t.Fatalf("read retry rejected: %d", w.Code)
		}
	}
	// A legitimate retry obtains a fresh signature; durable order references own
	// business idempotency, independent of this exact-HTTP-request guard.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", stamp-1))
	if w.Code != 204 {
		t.Fatalf("fresh signature rejected: %d", w.Code)
	}
}

func TestUpstreamReplayGuardFailsClosedWithoutConsumingInvalidSignatures(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	defer client.Close()
	router := gin.New()
	router.Use(UpstreamAPIAuthMiddleware(replayTestCredentials{}, client))
	router.POST("/api/v1/upstream/orders", func(c *gin.Context) { c.Status(204) })
	req := signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", time.Now().Unix())
	req.Header.Set(upstream.HeaderSignature, "invalid")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 401 || len(server.Keys()) != 0 {
		t.Fatalf("invalid signature reserved a replay key: %d", w.Code)
	}
	upper := signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", time.Now().Unix())
	upper.Header.Set(upstream.HeaderSignature, strings.ToUpper(upper.Header.Get(upstream.HeaderSignature)))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, upper)
	if w.Code != 401 || len(server.Keys()) != 0 {
		t.Fatalf("noncanonical signature accepted: %d", w.Code)
	}
	server.Close()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", time.Now().Unix()))
	if w.Code != 503 {
		t.Fatalf("Redis failure passed mutation: %d", w.Code)
	}
	router = gin.New()
	router.Use(UpstreamAPIAuthMiddleware(replayTestCredentials{}, nil))
	router.POST("/api/v1/upstream/orders", func(c *gin.Context) { c.Status(204) })
	w = httptest.NewRecorder()
	router.ServeHTTP(w, signedUpstreamRequest(http.MethodPost, "/api/v1/upstream/orders", time.Now().Unix()))
	if w.Code != 503 {
		t.Fatalf("disabled Redis passed mutation: %d", w.Code)
	}
}
