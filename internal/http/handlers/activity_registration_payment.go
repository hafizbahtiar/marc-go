package handlers

import (
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
	"marc/internal/http/middleware"
	"marc/internal/payment"
	"marc/internal/phone"
)

// ActivityRegistrationPaymentHandler — yuran AKTIVITI (activities.fee_cents),
// BUKAN yuran pendaftaran ahli sekali bayar (RegistrationPaymentHandler,
// registration_payment.go — jangan keliru dua-dua). Padanan pola handler
// itu rapat (billTo/billPhone/phone_required, VerifyWebhook, ReturnPage),
// tapi baris yang dikemas kini ialah `activity_registrations` (payment_ref/
// payment_status), bukan jadual `registration_payments` berasingan — sebab
// kelayakan sijil (queries/activity_certificates.sql) baca terus lajur ni.
//
// Checkout BUKAN gabungan daftar+bayar: ahli MESTI dah panggil
// POST /activities/:id/registration dahulu (yang kini tulis
// payment_status='pending' untuk aktiviti berbayar — lihat
// activity_registrations.go registerTx), checkout ni sekadar mulakan
// bayaran untuk pendaftaran yang SUDAH wujud.
type ActivityRegistrationPaymentHandler struct {
	gw      payment.Gateway
	queries *sqlc.Queries
	pool    *pgxpool.Pool
}

func NewActivityRegistrationPaymentHandler(pool *pgxpool.Pool, gw payment.Gateway) *ActivityRegistrationPaymentHandler {
	return &ActivityRegistrationPaymentHandler{gw: gw, queries: sqlc.New(pool), pool: pool}
}

// activityCheckoutRequest — Phone PILIHAN, sama padanan checkoutRequest
// (registration_payment.go): cuma perlu bila profiles.phone masih kosong
// atau cacat.
type activityCheckoutRequest struct {
	Phone string `json:"phone"`
}

