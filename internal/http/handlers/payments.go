// Package handlers — PaymentsHandler menyediakan bacaan sahaja bagi bayaran
// (bukan checkout/webhook, yang kekal dalam registration_payment.go/
// activity_registration_payment.go/donation.go): sejarah bayaran SEORANG
// ahli (yuran pendaftaran + yuran aktiviti, GET /me/payments) dan tinjauan
// merentas modul untuk pengurusan (GET /admin/payments, guna payment_logs
// yang sedia ada — lihat internal/paymentlog).
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/receipt"
	"marc/internal/storage"
)

type PaymentsHandler struct {
	queries *sqlc.Queries
	r2      *storage.R2Client
}

func NewPaymentsHandler(pool *pgxpool.Pool, r2Client *storage.R2Client) *PaymentsHandler {
	return &PaymentsHandler{queries: sqlc.New(pool), r2: r2Client}
}

// receiptUploadTimeout — had bagi SATU muat naik R2 resit (padanan
// certificateUploadTimeout, activity_certificates.go).
const receiptUploadTimeout = 30 * time.Second

// putReceiptObject bungkus PutObject dengan tempoh tamat per-panggilan.
func putReceiptObject(ctx context.Context, r2 *storage.R2Client, key string, body []byte) error {
	upCtx, cancel := context.WithTimeout(ctx, receiptUploadTimeout)
	defer cancel()
	return r2.PutObject(upCtx, key, "application/pdf", body)
}

// respondReceiptURL jana + muat naik PDF, pulangkan URL bertandatangan
// (padanan corak CertificateHandler.Download — R2 yang sampaikan fail,
// bukan backend). PDF SENGAJA dijana semula setiap panggilan (bukan
// disimpan/ditanda "sudah dijana" dalam DB) — resit deterministik drpd
// data yang dah tersimpan (jadual bayaran + payment_logs), jadi tiada
// keadaan tambahan untuk diselaraskan; PutObject menulis ganti kunci
// STABIL yang sama setiap kali, idempoten.
func (h *PaymentsHandler) respondReceiptURL(c *gin.Context, r2Key string, pdfBytes []byte) {
	ctx := c.Request.Context()
	if err := putReceiptObject(ctx, h.r2, r2Key, pdfBytes); err != nil {
		log.Printf("resit: muat naik R2 gagal (key=%s): %v", r2Key, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan resit"})
		return
	}
	url := h.r2.SignedURL(ctx, r2Key)
	if url == "" {
		log.Printf("resit: tandatangan URL gagal (key=%s)", r2Key)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan pautan resit"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// RegistrationReceipt — GET /me/payments/registration/:id/receipt. Hanya
// baris SENDIRI (query skop `user_id`) dan hanya bayaran 'succeeded' —
// tiada resit untuk bayaran pending/gagal.
func (h *PaymentsHandler) RegistrationReceipt(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	row, err := h.queries.GetMyRegistrationPaymentByID(ctx, sqlc.GetMyRegistrationPaymentByIDParams{
		ID:     id,
		UserID: middleware.UserID(c),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "resit tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan resit"})
		return
	}
	if row.Status != "succeeded" {
		c.JSON(http.StatusConflict, gin.H{"error": "bayaran belum berjaya, resit belum tersedia"})
		return
	}

	displayName := ""
	if n := textToPtr(row.DisplayName); n != nil {
		displayName = *n
	}
	pdfBytes, err := receipt.GenerateFeePDF(receipt.FeePayment{
		MemberID:    row.MemberID,
		PayerName:   displayName,
		PayerEmail:  row.Email,
		AmountCents: int64(row.AmountCents),
		Currency:    row.Currency,
		// `gateway_ref` nullable sejak L29. Laluan ni dijaga oleh
		// semakan `status != "succeeded"` di atas, dan bayaran tak boleh
		// jadi 'succeeded' tanpa webhook yang memadankan ref — jadi ia
		// sentiasa diisi di sini. `textOrEmpty` jaring keselamatan,
		// bukan kes yang dijangka.
		GatewayRef: textOrEmpty(row.GatewayRef),
		PaidAt:     row.CreatedAt.Time,
		Purpose:    "Yuran Pendaftaran Ahli",
	})
	if err != nil {
		log.Printf("resit yuran pendaftaran: gagal jana PDF (id=%s): %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana resit"})
		return
	}

	h.respondReceiptURL(c, fmt.Sprintf("receipts/registration/%s.pdf", row.ID), pdfBytes)
}

// ActivityReceipt — GET /me/payments/activity/:id/receipt. `:id` ialah
// id PENDAFTARAN (activity_registrations.id), bukan id aktiviti — hanya
// baris SENDIRI dan hanya `payment_status = 'paid'`.
func (h *PaymentsHandler) ActivityReceipt(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	row, err := h.queries.GetMyActivityFeeByID(ctx, sqlc.GetMyActivityFeeByIDParams{
		ID:     id,
		UserID: middleware.UserID(c),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "resit tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan resit"})
		return
	}
	if row.PaymentStatus == "refunded" {
		// Mesej berasingan drpd 'pending'/'not_required' (Opus verify
		// 2026-08-15) — "belum berjaya" salah bagi bayaran yang MEMANG
		// pernah berjaya lalu dikembalikan. Flutter tak pernah sampai
		// laluan ni (butang disembunyikan bila bukan 'paid'), tapi
		// caller API terus patut nampak mesej yang betul.
		c.JSON(http.StatusConflict, gin.H{"error": "bayaran telah dikembalikan, resit tidak lagi tersedia"})
		return
	}
	if row.PaymentStatus != "paid" {
		c.JSON(http.StatusConflict, gin.H{"error": "bayaran belum berjaya, resit belum tersedia"})
		return
	}

	displayName := ""
	if n := textToPtr(row.DisplayName); n != nil {
		displayName = *n
	}
	pdfBytes, err := receipt.GenerateFeePDF(receipt.FeePayment{
		MemberID:    row.MemberID,
		PayerName:   displayName,
		PayerEmail:  row.Email,
		AmountCents: int64(row.FeeCents),
		Currency:    row.Currency,
		GatewayRef:  textOrEmpty(row.PaymentRef),
		// `registered_at` — waktu PENDAFTARAN dicipta (checkout mula),
		// bukan waktu bayaran DISAHKAN (ahli boleh bayar berjam-jam
		// kemudian). Sama anggaran/keputusan yang DonationReceipt guna
		// (`created_at`, lihat komen di sana) — activity_registrations
		// pun tiada lajur "confirmed at" khusus. Timestamp SEBENAR wujud
		// dalam payment_logs (event webhook), tapi resit sengaja tak
		// query jadual log utk medan kosmetik ni (Opus verify 2026-08-15).
		PaidAt:  row.RegisteredAt.Time,
		Purpose: row.Title,
	})
	if err != nil {
		log.Printf("resit yuran aktiviti: gagal jana PDF (id=%s): %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana resit"})
		return
	}

	h.respondReceiptURL(c, fmt.Sprintf("receipts/activity/%s.pdf", row.ID), pdfBytes)
}

