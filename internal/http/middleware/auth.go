package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/auth"
)

const userIDKey = "userID"
const sessionIDKey = "sessionID"

func RequireAuth(j *auth.JWT) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak dijumpai"})
			return
		}

		userID, sessionID, err := j.ParseAccessToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak sah"})
			return
		}

		c.Set(userIDKey, userID)
		c.Set(sessionIDKey, sessionID)
		c.Next()
	}
}

// UserID pulangkan user id dari context. Hanya selamat dipanggil dalam
// route yang dilindungi RequireAuth.
func UserID(c *gin.Context) uuid.UUID {
	return c.MustGet(userIDKey).(uuid.UUID)
}

// SessionID - family_id sesi dari claim `sid`. uuid.Nil untuk token lama
// (sebelum sid wujud) - GET /me/sessions tak akan tandakan "peranti ini".
func SessionID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(sessionIDKey)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}

// OptionalAuth cuba parse Bearer token kalau ada, tapi TAK abort request
// kalau tiada/tak sah - untuk route awam yang nak "tahu siapa kalau log
// masuk" tanpa wajibkan auth (cth: donation checkout, Stage 12 - ahli
// MARC yang log masuk dikaitkan user_id, orang luar tetap boleh donate).
func OptionalAuth(j *auth.JWT) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if ok && token != "" {
			if userID, sessionID, err := j.ParseAccessToken(token); err == nil {
				c.Set(userIDKey, userID)
				c.Set(sessionIDKey, sessionID)
			}
		}
		c.Next()
	}
}

// UserIDOptional pulangkan user id + true kalau OptionalAuth (atau
// RequireAuth) berjaya set context; false kalau request tak
// authenticated (bukan ralat - route awam yang benarkan anonymous).
func UserIDOptional(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(userIDKey)
	if !ok {
		return uuid.UUID{}, false
	}
	return v.(uuid.UUID), true
}
