package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBodySize hadkan saiz request body (bytes) — elak memory-exhaustion
// DoS daripada body besar dihantar ke endpoint yang unauthenticated/tak
// throttled (refresh, logout, verify-email confirm) atau body JSON besar
// pada endpoint biasa.
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
