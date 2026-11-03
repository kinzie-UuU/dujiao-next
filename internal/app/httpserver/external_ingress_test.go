package httpserver

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/app/container"
	"github.com/dujiao-next/internal/app/httpserver/middleware"
	channelhttp "github.com/dujiao-next/internal/modules/channelapi/transport/http"
	upstreamhttp "github.com/dujiao-next/internal/modules/upstreamapi/transport/http"
	wallethttp "github.com/dujiao-next/internal/modules/wallet/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func TestExternalRoutesBoundRotatedUnauthenticatedKeys(t *testing.T) {
	for _, backend := range []string{"redis", "local"} {
		t.Run(backend, func(t *testing.T) {
			var client *redis.Client
			if backend == "redis" {
				server := miniredis.RunT(t)
				client = redis.NewClient(&redis.Options{Addr: server.Addr()})
				t.Cleanup(func() { client.Close() })
			}
			r := gin.New()
			api := r.Group("/api/v1")
			c := &container.Container{}
			rule := middleware.RateLimitRule{Prefix: "upstream", MaxRequests: 1, WindowSeconds: 60, BlockSeconds: 30}
			registerUpstreamRoutes(api, c, &upstreamhttp.Handler{}, client, rule, middleware.RateLimitRule{})
			rule.Prefix = "channel"
			registerChannelRoutes(api, c, &channelhttp.Handler{}, nil, nil, nil, nil, &wallethttp.ChannelHandler{}, client, rule)
			for _, tc := range []struct{ path, header string }{
				{"/api/v1/upstream/products", "Dujiao-Next-Api-Key"}, {"/api/v1/channel/me", "Dujiao-Next-Channel-Key"},
			} {
				for i, want := range []int{401, 401, 429, 401} {
					req := httptest.NewRequest("GET", tc.path, nil)
					req.Header.Set(tc.header, fmt.Sprintf("rotating-%d", i))
					if i == 3 {
						req.RemoteAddr = "203.0.113.8:1234"
					}
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, req)
					if rec.Code != want {
						t.Fatalf("%s attempt %d: got %d, want %d", tc.path, i, rec.Code, want)
					}
				}
			}
		})
	}
}
