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

// RequireApprovedStatus mesti dipasang selepas RequireAuth. Gate akses
// sehingga profiles.status = 'approved' (Stage 11) — app khusus
// ahli komuniti, pendaftaran baru kena diluluskan management dulu.
//
// GET/PATCH /me SENGAJA tak diletak di bawah middleware ni — user
// pending/rejected tetap perlu boleh papar status semasa dia sendiri
// (padanan design: "boleh login, tapi semua endpoint lain block").
func RequireApprovedStatus(q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		status, err := q.GetStatusByUserID(c.Request.Context(), UserID(c))
		if err != nil || status != "approved" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "akaun anda belum diluluskan pihak pengurusan"})
			return
		}
		c.Next()
	}
}
