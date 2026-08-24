package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestPaymentConfigPulangNilaiConfig - tiada DB diperlukan, handler ni
// bacaan config statik sahaja.
func TestPaymentConfigPulangNilaiConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewPaymentConfigHandler(100)
	r.GET("/payment-config", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/payment-config", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200", w.Code)
	}

	var body paymentConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.GatewayChargeCents != 100 {
		t.Fatalf("gateway_charge_cents = %d, mahu 100", body.GatewayChargeCents)
	}
}
