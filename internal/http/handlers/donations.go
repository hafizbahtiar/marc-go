package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
	"marc/internal/payment"
)

// DonationHandler bergantung interface `payment.Gateway` sahaja, BUKAN
// struct konkrit macam Stripe — tambah ToyyibPay/SociaBuzz (Stage 12)
// tak perlu ubah handler ni, cuma daftar entry baru dalam `gateways`
// (lihat `cmd/api/main.go`).
type DonationHandler struct {
	gateways    map[string]payment.Gateway
	queries     *sqlc.Queries
	emailClient *email.Client
}

func NewDonationHandler(pool *pgxpool.Pool, gateways map[string]payment.Gateway, emailClient *email.Client) *DonationHandler {
	return &DonationHandler{gateways: gateways, queries: sqlc.New(pool), emailClient: emailClient}
}

// minDonationCents/maxDonationCents — pagar munasabah (elak fat-finger
// RM0.01 atau RM999999) sementara threshold RM500 Stripe-vs-SociaBuzz
// (Stage 12, belum implement — SociaBuzz belum wired) tak dikuatkuasakan
// di sini lagi.
const (
	minDonationCents = 100     // RM1
	maxDonationCents = 5000000 // RM50,000
)

// max=200/max=254 — had panjang munasabah. Bukan sekadar kebersihan:
// nilai metadata Stripe dihadkan 500 aksara, jadi nama 100KB (body limit
// global 1MB) akan buat `CreatePayment` gagal dengan 500 daripada API
// Stripe, bukan 400 yang betul, dan sampah tu turut masuk column `text`
// tanpa had di DB.
type donationCheckoutRequest struct {
	AmountCents int64  `json:"amount_cents" binding:"required"`
	DonorName   string `json:"donor_name" binding:"omitempty,max=200"`
	DonorEmail  string `json:"donor_email" binding:"omitempty,email,max=254"`
}

// selectGateway pilih gateway ikut amount. Buat masa ni Stripe SAHAJA
// (SociaBuzz belum wired) — bila siap, threshold RM500 masuk sini SATU
// tempat, tak sentuh Checkout() langsung.
func (h *DonationHandler) selectGateway(amountCents int64) payment.Gateway {
	return h.gateways["stripe"]
}

