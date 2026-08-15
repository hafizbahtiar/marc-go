// Package handlers — PaymentsHandler menyediakan bacaan sahaja bagi bayaran
// (bukan checkout/webhook, yang kekal dalam registration_payment.go/
// activity_registration_payment.go/donation.go): sejarah bayaran SEORANG
// ahli (yuran pendaftaran + yuran aktiviti, GET /me/payments) dan tinjauan
// merentas modul untuk pengurusan (GET /admin/payments, guna payment_logs
// yang sedia ada — lihat internal/paymentlog).
package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type PaymentsHandler struct {
	queries *sqlc.Queries
}

func NewPaymentsHandler(pool *pgxpool.Pool) *PaymentsHandler {
	return &PaymentsHandler{queries: sqlc.New(pool)}
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

// Mine — GET /me/payments. Dua senarai berasingan (bukan satu list
// digabung): dua jenis bayaran ni struktur berbeza (yuran pendaftaran
// boleh ada >1 percubaan per ahli; yuran aktiviti satu baris setiap
// pendaftaran) dan Flutter memaparkannya sebagai dua seksyen, bukan satu
// jadual rata.
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

	c.JSON(http.StatusOK, gin.H{
		"registration_fee": registrationFee,
		"activity_fees":    activityFees,
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

// ListAll — GET /admin/payments. Tinjauan merentas modul guna payment_logs
// sedia ada — pengurusan sahaja (disemak DALAM handler, padanan
// PaymentReconcileHandler.Run). Cursor keyset `before_id` (padanan
// ListPosts), bukan offset — offset jadi tak stabil bila baris baharu
// terus masuk semasa pengurus menatal.
func (h *PaymentsHandler) ListAll(c *gin.Context) {
	ctx := c.Request.Context()

	isManagement, err := authz.IsManagement(ctx, h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai bayaran"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "akses ditolak"})
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

	var moduleFilter pgtype.Text
	if raw := c.Query("module"); raw != "" {
		if !validPaymentLogModules[raw] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "modul tidak sah"})
			return
		}
		moduleFilter = pgtype.Text{String: raw, Valid: true}
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
		Module:   moduleFilter,
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
