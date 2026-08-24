package handlers

import "github.com/gin-gonic/gin"

// PaymentConfigHandler dedah nilai config yang GENERIK untuk SEMUA
// checkout gateway (yuran pendaftaran, yuran aktiviti, modul depan) -
// bukan tapak checkout tertentu. Buat masa ini cuma satu nilai:
// anggaran fi transaksi gateway, untuk client papar breakdown invoice
// ("Yuran" + "Caj Pemprosesan" = "Jumlah"). TAK menyentuh jumlah
// SEBENAR yang dihantar ke gateway - paparan sahaja di sisi client.
type PaymentConfigHandler struct {
	gatewayChargeCents int64
}

func NewPaymentConfigHandler(gatewayChargeCents int) *PaymentConfigHandler {
	return &PaymentConfigHandler{gatewayChargeCents: int64(gatewayChargeCents)}
}

type paymentConfigResponse struct {
	GatewayChargeCents int64 `json:"gateway_charge_cents"`
}

// Get - RequireAuth SAHAJA (padanan `/registration-payments/checkout`):
// ahli `pending` yang checkout yuran pendaftaran pun perlu nilai ni.
func (h *PaymentConfigHandler) Get(c *gin.Context) {
	c.JSON(200, paymentConfigResponse{GatewayChargeCents: h.gatewayChargeCents})
}
