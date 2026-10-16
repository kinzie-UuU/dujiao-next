package httpserver

import (
	"github.com/gin-gonic/gin"
	"path"
	"strings"
)

// All uploads are untrusted documents, including non-image Telegram attachments.
// Keep image embedding functional while isolating documents opened directly.
func uploadSecurityHeaders(c *gin.Context) {
	c.Header("Content-Security-Policy", "sandbox; script-src 'none'")
	c.Header("X-Content-Type-Options", "nosniff")
	if strings.EqualFold(path.Ext(c.Request.URL.Path), ".svg") {
		c.Header("Content-Disposition", "attachment")
	}
}
