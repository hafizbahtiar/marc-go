package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
)

// L15 — pendaftaran tak boleh dibatalkan selepas aktiviti TAMAT
// (keputusan produk 2026-08-22).
//
// Kenapa ia penting: `ListEligibleForCertificate` menuntut
// `r.status = 'registered'`. Ahli yang hadir setiap sesi lalu menekan
// "Batal pendaftaran" pada aktiviti yang sudah tamat memusnahkan
// kelayakan sijilnya sendiri — senyap (pembatalan sengaja tak diaudit),
// dan tanpa laluan pulih dalam app.

// seedActivityEndingAt cipta aktiviti diterbitkan yang `ends_at`-nya
// ditetapkan relatif kepada sekarang. Negatif = sudah tamat.
func seedActivityEndingAt(t *testing.T, pool *pgxpool.Pool, endsIn time.Duration) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		insert into activities (category_id, title, location_name, starts_at, ends_at,
		  registration_closes_at, capacity, status)
		values ((select id from activity_categories limit 1), 'Ujian batal', 'Dewan',
		  now() + $1::interval - interval '2 hours', now() + $1::interval,
		  now() + interval '1 day', null, 'published')
		returning id`, endsIn.String()).Scan(&id); err != nil {
		t.Fatalf("seed activity: %v", err)
	}
	return id
}

func seedRegistrationFor(t *testing.T, pool *pgxpool.Pool, activityID, userID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		insert into activity_registrations
		  (activity_id, user_id, status, payment_status, checkin_token)
		values ($1, $2, 'registered', 'not_required', $3)
		returning id`, activityID, userID, uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	return id
}

func registrationStatusByID(t *testing.T, pool *pgxpool.Pool, regID uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(),
		`select status from activity_registrations where id = $1`, regID).Scan(&s); err != nil {
		t.Fatalf("baca status: %v", err)
	}
	return s
}

// Aktiviti masih akan datang — pembatalan MESTI berfungsi seperti biasa.
func TestCancelDibenarkanSebelumAktivitiTamat(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivityEndingAt(t, pool, 48*time.Hour)
	regID := seedRegistrationFor(t, pool, activityID, userID)

	rec := registrationCall(t, pool, userID, http.MethodDelete,
		"/activities/"+activityID.String()+"/registration",
		gin.Params{{Key: "id", Value: activityID.String()}},
		func(h *RegistrationHandler, c *gin.Context) { h.Cancel(c) })

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}
	if got := registrationStatusByID(t, pool, regID); got != "cancelled" {
		t.Errorf("status = %q, mahu \"cancelled\"", got)
	}
}

// INTI L15 — aktiviti sudah tamat, pembatalan mesti ditolak DAN baris
// mesti kekal 'registered'.
func TestCancelDitolakSelepasAktivitiTamat(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivityEndingAt(t, pool, -24*time.Hour)
	regID := seedRegistrationFor(t, pool, activityID, userID)

	rec := registrationCall(t, pool, userID, http.MethodDelete,
		"/activities/"+activityID.String()+"/registration",
		gin.Params{{Key: "id", Value: activityID.String()}},
		func(h *RegistrationHandler, c *gin.Context) { h.Cancel(c) })

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("kod = %d, mahu 422. Badan: %s", rec.Code, rec.Body.String())
	}
	if got := registrationStatusByID(t, pool, regID); got != "registered" {
		t.Errorf("status = %q, mahu kekal \"registered\" — ahli baru sahaja "+
			"memusnahkan kelayakan sijilnya sendiri", got)
	}
}

// Guard MESTI hidup dalam SQL juga, bukan hanya dalam handler. Kalau ia
// cuma dalam handler, mana-mana laluan tulis masa hadapan (job latar,
// endpoint admin, skrip pembaikan) boleh memintasnya tanpa perasan.
//
// Diuji dengan memanggil query TERUS, memintas handler sepenuhnya.
func TestCancelGuardHidupDalamSQLBukanHanyaHandler(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivityEndingAt(t, pool, -24*time.Hour)
	regID := seedRegistrationFor(t, pool, activityID, userID)

	_, err := sqlc.New(pool).CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID,
		UserID:     userID,
	})
	if err == nil {
		t.Fatal("CancelRegistration BERJAYA pada aktiviti yang sudah tamat — " +
			"guard hanya dalam handler, jadi mana-mana laluan lain boleh " +
			"memintasnya")
	}
	if got := registrationStatusByID(t, pool, regID); got != "registered" {
		t.Errorf("status = %q, mahu kekal \"registered\"", got)
	}
}