// Checkout mulakan donation (gateway ditentukan `selectGateway`) +
// rekod `donations` status `pending`. Route AWAM (tiada RequireAuth) —
// tapi guna OptionalAuth: ahli MARC yang log masuk dikaitkan `user_id`
// automatik (jejak dalaman lengkap, boleh terus rujuk emel akaun);
// anonymous WAJIB isi `donor_email` (keputusan produk 2026-08-09 —
// semua donation kena ada jejak, walau bukan ahli app).
func (h *DonationHandler) Checkout(c *gin.Context) {
	var req donationCheckoutRequest
	if !bindJSON(c, &req) {
		return
	}

	if req.AmountCents < minDonationCents || req.AmountCents > maxDonationCents {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount tidak sah"})
		return
	}

	gw := h.selectGateway(req.AmountCents)
	if gw == nil || !gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "donation belum tersedia"})
		return
	}

	userID, loggedIn := middleware.UserIDOptional(c)
	req.DonorEmail = strings.ToLower(strings.TrimSpace(req.DonorEmail))
	req.DonorName = strings.TrimSpace(req.DonorName)

	if !loggedIn && req.DonorEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email diperlukan untuk donation tanpa log masuk"})
		return
	}

	result, err := gw.CreatePayment(c.Request.Context(), payment.CreateParams{
		AmountCents: req.AmountCents,
		Currency:    "myr",
		Metadata: map[string]string{
			"donor_name":  req.DonorName,
			"donor_email": req.DonorEmail,
		},
	})
	if err != nil {
		log.Printf("%s create payment: %v", gw.Name(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	params := sqlc.CreateDonationParams{
		DonorName:   ptrToText(req.DonorName),
		DonorEmail:  ptrToText(req.DonorEmail),
		AmountCents: int32(req.AmountCents),
		Currency:    "myr",
		Gateway:     gw.Name(),
		GatewayRef:  result.GatewayRef,
	}
	if loggedIn {
		params.UserID = pgtype.UUID{Bytes: userID, Valid: true}
	}

	if _, err := h.queries.CreateDonation(c.Request.Context(), params); err != nil {
		log.Printf("create donation row: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	// Client tengok field mana terisi (client_secret vs redirect_url)
	// untuk tentukan flow — tak perlu tahu gateway spesifik apa (lihat
	// payment.CreateResult).
	c.JSON(http.StatusOK, gin.H{
		"gateway":       gw.Name(),
		"client_secret": result.ClientSecret,
		"redirect_url":  result.RedirectURL,
	})
}

// Webhook terima callback mana-mana gateway berdaftar (`:gateway` path
// param, cth "stripe") dan update status row `donations` berpadanan.
// Route AWAM, tiada auth — keselamatan bergantung SEPENUHNYA pada
// `gw.VerifyWebhook` (signature/format spesifik setiap gateway), BUKAN
// body request yang boleh dipalsukan sesiapa.
func (h *DonationHandler) Webhook(c *gin.Context) {
	gw, ok := h.gateways[c.Param("gateway")]
	if !ok || !gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "donation belum tersedia"})
		return
	}

	payload, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload tidak sah"})
		return
	}

	event, err := gw.VerifyWebhook(payload, c.Request.Header)
	if err != nil {
		if errors.Is(err, payment.ErrIgnoredEvent) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		// Signing secret belum diisi — endpoint TAK boleh sahkan apa-apa,
		// jadi ia fail-closed (503), bukan terima event tak disahkan.
		if errors.Is(err, payment.ErrNotConfigured) {
			log.Printf("webhook %s: signing secret belum dikonfigurasi", gw.Name())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webhook belum dikonfigurasi"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "signature tidak sah"})
		return
	}

	updated, err := h.queries.UpdateDonationStatusByGatewayRef(c.Request.Context(), sqlc.UpdateDonationStatusByGatewayRefParams{
		Gateway:    gw.Name(),
		GatewayRef: event.GatewayRef,
		Status:     event.Status,
	})
	if err != nil {
		// pgx.ErrNoRows = tiada row yang layak dikemas kini: replay
		// webhook atas donation yang dah 'succeeded' (terminal), atau ref
		// yang bukan milik kita. Kedua-duanya normal, bukan kegagalan.
		// Ralat lain pun sengaja tak digagalkan — gateway retry
		// berterusan kalau bukan 200, jadi log je.
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("update donation status (gateway=%s, ref=%s): %v", gw.Name(), event.GatewayRef, err)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	// `err == nil` di sini bermakna row BENAR-BENAR beralih (WHERE
	// `status <> 'succeeded'` di query terkena) — bukan replay. Jadi
	// resit hantar TEPAT SEKALI setiap donation berjaya, retry Stripe
	// selepas ni akan kena `pgx.ErrNoRows` (row dah 'succeeded', tak
	// match WHERE lagi) dan skip terus cabang ni.
	if event.Status == "succeeded" {
		h.sendReceiptEmail(c.Request.Context(), updated)
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// sendReceiptEmail best-effort — kegagalan hantar emel TAK gagalkan
// webhook (Stripe retry kalau bukan 200, dan donation dah pun berjaya
// direkod, resit hilang bukan sebab kritikal untuk retry).
func (h *DonationHandler) sendReceiptEmail(ctx context.Context, d sqlc.Donation) {
	to := textToPtr(d.DonorEmail)
	if to == nil && d.UserID.Valid {
		user, err := h.queries.GetUserByID(ctx, d.UserID.Bytes)
		if err != nil {
			log.Printf("resit donation: gagal cari emel user %s: %v", d.UserID.Bytes, err)
			return
		}
		to = &user.Email
	}
	if to == nil {
		// Sepatutnya tak berlaku — constraint DB `donations_traceable`
		// jamin user_id ATAU donor_email wujud — tapi jangan panic kalau
		// data lama/tak dijangka, log je.
		log.Printf("resit donation: tiada emel untuk donation gateway_ref=%s", d.GatewayRef)
		return
	}

	name := "Penderma"
	if n := textToPtr(d.DonorName); n != nil && *n != "" {
		name = *n
	}
	amount := fmt.Sprintf("RM%.2f", float64(d.AmountCents)/100)

	subject := "Resit Sumbangan MARC"
	html := fmt.Sprintf(
		"<p>Terima kasih %s atas sumbangan anda sebanyak <strong>%s</strong> kepada MARC.</p>"+
			"<p>Emel ini adalah resit rasmi bagi sumbangan anda. Sila simpan untuk rekod anda.</p>",
		name, amount,
	)

	if err := h.emailClient.Send(ctx, *to, subject, html); err != nil {
		log.Printf("gagal hantar resit donation (gateway_ref=%s): %v", d.GatewayRef, err)
	}
}
