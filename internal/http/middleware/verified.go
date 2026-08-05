package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"marc/internal/db/sqlc"
)

// RequireVerifiedEmail mesti dipasang selepas RequireAuth. Digunakan untuk
// route Posts (Stage 10) — gate `profiles.email_verified = true`.
//
// Extension point: bila payment/membership dues system siap, tambah check
// kedua ("dah verified DAN dah bayar") terus dalam middleware ni — tak
// perlu ubah struktur route/handler yang dah pakai middleware ni.
func RequireVerifiedEmail(q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		verified, err := q.GetEmailVerifiedByUserID(c.Request.Context(), UserID(c))
		if err != nil || !verified {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "sila sahkan email anda dahulu"})
			return
		}
		c.Next()
	}
}
