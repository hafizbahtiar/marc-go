package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// Carian emel case-insensitive: handler sendiri `strings.ToLower()` emel
// sebelum queri (queri GetUserByEmail ialah `email = $1` biasa, sensitif
// huruf besar/kecil di lapisan SQL); index users_email_lowercase_unique
// cuma pastikan tiada dua akaun dengan emel yang sama selepas dikecilkan,
// jadi carian ToLower() tak pernah bertaburan (ambiguous).
func TestRequestResetEmelDinormalkan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	emel := emailOf(t, pool, userID)

	resetRequestCall(t, pool, `{"email":"`+strings.ToUpper(emel)+`"}`)

	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d — emel huruf besar tak dipadan case-insensitive", got)
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

// tokenMentahUntuk cipta token reset dan pulangkan bentuk MENTAHnya
// (yang biasanya hanya wujud dalam emel).
func tokenMentahUntuk(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, ttl time.Duration) string {
	t.Helper()
	raw, err := auth.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`insert into password_reset_tokens (user_id, token_hash, expires_at)
		 values ($1, $2, now() + $3::interval)`,
		userID, auth.HashToken(raw), ttl.String()); err != nil {
		t.Fatalf("sisip token: %v", err)
	}
	return raw
}

func resetConfirmCall(t *testing.T, pool *pgxpool.Pool, token, katalaluan string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/password-reset/confirm",
		strings.NewReader(`{"token":"`+token+`","password":"`+katalaluan+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	resetHandler(pool).ConfirmPasswordReset(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func passwordSah(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, katalaluan string) bool {
	t.Helper()
	var hash string
	if err := pool.QueryRow(context.Background(),
		`select password_hash from users where id = $1`, userID).Scan(&hash); err != nil {
		t.Fatalf("baca hash: %v", err)
	}
	return auth.VerifyPassword(hash, katalaluan)
}

func TestConfirmResetMenukarKataLaluan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	rec := resetConfirmCall(t, pool, token, "kata-laluan-baharu")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d, mahu 204. Badan: %s", rec.Code, rec.Body.String())
	}
	if !passwordSah(t, pool, userID, "kata-laluan-baharu") {
		t.Error("kata laluan baharu tak berkuat kuasa")
	}
	if passwordSah(t, pool, userID, "x") {
		t.Error("kata laluan lama masih diterima")
	}
}

// Sekali-guna: pautan yang sama tak boleh mereset dua kali.
func TestConfirmResetSekaliGuna(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	if rec := resetConfirmCall(t, pool, token, "pertama-123"); rec.Code != http.StatusNoContent {
		t.Fatalf("guna pertama: kod = %d", rec.Code)
	}
	rec := resetConfirmCall(t, pool, token, "kedua-456")

	if rec.Code == http.StatusNoContent {
		t.Fatal("pautan yang SAMA mereset dua kali — token bukan sekali-guna")
	}
	if !passwordSah(t, pool, userID, "pertama-123") {
		t.Error("guna kedua menukar kata laluan walaupun ditolak")
	}
}

// Sekali-guna mesti berkuat kuasa di bawah PERLUMBAAN, bukan hanya
// secara berjujukan. TestConfirmResetSekaliGuna memanggil satu demi
// satu, jadi ia tak pernah menguji ini.
//
// `delete ... returning` menjadikannya deterministik: kunci baris
// Postgres menyerikan kedua-dua permintaan, yang kedua mengena 0 baris.
func TestConfirmResetSekaliGunaDiBawahPerlumbaan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	var wg sync.WaitGroup
	kod := make([]int, 2)
	for i := range kod {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			kod[n] = resetConfirmCall(t, pool, token, "serentak-123").Code
		}(i)
	}
	wg.Wait()

	berjaya := 0
	for _, c := range kod {
		if c == http.StatusNoContent {
			berjaya++
		}
	}
	if berjaya != 1 {
		t.Fatalf("%d permintaan serentak berjaya, mahu TEPAT 1 — token "+
			"boleh dituntut lebih drpd sekali di bawah perlumbaan", berjaya)
	}
}

func TestConfirmResetTokenLuputDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, -time.Minute)

	if rec := resetConfirmCall(t, pool, token, "baharu-123"); rec.Code == http.StatusNoContent {
		t.Fatal("token LUPUT diterima")
	}
	if passwordSah(t, pool, userID, "baharu-123") {
		t.Error("token luput menukar kata laluan")
	}
}

func TestConfirmResetTokenTidakSahDitolak(t *testing.T) {
	pool := activityTestPool(t)

	if rec := resetConfirmCall(t, pool, "token-rekaan-yang-tak-wujud", "baharu-123"); rec.Code == http.StatusNoContent {
		t.Fatal("token rekaan diterima")
	}
}

// INTI: reset MESTI membatalkan setiap sesi. Sebab orang reset selalunya
// kerana syak akaun dikompromi — membiarkan refresh token penyerang hidup
// mengalahkan tujuannya.
func TestConfirmResetMembatalkanSemuaRefreshToken(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(ctx,
			`insert into refresh_tokens (user_id, token_hash, expires_at, family_id)
			 values ($1, $2, now() + interval '30 days', gen_random_uuid())`,
			userID, auth.HashToken(uuid.NewString())); err != nil {
			t.Fatalf("sisip refresh token: %v", err)
		}
	}

	token := tokenMentahUntuk(t, pool, userID, time.Hour)
	if rec := resetConfirmCall(t, pool, token, "baharu-123"); rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d", rec.Code)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`select count(*) from refresh_tokens where user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatalf("kira refresh token: %v", err)
	}
	if n != 0 {
		t.Fatalf("refresh token tinggal = %d, mahu 0 — sesi penyerang kekal "+
			"hidup selepas mangsa reset kata laluan", n)
	}
}

func TestConfirmResetBerfungsiUntukAhliPending(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "pending")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	if rec := resetConfirmCall(t, pool, token, "baharu-123"); rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d untuk ahli pending, mahu 204", rec.Code)
	}
}

// Reset TIDAK menanda emel disahkan. Mengklik pautan memang membuktikan
// kawalan emel — tapi menggabungkan keduanya bermakna akaun yang
// dikompromi lalu direset senyap memperoleh status disahkan.
func TestConfirmResetTidakMenandaEmailVerified(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	resetConfirmCall(t, pool, token, "baharu-123")

	var verified bool
	if err := pool.QueryRow(ctx,
		`select email_verified from profiles where user_id = $1`, userID).Scan(&verified); err != nil {
		t.Fatalf("baca email_verified: %v", err)
	}
	if verified {
		t.Error("reset menanda email_verified = true")
	}
}

// Kata laluan pendek ditolak — peraturan sama dengan /auth/register.
func TestConfirmResetTolakKataLaluanPendek(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := tokenMentahUntuk(t, pool, userID, time.Hour)

	if rec := resetConfirmCall(t, pool, token, "abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("kod = %d, mahu 400", rec.Code)
	}
	if got := countResetTokens(t, pool, userID); got != 1 {
		t.Errorf("token = %d — permintaan tak sah tak patut membakar token", got)
	}
}