// DonationReceipt — GET /me/payments/donation/:id/receipt. Ahli LOG
// MASUK sahaja — donation anonymous (user_id null) tiada akaun untuk
// tuntut baris ni, jejak mereka cuma emel resit yang dihantar semasa
// webhook (donations.go sendReceiptEmail).
func (h *PaymentsHandler) DonationReceipt(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	d, err := h.queries.GetMyDonationByID(ctx, sqlc.GetMyDonationByIDParams{
		ID:     id,
		UserID: pgUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "resit tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan resit"})
		return
	}
	if d.Status != "succeeded" {
		c.JSON(http.StatusConflict, gin.H{"error": "bayaran belum berjaya, resit belum tersedia"})
		return
	}

	memberID := ""
	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err == nil {
		memberID = profile.MemberID
	}

	pdfBytes, err := receipt.GeneratePDF(receipt.Donation{
		MemberID:    memberID,
		DonorName:   textOrEmpty(d.DonorName),
		DonorEmail:  textOrEmpty(d.DonorEmail),
		AmountCents: int64(d.AmountCents),
		Currency:    d.Currency,
		GatewayRef:  d.GatewayRef,
		// `created_at` — bukan tarikh bayaran SEBENAR (donations tiada
		// lajur paid_at, TODO.md L22/L27), sama anggaran yang emel resit
		// asal guna kalau webhook lambat. Diterima — konsisten dgn
		// gelagat sedia ada, bukan regresi.
		PaidAt: d.CreatedAt.Time,
	})
	if err != nil {
		log.Printf("resit donation: gagal jana PDF (id=%s): %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana resit"})
		return
	}

	h.respondReceiptURL(c, fmt.Sprintf("receipts/donation/%s.pdf", d.ID), pdfBytes)
}

type registrationPaymentItem struct {
	ID          uuid.UUID          `json:"id"`
	AmountCents int32              `json:"amount_cents"`
	Currency    string             `json:"currency"`
	Gateway     string             `json:"gateway"`
	Status      string             `json:"status"`
	CreatedAt   pgtype.Timestamptz `json:"created_at"`
}

