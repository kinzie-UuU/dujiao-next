package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	credentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func TestResolveAllowedOrigin(t *testing.T) {
	got := resolveAllowedOrigin("https://example.com", []string{"*"}, false)
	if got != "*" {
		t.Fatalf("wildcard without credentials should return *, got %s", got)
	}

	got = resolveAllowedOrigin("https://example.com", []string{"*"}, true)
	if got != "" {
		t.Fatalf("wildcard with credentials must not echo origin, got %s", got)
	}

	got = resolveAllowedOrigin("https://a.example.com", []string{"https://a.example.com", "https://b.example.com"}, false)
	if got != "https://a.example.com" {
		t.Fatalf("allow-list should return matched origin, got %s", got)
	}

	got = resolveAllowedOrigin("https://x.example.com", []string{"https://a.example.com"}, false)
	if got != "" {
		t.Fatalf("unmatched origin should be empty, got %s", got)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"request_id": getRequestID(c)})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(requestIDHeader, "req-123")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status want 200 got %d", w.Code)
	}
	if w.Header().Get(requestIDHeader) != "req-123" {
		t.Fatalf("response request id want req-123 got %s", w.Header().Get(requestIDHeader))
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp["request_id"] != "req-123" {
		t.Fatalf("context request id want req-123 got %s", resp["request_id"])
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w2, req2)
	generated := w2.Header().Get(requestIDHeader)
	if generated == "" {
		t.Fatalf("generated request id should not be empty")
	}
	if resp := strings.TrimSpace(generated); resp == "" {
		t.Fatalf("generated request id should not be blank")
	}
}

func TestJWTAuthMiddlewareMissingSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(JWTAuthMiddleware("", nil))
	r.GET("/admin/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status want 200 got %d", w.Code)
	}
	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status_code want 401 got %d", resp.StatusCode)
	}
}

func TestPaymentTOTPHeaderAllowedByDefaultCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORSMiddleware(config.CORSConfig{}))
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/payment-channels", nil)
	req.Header.Set("Origin", "https://admin.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-admin-totp")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Admin-TOTP") {
		t.Fatalf("TOTP preflight blocked: %d %v", w.Code, w.Header())
	}
}

func TestCredentialedCORSRequiresExplicitOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, origin string
		allowed      []string
		credentials  bool
		want         string
	}{
		{"wildcard attacker", "https://attacker.example", []string{"*"}, true, ""},
		{"wildcard opaque", "null", []string{"*"}, true, ""},
		{"wildcard absent", "", []string{"*"}, true, ""},
		{"mixed attacker", "https://attacker.example", []string{"*", "https://shop.example"}, true, ""},
		{"mixed opaque", "null", []string{"*", "https://shop.example"}, true, ""},
		{"mixed allowed", "https://shop.example", []string{"*", "https://shop.example"}, true, "https://shop.example"},
		{"exact allowed", "https://shop.example", []string{"https://shop.example"}, true, "https://shop.example"},
		{"public wildcard", "https://attacker.example", []string{"*"}, false, "*"},
		{"public opaque", "null", []string{"*"}, false, "*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodOptions, http.MethodGet} {
				r := gin.New()
				r.Use(CORSMiddleware(config.CORSConfig{AllowedOrigins: tc.allowed, AllowCredentials: tc.credentials}))
				r.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusOK) })
				req := httptest.NewRequest(method, "/api/test", nil)
				req.Header.Set("Origin", tc.origin)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if got := w.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
					t.Fatalf("%s origin header = %q, want %q", method, got, tc.want)
				}
				if tc.want != "" && tc.want != "*" && w.Header().Get("Vary") != "Origin" {
					t.Fatal("explicit origin must vary by Origin")
				}
				if tc.credentials && w.Header().Get("Access-Control-Allow-Credentials") != "true" {
					t.Fatal("credentials behavior changed")
				}
				if !tc.credentials && w.Header().Get("Access-Control-Allow-Credentials") != "" {
					t.Fatal("public request permits credentials")
				}
			}
		})
	}
}

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
