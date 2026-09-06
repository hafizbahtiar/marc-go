package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
)

func parseExpectedUpdatedAt(c *gin.Context, raw string) (pgtype.Timestamptz, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "updated_at diperlukan untuk mengelakkan perubahan lapuk"})
		return pgtype.Timestamptz{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "updated_at tidak sah"})
		return pgtype.Timestamptz{}, false
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, true
}

func staleWrite(c *gin.Context, message string) {
	c.JSON(http.StatusConflict, gin.H{
		"error": message,
		"code":  "stale_write",
	})
}
