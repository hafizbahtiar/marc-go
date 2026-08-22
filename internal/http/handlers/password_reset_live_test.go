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
	"marc/internal/email"
)

// L32 — reset kata laluan.
//
// `emailClient` dibina TANPA kredential (`Enabled()` false) jadi
// penghantaran jadi no-op senyap tanpa rangkaian; token tetap ditulis ke
// DB, yang itulah yang diuji di sini.
func resetHandler(pool *pgxpool.Pool) *AuthHandler {
	return NewAuthHandler(
		pool,
		auth.NewJWT("ujian-rahsia", 15*time.Minute),
		30*24*time.Hour,
		email.NewClient("", ""),
		"http://localhost:8080",
		"",
		"https://marc.test/reset-kata-laluan",
	)
}

func resetRequestCall(t *testing.T, pool *pgxpool.Pool, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/auth/password-reset/request", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	resetHandler(pool).RequestPasswordReset(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func countResetTokens(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from password_reset_tokens where user_id = $1`,
		userID).Scan(&n); err != nil {
		t.Fatalf("kira token: %v", err)
	}
	return n
}

func emailOf(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	var e string
	if err := pool.QueryRow(context.Background(),
		`select email from users where id = $1`, userID).Scan(&e); err != nil {
		t.Fatalf("baca emel: %v", err)
	}
	return e
}

// Invarian bukan-enumerasi: emel tak dikenali kelihatan IDENTIKAL dengan
// yang dikenali dari luar.
func TestRequestResetEmelTakDikenaliPulang204(t *testing.T) {
	pool := activityTestPool(t)

	rec := resetRequestCall(t, pool, `{"email":"tiada-`+uuid.NewString()+`@test.local"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d, mahu 204 — respons membocorkan sama ada akaun wujud. Badan: %s",
			rec.Code, rec.Body.String())
	}
}

func TestRequestResetMenciptaToken(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")

	rec := resetRequestCall(t, pool, `{"email":"`+emailOf(t, pool, userID)+`"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d, mahu 204", rec.Code)
	}
	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d, mahu 1", got)
	}
}

// Permintaan kedua mesti membunuh pautan pertama — kalau tidak, setiap
// permintaan menambah satu lagi kelayakan hidup pada akaun yang sama.
func TestRequestResetKeduaMembatalkanYangPertama(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	emel := emailOf(t, pool, userID)

	resetRequestCall(t, pool, `{"email":"`+emel+`"}`)
	resetRequestCall(t, pool, `{"email":"`+emel+`"}`)

	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d selepas dua permintaan, mahu 1 — pautan lama "+
			"kekal hidup, jadi setiap permintaan menambah kelayakan", got)
	}
}

// Emel dinormalkan sebelum carian, sama seperti login/register.
func TestRequestResetEmelDinormalkan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	emel := emailOf(t, pool, userID)

	resetRequestCall(t, pool, `{"email":"  `+strings.ToUpper(emel)+`  "}`)

	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d — emel huruf besar/berruang tak dinormalkan", got)
	}
}

// Ahli `pending` MESTI boleh reset: mereka yang paling mungkin terkunci
// keluar, dan tiada laluan lain untuk pulih.
func TestRequestResetBerfungsiUntukAhliPending(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "pending")

	resetRequestCall(t, pool, `{"email":"`+emailOf(t, pool, userID)+`"}`)

	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d untuk ahli pending, mahu 1", got)
	}
}

// `PASSWORD_RESET_URL` kosong = ciri dimatikan. 503 jelas, bukan pautan
// rosak dalam emel ahli.
func TestRequestResetTanpaURLDikonfigurPulang503(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/password-reset/request",
		strings.NewReader(`{"email":"`+emailOf(t, pool, userID)+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	NewAuthHandler(pool, auth.NewJWT("x", time.Minute), time.Hour,
		email.NewClient("", ""), "http://localhost:8080", "", "").RequestPasswordReset(c)
	c.Writer.WriteHeaderNow()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("kod = %d, mahu 503", rec.Code)
	}
	if got := countResetTokens(t, pool, userID); got != 0 {
		t.Errorf("token = %d ditulis walaupun ciri dimatikan", got)
	}
}
