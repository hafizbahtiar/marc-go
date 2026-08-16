package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/auth"
)

const userIDKey = "userID"

func RequireAuth(j *auth.JWT) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak dijumpai"})
			return
		}

		userID, err := j.ParseAccessToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak sah"})
			return
		}

		c.Set(userIDKey, userID)
		c.Next()
	}
}

// UserID pulangkan user id dari context. Hanya selamat dipanggil dalam
// route yang dilindungi RequireAuth.
func UserID(c *gin.Context) uuid.UUID {
	return c.MustGet(userIDKey).(uuid.UUID)
}

// OptionalAuth cuba parse Bearer token kalau ada, tapi TAK abort request
// kalau tiada/tak sah — untuk route awam yang nak "tahu siapa kalau log
// masuk" tanpa wajibkan auth (cth: donation checkout, Stage 12 — ahli
// MARC yang log masuk dikaitkan user_id, orang luar tetap boleh donate).
func OptionalAuth(j *auth.JWT) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if ok && token != "" {
			if userID, err := j.ParseAccessToken(token); err == nil {
				c.Set(userIDKey, userID)
			}
		}
		c.Next()
	}
}

// UserIDOptional pulangkan user id + true kalau OptionalAuth (atau
// RequireAuth) berjaya set context; false kalau request tak
// authenticated (bukan ralat — route awam yang benarkan anonymous).
func UserIDOptional(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(userIDKey)
	if !ok {
		return uuid.UUID{}, false
	}
	return v.(uuid.UUID), true
}
