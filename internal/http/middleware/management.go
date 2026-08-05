package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
)

// RequireManagement mesti dipasang selepas RequireAuth — ia bergantung
// pada user id yang RequireAuth letak dalam context.
func RequireManagement(q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		isManagement, err := authz.IsManagement(c.Request.Context(), q, UserID(c))
		if err != nil || !isManagement {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "akses ditolak"})
			return
		}
		c.Next()
	}
}
