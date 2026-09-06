package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"marc/internal/payment"
)

// PaymentStatusHandler menyediakan status minimum yang diperlukan oleh
// halaman return awam. Ia sengaja tidak memulangkan nama, jumlah, emel atau
// apa-apa data DB - hanya status yang gateway sahkan sendiri.
type PaymentStatusHandler struct {
	gateways map[string]payment.Gateway
}

func NewPaymentStatusHandler(gateways map[string]payment.Gateway) *PaymentStatusHandler {
	return &PaymentStatusHandler{gateways: gateways}
}

// Check - GET /payment-status/:gateway/:reference.
func (h *PaymentStatusHandler) Check(c *gin.Context) {
	gatewayName := c.Param("gateway")
	reference := strings.TrimSpace(c.Param("reference"))
	if len(reference) < 3 || len(reference) > 128 || !paymentReferenceIsSafe(reference) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rujukan bayaran tidak sah"})
		return
	}

	gateway, ok := h.gateways[gatewayName]
	if !ok || !gateway.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "gateway bayaran belum tersedia"})
		return
	}

	status, err := gateway.CheckStatus(c.Request.Context(), reference)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "status bayaran belum dapat disemak"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": status})
}

func paymentReferenceIsSafe(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
