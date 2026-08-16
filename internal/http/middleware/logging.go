package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"

// RequestLogger log setiap request sebagai satu baris JSON structured
// (method, path, status, latency, client_ip, request_id) — gantian
// gin.Default() punya plain-text logger, senang di-parse/query oleh log
// aggregator (Railway logs, dsb). request_id turut dihantar balik dalam
// response header supaya client boleh rujuk bila report isu.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := uuid.New().String()
		c.Set("request_id", requestID)
		c.Header(RequestIDHeader, requestID)

		start := time.Now()
		c.Next()
		latency := time.Since(start)

		level := slog.LevelInfo
		if c.Writer.Status() >= 500 {
			level = slog.LevelError
		} else if c.Writer.Status() >= 400 {
			level = slog.LevelWarn
		}

		logger.LogAttrs(c.Request.Context(), level, "request",
			slog.String("request_id", requestID),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("latency", latency),
			slog.String("client_ip", c.ClientIP()),
		)
	}
}