type activityPaymentItem struct {
	RegistrationID     uuid.UUID          `json:"registration_id"`
	ActivityID         uuid.UUID          `json:"activity_id"`
	Title              string             `json:"title"`
	FeeCents           int32              `json:"fee_cents"`
	Currency           string             `json:"currency"`
	StartsAt           pgtype.Timestamptz `json:"starts_at"`
	RegistrationStatus string             `json:"registration_status"`
	PaymentStatus      string             `json:"payment_status"`
	RegisteredAt       pgtype.Timestamptz `json:"registered_at"`
}

// donationPaymentItem — satu derma milik ahli log masuk.
//
// SENGAJA tiada `donor_name`/`donor_email`: ahli boleh menderma dengan
// nama/emel yang berbeza daripada akaunnya, dan senarai ni cuma perlu
// menjawab "apa yang saya derma, dan mana resitnya". Nilai itu tetap
// dicetak pada resit PDF, yang cuma pemiliknya boleh muat turun.
type donationPaymentItem struct {
	ID          uuid.UUID          `json:"id"`
	AmountCents int32              `json:"amount_cents"`
	Currency    string             `json:"currency"`
	Gateway     string             `json:"gateway"`
	Status      string             `json:"status"`
	CreatedAt   pgtype.Timestamptz `json:"created_at"`
}