// Checkout — POST /activities/:id/registration/checkout.
func (h *ActivityRegistrationPaymentHandler) Checkout(c *gin.Context) {
	if h.gw == nil || !h.gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pembayaran yuran aktiviti belum tersedia"})
		return
	}

	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req activityCheckoutRequest
	// Body kosong sah (ahli yang dah ada phone tak perlu hantar apa-apa) —
	// cuma tolak kalau JSON yang DIHANTAR cacat.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	reg, err := h.queries.GetRegistrationByActivityAndUser(ctx, sqlc.GetRegistrationByActivityAndUserParams{
		ActivityID: activityID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "anda belum berdaftar untuk aktiviti ini, daftar dahulu"})
			return
		}
		log.Printf("cari pendaftaran aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	switch reg.PaymentStatus {
	case "paid":
		c.JSON(http.StatusBadRequest, gin.H{"error": "yuran aktiviti ini sudah dibayar"})
		return
	case "not_required":
		c.JSON(http.StatusBadRequest, gin.H{"error": "aktiviti ini percuma, tiada yuran perlu dibayar"})
		return
	}

	// Nota (Opus verify 2026-08-15, LOW): kalau `reg.PaymentRef` dah
	// diisi (checkout sebelum ni), `SetRegistrationPaymentRef` di bawah
	// akan TULIS GANTI dengan ref bil baharu — bil LAMA yatim (jika ahli
	// entah bagaimana masih bayar ke situ, webhook takkan jumpa baris
	// sepadan). Sengaja TAK disekat di sini: menyekat checkout berulang
	// akan kunci ahli yang bil pertamanya tamat tempoh/gagal daripada
	// cuba lagi, sehingga sapuan 24 jam (unpaidBillAfter) bebaskan
	// semula — regresi UX lebih teruk drpd risiko bil yatim yang jarang
	// berlaku. Log sahaja untuk kelihatan dalam pemantauan.
	if reg.PaymentRef.Valid && reg.PaymentRef.String != "" {
		log.Printf("checkout yuran aktiviti: ganti payment_ref sedia ada (registration=%s, ref lama=%s) — bil lama jadi yatim kalau masih boleh dibayar", reg.ID, reg.PaymentRef.String)
	}

	activity, err := h.queries.GetActivityByID(ctx, activityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": errActivityNotFound.Error()})
			return
		}
		log.Printf("cari aktiviti %s (checkout yuran): %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	// billTo WAJIB oleh ToyyibPay (lihat toyyibpay.go) — fallback ke
	// member_id kalau display_name kosong.
	billTo := ""
	if profile.DisplayName.Valid {
		billTo = profile.DisplayName.String
	}
	if billTo == "" {
		billTo = profile.MemberID
	}

	// billPhone JUGA WAJIB — padanan pola RegistrationPaymentHandler.Checkout
	// (lihat komen penuh di sana): phone TERSIMPAN turut disahkan semula
	// (bukan sekadar disemak kosong/tidak), phone cacat dilayan sama macam
	// kosong dan jatuh ke laluan minta-semula.
	billPhone := ""
	if profile.Phone.Valid && profile.Phone.String != "" {
		if normalized, ok := phone.NormalizeMY(profile.Phone.String); ok {
			billPhone = normalized
		}
	}
	if billPhone == "" {
		trimmed := strings.TrimSpace(req.Phone)
		if trimmed == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "sila isi nombor telefon dahulu",
				"code":  "phone_required",
			})
			return
		}
		normalized, ok := phone.NormalizeMY(trimmed)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "format nombor telefon tidak sah"})
			return
		}
		if _, err := h.queries.UpdateProfile(ctx, sqlc.UpdateProfileParams{
			UserID: userID,
			Phone:  pgtype.Text{String: normalized, Valid: true},
		}); err != nil {
			log.Printf("simpan phone semasa checkout yuran aktiviti: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
			return
		}
		billPhone = normalized
	}

	metadata := map[string]string{
		"description": fmt.Sprintf("Yuran aktiviti: %s", activity.Title),
		"reference":   reg.ID.String(),
		"billTo":      billTo,
		"billEmail":   profile.Email,
		"billPhone":   billPhone,
	}

	result, err := h.gw.CreatePayment(ctx, payment.CreateParams{
		AmountCents: int64(activity.FeeCents),
		Currency:    "myr",
		Metadata:    metadata,
	})
	if err != nil {
		log.Printf("%s create payment (activity fee): %v", h.gw.Name(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	if _, err := h.queries.SetRegistrationPaymentRef(ctx, sqlc.SetRegistrationPaymentRefParams{
		ID:         reg.ID,
		PaymentRef: pgtype.Text{String: result.GatewayRef, Valid: true},
	}); err != nil {
		log.Printf("simpan payment_ref pendaftaran aktiviti %s: %v", reg.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"redirect_url": result.RedirectURL})
}

// Webhook — POST /activity-registrations/webhook/toyyibpay. Route AWAM,
// tiada auth — padanan pola RegistrationPaymentHandler.Webhook,
// keselamatan bergantung SEPENUHNYA pada gw.VerifyWebhook.
func (h *ActivityRegistrationPaymentHandler) Webhook(c *gin.Context) {
	if h.gw == nil || !h.gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pembayaran yuran aktiviti belum tersedia"})
		return
	}

	payload, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload tidak sah"})
		return
	}

	event, err := h.gw.VerifyWebhook(payload, c.Request.Header)
	if err != nil {
		if errors.Is(err, payment.ErrIgnoredEvent) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		// Kredential belum diisi — endpoint TAK boleh sahkan apa-apa, jadi
		// fail-closed (503), bukan terima event tak disahkan.
		if errors.Is(err, payment.ErrNotConfigured) {
			log.Printf("webhook %s (activity fee): kredential belum dikonfigurasi", h.gw.Name())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webhook belum dikonfigurasi"})
			return
		}
		log.Printf("webhook %s (activity fee): verify gagal: %v", h.gw.Name(), err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "signature tidak sah"})
		return
	}

	// Skema activity_registrations CHECK payment_status IN ('not_required',
	// 'pending', 'paid', 'refunded') — TIADA 'failed' (beza drpd
	// registration_payments yang ada 'failed'). Tulis "failed" terus ke
	// lajur ni akan langgar CHECK constraint. `event.Status == "failed"`
	// (ToyyibPay pulang gagal eksplisit) sengaja TAK ditulis — baris
	// kekal 'pending' dan akan dibersihkan oleh sapuan latar
	// (internal/activitysweep, 45 minit) sama macam pembayaran yang
	// ditinggalkan tanpa respons langsung. Cuma "succeeded" -> 'paid'
	// yang ditulis di sini.
	if event.Status != "succeeded" {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	updated, err := h.queries.UpdateRegistrationPaymentStatusByPaymentRef(c.Request.Context(), sqlc.UpdateRegistrationPaymentStatusByPaymentRefParams{
		PaymentRef:    pgtype.Text{String: event.GatewayRef, Valid: true},
		PaymentStatus: "paid",
	})
	if err != nil {
		// pgx.ErrNoRows = tiada row layak dikemas kini: replay webhook atas
		// bayaran yang dah 'paid' (terminal), atau ref yang bukan milik
		// kita. Normal, bukan kegagalan.
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("update payment_status pendaftaran aktiviti (ref=%s): %v", event.GatewayRef, err)
		}
	} else if updated.Status == "cancelled" {
		// Race sweep-vs-webhook (Opus verify 2026-08-15): baris ni dah
		// dibatal (CancelStaleUnpaidBills) SEBELUM webhook confirm tiba.
		// Query sengaja TIADA guard `status<>'cancelled'` supaya kes ni
		// tetap tertulis (payment_status='paid' atas status='cancelled')
		// dan boleh dikesan — bukan senyap hilang. Ini keadaan yang
		// PERLUKAN campur tangan manual (padanan proses refund manual
		// sedia ada, bukan automasi baharu): ahli dah bayar tapi slot
		// mungkin dah diambil orang lain. ERROR (bukan sekadar log)
		// supaya nampak dalam pemantauan produksi, bukan tenggelam
		// dalam log biasa.
		log.Printf("ERROR activity_registration_payment: ahli BAYAR (ref=%s, registration=%s) tapi pendaftaran SUDAH DIBATAL oleh sapuan — perlukan semakan manual (slot mungkin dah diambil orang lain)", event.GatewayRef, updated.ID)
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ReturnPage — halaman landing selepas pembayar selesai di ToyyibPay.
// Sekadar makluman; pengesahan SEBENAR jalan async via Webhook.
func (h *ActivityRegistrationPaymentHandler) ReturnPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html>
<html lang="ms"><head><meta charset="utf-8"><title>Yuran Aktiviti MARC</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h2>MARC</h2>
<p>Terima kasih. Pembayaran yuran aktiviti anda sedang diproses. Boleh kembali ke app MARC — status akan dikemas kini automatik sebaik pembayaran disahkan.</p>
</body></html>`))
}
