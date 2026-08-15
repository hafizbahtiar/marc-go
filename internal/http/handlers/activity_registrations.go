package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type RegistrationHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewRegistrationHandler(pool *pgxpool.Pool) *RegistrationHandler {
	return &RegistrationHandler{pool: pool, queries: sqlc.New(pool)}
}

var (
	errActivityFull       = errors.New("aktiviti penuh")
	errRegistrationClosed = errors.New("pendaftaran ditutup")
	errAlreadyRegistered  = errors.New("sudah berdaftar")
	errActivityNotOpen    = errors.New("aktiviti tidak dibuka untuk pendaftaran")
)

// newCheckinToken jana token legap untuk QR ahli.
//
// crypto/rand, bukan math/rand: sesiapa yang dapat meneka token ini boleh
// ditandakan hadir untuk orang lain, dan kehadiran itulah yang menentukan
// siapa dapat sijil.
func newCheckinToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// registerTx mendaftarkan seorang ahli, menyerikan pemeriksaan kapasiti.
//
// `select ... for update` atas baris aktiviti ialah intinya: tanpa kunci
// itu, dua permintaan serentak boleh kedua-duanya membaca "9 daripada 10
// terisi" dan kedua-duanya memasukkan baris. Pada skala ratusan ahli, kunci
// baris ini percuma — tiada sebab untuk mereka sesuatu yang lebih pintar.
func registerTx(ctx context.Context, pool *pgxpool.Pool, activityID, userID uuid.UUID) (sqlc.ActivityRegistration, error) {
	var zero sqlc.ActivityRegistration

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	// Setiap `return` di bawah berlaku SEBELUM Commit, jadi rollback ini
	// yang membatalkan segalanya — tiada laluan yang boleh menyimpan
	// pendaftaran tanpa melepasi semakan kapasiti.
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	activity, err := q.LockActivityForRegistration(ctx, activityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, errActivityNotFound
		}
		return zero, err
	}
	if activity.Status != statusPublished {
		return zero, errActivityNotOpen
	}

	now := time.Now()
	if activity.RegistrationOpensAt.Valid && now.Before(activity.RegistrationOpensAt.Time) {
		return zero, errRegistrationClosed
	}
	if now.After(activity.RegistrationClosesAt.Time) {
		return zero, errRegistrationClosed
	}

	if _, err := q.GetRegistrationByActivityAndUser(ctx, sqlc.GetRegistrationByActivityAndUserParams{
		ActivityID: activityID, UserID: userID,
	}); err == nil {
		return zero, errAlreadyRegistered
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}

	if activity.Capacity.Valid {
		count, err := q.CountActiveRegistrations(ctx, activityID)
		if err != nil {
			return zero, err
		}
		if count >= int64(activity.Capacity.Int32) {
			return zero, errActivityFull
		}
	}

	token, err := newCheckinToken()
	if err != nil {
		return zero, err
	}

	// payment_status 'pending' untuk aktiviti berbayar (fee_cents > 0) —
	// ahli tetap dapat slot kapasiti serta-merta (design decision), tapi
	// kelayakan sijil (activity_certificates.sql) hanya benarkan lepas
	// payment_status='paid'. Aktiviti percuma kekal 'not_required'.
	status, paymentStatus := "registered", "not_required"
	if activity.FeeCents > 0 {
		paymentStatus = "pending"
	}

	reg, err := q.CreateRegistration(ctx, sqlc.CreateRegistrationParams{
		ActivityID:    activityID,
		UserID:        userID,
		Status:        status,
		PaymentStatus: paymentStatus,
		CheckinToken:  token,
	})
	if err != nil {
		// Indeks unik separa activity_registrations_active_uniq ialah
		// penjaga terakhir kalau semakan di atas dilepasi entah bagaimana;
		// ia ralat klien, bukan 500.
		if isUniqueViolation(err) {
			return zero, errAlreadyRegistered
		}
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return reg, nil
}

// requireManagement — sama seperti ActivityHandler: semakan dibuat DALAM
// handler (authz.IsManagement), bukan middleware.
func (h *RegistrationHandler) requireManagement(c *gin.Context) bool {
	ok, err := authz.IsManagement(c.Request.Context(), h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk pengurusan sahaja"})
		return false
	}
	return true
}

// Register — POST /activities/:id/registration.
//
// Tiada audit.Record di sini: volum pendaftaran tinggi, dan baris itu
// sendiri sudah membawa registered_at/cancelled_at. Keputusan sama seperti
// `create` post.
func (h *RegistrationHandler) Register(c *gin.Context) {
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	reg, err := registerTx(c.Request.Context(), h.pool, activityID, middleware.UserID(c))
	switch {
	case errors.Is(err, errActivityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	case errors.Is(err, errActivityFull):
		c.JSON(http.StatusConflict, gin.H{"error": "aktiviti sudah penuh"})
		return
	case errors.Is(err, errRegistrationClosed):
		c.JSON(http.StatusConflict, gin.H{"error": "pendaftaran telah ditutup"})
		return
	case errors.Is(err, errAlreadyRegistered):
		c.JSON(http.StatusConflict, gin.H{"error": "anda sudah berdaftar"})
		return
	case errors.Is(err, errActivityNotOpen):
		c.JSON(http.StatusConflict, gin.H{"error": "aktiviti belum dibuka"})
		return
	case err != nil:
		log.Printf("daftar aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal daftar aktiviti"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"registration": reg})
}

// Cancel — DELETE /activities/:id/registration. Baris 'cancelled' kekal
// sebagai sejarah; indeks unik separa yang membenarkan daftar semula.
func (h *RegistrationHandler) Cancel(c *gin.Context) {
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	reg, err := h.queries.CancelRegistration(c.Request.Context(), sqlc.CancelRegistrationParams{
		ActivityID: activityID,
		UserID:     middleware.UserID(c),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "anda tidak berdaftar"})
			return
		}
		log.Printf("batal pendaftaran aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batal pendaftaran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"registration": reg})
}

// ListForActivity — GET /activities/:id/registrations. Pengurusan sahaja:
// senarai ini membawa nama sebenar dan member_id ahli lain.
//
// Setiap baris turut membawa attended_session_ids (uuid[], [] bila kosong)
// — skrin kehadiran pengurusan menyemai suisnya daripada situ. Tanpa medan
// itu setiap suis bermula OFF dan laluan DELETE .../attendance/:rid tidak
// pernah boleh dicapai. Agregat itu datang dalam kueri YANG SAMA; jangan
// gantikannya dengan satu bacaan kehadiran per pendaftaran.
func (h *RegistrationHandler) ListForActivity(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	rows, err := h.queries.ListRegistrationsByActivity(c.Request.Context(), activityID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai pendaftaran"})
		return
	}
	if rows == nil {
		rows = []sqlc.ListRegistrationsByActivityRow{}
	}
	c.JSON(http.StatusOK, gin.H{"registrations": rows})
}

// ListMine — GET /me/activities. Pendaftaran aktif pemanggil sahaja;
// yang dibatalkan tidak dipulangkan.
func (h *RegistrationHandler) ListMine(c *gin.Context) {
	rows, err := h.queries.ListMyRegistrations(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat aktiviti anda"})
		return
	}
	if rows == nil {
		rows = []sqlc.ListMyRegistrationsRow{}
	}
	c.JSON(http.StatusOK, gin.H{"registrations": rows})
}