// Mine — GET /me/payments. TIGA senarai berasingan (bukan satu list
// digabung): ketiga-tiga jenis bayaran ni struktur berbeza (yuran
// pendaftaran boleh ada >1 percubaan per ahli; yuran aktiviti satu baris
// setiap pendaftaran; derma bebas daripada kedua-duanya) dan Flutter
// memaparkannya sebagai tiga seksyen, bukan satu jadual rata.
//
// `donations` ditambah 2026-08-22 (L33). Sebelum ni endpoint resit derma
// (`GET /me/payments/donation/:id/receipt`) wujud dan berfungsi, tapi
// TIADA permukaan API yang pernah mendedahkan `donations.id` kepada
// pemiliknya — jadi ia mati secara praktikal melainkan seseorang meneka
// UUID. Kliennya pun sudah ada (`PaymentReceiptRepository.donation`);
// yang hilang cuma senarainya.
func (h *PaymentsHandler) Mine(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	regRows, err := h.queries.ListMyRegistrationPayments(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat sejarah bayaran"})
		return
	}
	actRows, err := h.queries.ListMyActivityPayments(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat sejarah bayaran"})
		return
	}
	donRows, err := h.queries.ListMyDonations(ctx, pgUUID(userID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat sejarah bayaran"})
		return
	}

	registrationFee := make([]registrationPaymentItem, 0, len(regRows))
	for _, r := range regRows {
		registrationFee = append(registrationFee, registrationPaymentItem{
			ID:          r.ID,
			AmountCents: r.AmountCents,
			Currency:    r.Currency,
			Gateway:     r.Gateway,
			Status:      r.Status,
			CreatedAt:   r.CreatedAt,
		})
	}

	activityFees := make([]activityPaymentItem, 0, len(actRows))
	for _, r := range actRows {
		activityFees = append(activityFees, activityPaymentItem{
			RegistrationID:     r.ID,
			ActivityID:         r.ActivityID,
			Title:              r.Title,
			FeeCents:           r.FeeCents,
			Currency:           r.Currency,
			StartsAt:           r.StartsAt,
			RegistrationStatus: r.Status,
			PaymentStatus:      r.PaymentStatus,
			RegisteredAt:       r.RegisteredAt,
		})
	}

	donations := make([]donationPaymentItem, 0, len(donRows))
	for _, r := range donRows {
		donations = append(donations, donationPaymentItem{
			ID:          r.ID,
			AmountCents: r.AmountCents,
			Currency:    r.Currency,
			Gateway:     r.Gateway,
			Status:      r.Status,
			CreatedAt:   r.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"registration_fee": registrationFee,
		"activity_fees":    activityFees,
		"donations":        donations,
	})
}

const (
	paymentLogsDefaultLimit = 50
	paymentLogsMaxLimit     = 200
)

var validPaymentLogModules = map[string]bool{
	"donation":         true,
	"registration_fee": true,
	"activity_fee":     true,
}

// paymentLogItem — sengaja TIADA RawPayload. Payload mentah gateway boleh
// bawa PII pembayar (billTo/billEmail/billPhone ToyyibPay) dan sengaja
// disimpan TANPA scrub di payment_logs (lihat migrasi
// 20260815*_create_payment_logs.sql) sebab diagnosis insiden perlukan
// bentuk asal — akses padanya terhad kepada pelayan (query DB terus),
// bukan didedahkan menerusi API kepada mana-mana app pengurusan.
type paymentLogItem struct {
	ID          int64              `json:"id"`
	Module      string             `json:"module"`
	Event       string             `json:"event"`
	Status      string             `json:"status"`
	Gateway     string             `json:"gateway"`
	GatewayRef  *string            `json:"gateway_ref"`
	AmountCents *int32             `json:"amount_cents"`
	UserID      *uuid.UUID         `json:"user_id"`
	RelatedID   *uuid.UUID         `json:"related_id"`
	Message     *string            `json:"message"`
	CreatedAt   pgtype.Timestamptz `json:"created_at"`
}

// superAdminRoleKey — siling "superadmin sahaja" utk data derma. Derma
// (Stripe) dianggap lebih sensitif drpd yuran (kelab) — keputusan produk
// 2026-08-15: management biasa (supervisor/manager/admin) TAK nampak
// baris donation langsung dalam /admin/payments, walau tapisan "Semua".
const superAdminRoleKey = "superadmin"

// nonDonationModules — apa yang management BIASA (bukan superadmin)
// boleh nampak bila tiada tapisan modul eksplisit diminta.
var nonDonationModules = []string{"registration_fee", "activity_fee"}

var allPaymentLogModules = []string{"donation", "registration_fee", "activity_fee"}

// ListAll — GET /admin/payments. Tinjauan merentas modul guna payment_logs
// sedia ada — pengurusan sahaja (disemak DALAM handler, padanan
// PaymentReconcileHandler.Run). Cursor keyset `before_id` (padanan
// ListPosts), bukan offset — offset jadi tak stabil bila baris baharu
// terus masuk semasa pengurus menatal.
//
// Modul `donation` disekat kepada superadmin sahaja (lihat komen
// `superAdminRoleKey`) — dikuatkuasakan di SINI (Go), bukan bergantung
// pada Flutter menyembunyikan cip penapis; caller yang minta
// `?module=donation` terus (bukan menerusi UI) tetap disekat.
func (h *PaymentsHandler) ListAll(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	isManagement, err := authz.IsManagement(ctx, h.queries, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai bayaran"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "akses ditolak"})
		return
	}

	isSuperAdmin, err := authz.IsAtLeastRole(ctx, h.queries, userID, superAdminRoleKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai bayaran"})
		return
	}

	limit := int32(paymentLogsDefaultLimit)
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit tidak sah"})
			return
		}
		if n > paymentLogsMaxLimit {
			n = paymentLogsMaxLimit
		}
		limit = int32(n)
	}

	var modules []string
	if raw := c.Query("module"); raw != "" {
		if !validPaymentLogModules[raw] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "modul tidak sah"})
			return
		}
		if raw == "donation" && !isSuperAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "modul derma untuk superadmin sahaja"})
			return
		}
		modules = []string{raw}
	} else if isSuperAdmin {
		modules = allPaymentLogModules
	} else {
		modules = nonDonationModules
	}

	var beforeID pgtype.Int8
	if raw := c.Query("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before_id tidak sah"})
			return
		}
		beforeID = pgtype.Int8{Int64: n, Valid: true}
	}

	rows, err := h.queries.ListRecentPaymentLogs(ctx, sqlc.ListRecentPaymentLogsParams{
		Limit:    limit,
		Modules:  modules,
		BeforeID: beforeID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai bayaran"})
		return
	}

	items := make([]paymentLogItem, 0, len(rows))
	for _, r := range rows {
		item := paymentLogItem{
			ID:        r.ID,
			Module:    r.Module,
			Event:     r.Event,
			Status:    r.Status,
			Gateway:   r.Gateway,
			CreatedAt: r.CreatedAt,
		}
		if r.GatewayRef.Valid {
			v := r.GatewayRef.String
			item.GatewayRef = &v
		}
		if r.AmountCents.Valid {
			v := r.AmountCents.Int32
			item.AmountCents = &v
		}
		if r.UserID.Valid {
			v := uuid.UUID(r.UserID.Bytes)
			item.UserID = &v
		}
		if r.RelatedID.Valid {
			v := uuid.UUID(r.RelatedID.Bytes)
			item.RelatedID = &v
		}
		if r.Message.Valid {
			v := r.Message.String
			item.Message = &v
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{"logs": items})
}
