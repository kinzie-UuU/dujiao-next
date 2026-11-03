package middleware

import (
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestAuthenticatedRateKeysUseIdentityAndSourceIP(t *testing.T) {
	for _, tc := range []struct {
		contextKey string
		key        RateLimitKeyFunc
	}{
		{upstreamCredentialIDKey, KeyByUpstreamCredentialAndIP}, {channelClientIDKey, KeyByChannelClientAndIP},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.RemoteAddr = "192.0.2.1:1234"
		c.Set(tc.contextKey, uint(1))
		first := tc.key(c)
		c.Set(tc.contextKey, uint(2))
		if first == tc.key(c) {
			t.Fatal("different authenticated clients share a credential quota")
		}
		c.Set(tc.contextKey, uint(1))
		c.Request.RemoteAddr = "192.0.2.2:1234"
		if first == tc.key(c) {
			t.Fatal("different source IPs share a credential quota")
		}
	}
}

type fixedCallbackRoutes struct{}

func (fixedCallbackRoutes) GetCallbackRoutesCached() *settingsintegration.CallbackRoutesSetting {
	return &settingsintegration.CallbackRoutesSetting{PaymentCallback: "/api/custom-pay", StripeWebhook: "/api/custom-stripe"}
}
func TestCustomCallbacksCannotBypassRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CustomCallbackRateLimitMiddleware(fixedCallbackRoutes{}, func(c *gin.Context) { c.AbortWithStatus(429) }))
	r.NoRoute(func(c *gin.Context) { c.Status(204) })
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/custom-pay", 429}, {"GET", "/api/custom-pay/", 429},
		{"POST", "/api/custom-stripe", 429}, {"GET", "/api/custom-stripe", 204},
		{"POST", "/api/unrelated", 204},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}
