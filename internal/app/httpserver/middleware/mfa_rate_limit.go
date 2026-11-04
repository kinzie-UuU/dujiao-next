package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// MFAManagementRateLimit bounds all code-verifying mutations by authenticated
// account, so switching IPs or management endpoints cannot reset the budget.
// Authentication must run before this middleware. Status reads consume no budget.
func MFAManagementRateLimit(client *redis.Client, identityKey string) gin.HandlerFunc {
	limit := RateLimitMiddleware(client, RateLimitRule{
		Prefix:        "mfa_management:" + identityKey,
		WindowSeconds: 300, MaxRequests: 5, BlockSeconds: 300,
		MessageKey: "error.rate_limited",
	}, func(c *gin.Context) string { return fmt.Sprint(c.GetUint(identityKey)) })
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet {
			c.Next()
			return
		}
		if c.GetUint(identityKey) == 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		limit(c)
	}
}
