package middleware

import (
	"fmt"
	"github.com/redis/go-redis/v9"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestMFAManagementLimitsAccountAcrossIPAndEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 10 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	for _, backend := range []struct {
		name   string
		client *redis.Client
	}{{"unconfigured", nil}, {"unavailable", client}} {
		for _, identityKey := range []string{"admin_id", "user_id"} {
			t.Run(backend.name+"/"+identityKey, func(t *testing.T) {
				r := gin.New()
				r.Use(func(c *gin.Context) {
					if c.GetHeader("Test-Account") == "other" {
						c.Set(identityKey, uint(2))
					} else if c.GetHeader("Test-Account") != "missing" {
						c.Set(identityKey, uint(1))
					}
				})
				r.Use(MFAManagementRateLimit(backend.client, identityKey))
				r.POST("/disable", func(c *gin.Context) { c.Status(http.StatusNoContent) })
				r.POST("/regenerate", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
				r.GET("/status", func(c *gin.Context) { c.Status(http.StatusNoContent) })
				request := func(method, path, account string, index int) int {
					w := httptest.NewRecorder()
					req := httptest.NewRequest(method, path, nil)
					req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", index+1)
					req.Header.Set("Test-Account", account)
					r.ServeHTTP(w, req)
					return w.Code
				}
				for i := 0; i < 6; i++ {
					if got := request(http.MethodGet, "/status", "", i); got != 204 {
						t.Fatalf("status blocked: %d", got)
					}
				}
				for i := 0; i < 5; i++ {
					path := "/disable"
					want := 204
					if i%2 == 1 {
						path = "/regenerate"
						want = 400
					}
					if got := request(http.MethodPost, path, "", i); got != want {
						t.Fatalf("attempt %d: %d", i, got)
					}
				}
				if got := request(http.MethodPost, "/regenerate", "", 9); got != 429 {
					t.Fatalf("IP/endpoint rotation bypass: %d", got)
				}
				if got := request(http.MethodGet, "/status", "", 9); got != 204 {
					t.Fatalf("status consumes mutation budget: %d", got)
				}
				if got := request(http.MethodPost, "/disable", "other", 9); got != 204 {
					t.Fatalf("other account blocked: %d", got)
				}
				if got := request(http.MethodPost, "/disable", "missing", 9); got != 401 {
					t.Fatalf("missing identity accepted: %d", got)
				}
			})
		}
	}

}
