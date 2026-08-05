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
