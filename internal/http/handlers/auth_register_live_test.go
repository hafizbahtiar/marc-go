package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
	"marc/internal/email"
)

// Ujian integrasi terhadap Postgres sebenar untuk POST /auth/register
// (Task 4, pengesahan staff_id). Real Postgres diperlukan sebab yang
// diuji ialah kekangan `staff_id NOT NULL UNIQUE` (20260902100000_add_staff_id.sql)
// dan sifat transaksi CreateProfile - dua-dua hilang kalau lapisan DB
// dimock. Dilangkau melainkan HANDLER_TEST_DB diset (lihat statusTestPool
// dalam profile_status_live_test.go).

// registerHandler - padanan resetHandler (password_reset_live_test.go):
// bina AuthHandler terus, emailClient tanpa kredential supaya
// penghantaran emel pengesahan jadi no-op senyap.
func registerHandler(pool *pgxpool.Pool) *AuthHandler {
	return NewAuthHandler(
		pool,
		auth.NewJWT("ujian-rahsia", 15*time.Minute),
		30*24*time.Hour,
		email.NewClient("", ""),
		"http://localhost:8080",
		"https://marc.test/sahkan-emel",
		"https://marc.test/reset-kata-laluan",
	)
}

func registerCall(t *testing.T, pool *pgxpool.Pool, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/auth/register", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	registerHandler(pool).Register(c)
	c.Writer.WriteHeaderNow()
	return rec
}

// createTestProfileWithStaffID - mirrors createTestPendingProfile
// (staff_id_query_live_test.go) but lets the caller pin an exact
// staff_id, needed to set up a duplicate-staff_id collision.
func createTestProfileWithStaffID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, staffID string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	email := "staffid-dup-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = 'ahli'), 'pending')`,
		userID, staffID); err != nil {
		t.Fatalf("seed profile with staff_id: %v", err)
	}
	return userID
}

func TestRegister_RequiresStaffID(t *testing.T) {
	pool, _ := statusTestPool(t)

	body := `{"email":"new-` + uuid.NewString() + `@example.com","password":"Password1!","phone":"0123456789"}`
	rec := registerCall(t, pool, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing staff_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRegister_DefersMemberID(t *testing.T) {
	pool, ctx := statusTestPool(t)

	emailAddr := "staff-" + uuid.NewString() + "@example.com"
	body := `{"email":"` + emailAddr + `","password":"Password1!","phone":"0123456789","staff_id":"EMP-001-` + uuid.NewString()[:8] + `"}`
	rec := registerCall(t, pool, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected success, got %d: %s", rec.Code, rec.Body.String())
	}

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `select id from users where email = $1`, emailAddr).Scan(&userID); err != nil {
		t.Fatalf("find seeded user: %v", err)
	}

	q := sqlc.New(pool)
	profile, err := q.GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.MemberID.Valid {
		t.Fatalf("expected member_id NULL until verification, got %v", profile.MemberID)
	}
	if profile.StaffID == "" {
		t.Fatal("expected staff_id to be set")
	}
	if profile.StaffIDVerifiedAt.Valid {
		t.Fatal("expected staff_id_verified_at NULL until manager verification")
	}
}

func TestRegister_StaffIDWithSlashRejected(t *testing.T) {
	pool, _ := statusTestPool(t)

	body := `{"email":"new-` + uuid.NewString() + `@example.com","password":"Password1!","phone":"0123456789","staff_id":"EMP/001"}`
	rec := registerCall(t, pool, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for staff_id containing '/', got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRegister_DuplicateStaffIDReturns409(t *testing.T) {
	pool, ctx := statusTestPool(t)

	dupStaffID := "EMP-DUP-" + uuid.NewString()[:8]
	createTestProfileWithStaffID(t, ctx, pool, dupStaffID)

	body := `{"email":"other-` + uuid.NewString() + `@example.com","password":"Password1!","phone":"0123456789","staff_id":"` + dupStaffID + `"}`
	rec := registerCall(t, pool, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 duplicate staff_id, got %d: %s", rec.Code, rec.Body.String())
	}
}
