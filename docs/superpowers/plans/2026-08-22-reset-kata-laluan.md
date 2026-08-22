# Reset Kata Laluan — Pelan Pelaksanaan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ahli yang lupa kata laluan boleh pulih sendiri melalui pautan emel, tanpa staf mengemas kini DB secara manual.

**Architecture:** Jadual `password_reset_tokens` mencerminkan `email_verification_tokens`. Dua endpoint awam: `request` (sentiasa 204, dipanggil dari app) dan `confirm` (CORS, dipanggil dari halaman Astro). Ahli menaip kata laluan baharu di `marc_astro`, bukan dalam app — tiada app-link https dikonfigur, jadi pautan emel membuka pelayar.

**Tech Stack:** Go 1.26 + Gin + sqlc + goose + pgx/v5 · Astro 7 · Flutter + Riverpod + go_router

**Spec:** `docs/superpowers/specs/2026-08-22-reset-kata-laluan-design.md`

## Global Constraints

- Kata laluan: `binding:"required,min=6,max=72"` — 72 ialah had bcrypt, bukan pilihan sewenang-wenang. Padan `/auth/register`.
- TTL token: **1 jam**, sama seperti `emailVerificationTTL`.
- Token: `auth.GenerateOpaqueToken()` (32 bait) disimpan sebagai `auth.HashToken()` (SHA-256 hex). Token mentah HANYA dalam emel.
- `request` pulang **204 SENTIASA** — tiada enumerasi akaun.
- Emel dihantar dalam **goroutine** — mitigasi separa oracle masa.
- Reset MESTI membatalkan **semua** refresh token ahli, dalam transaksi yang sama.
- Reset TIDAK menanda `email_verified = true`.
- Berfungsi untuk **sebarang** `profiles.status` (termasuk `pending`/`rejected`).
- Baldi had kadar bernama **`password-reset`** — jangan kongsi `auth`.
- Semua komen kod, mesej ralat dan teks UI dalam **Bahasa Melayu**, ikut repo.
- DB ujian: guna DB buangan. `HANDLER_TEST_DB` / `ACTIVITY_TEST_DB`.

---

### Task 1: Skema + query

**Files:**
- Create: `internal/db/migrations/20260823090000_create_password_reset_tokens.sql`
- Create: `queries/password_reset_tokens.sql`
- Modify: `queries/users.sql` (tambah `UpdateUserPassword` di hujung)
- Modify: `DATABASE.md` (jadual "Identiti & akses")
- Test: `internal/http/handlers/password_reset_query_live_test.go`

**Interfaces:**
- Consumes: `auth.GenerateOpaqueToken`, `auth.HashToken`, `auth.HashPassword` (sedia ada)
- Produces: `sqlc.CreatePasswordResetToken(ctx, CreatePasswordResetTokenParams{UserID uuid.UUID, TokenHash string, ExpiresAt pgtype.Timestamptz}) (PasswordResetToken, error)`; `sqlc.GetPasswordResetTokenByHash(ctx, string) (PasswordResetToken, error)`; `sqlc.DeletePasswordResetTokensByUser(ctx, uuid.UUID) error`; `sqlc.UpdateUserPassword(ctx, UpdateUserPasswordParams{ID uuid.UUID, PasswordHash string}) error`

- [ ] **Step 1: Tulis migration**

```sql
-- +goose Up

-- Reset kata laluan (L32). Cerminan `email_verification_tokens` — sama
-- bentuk, sama kitaran hayat, sengaja jadual BERASINGAN.
--
-- Kenapa bukan guna semula jadual pengesahan emel dgn lajur `purpose`:
-- ia menggabungkan dua kitaran hayat berbeza dan memerlukan migration
-- atas jadual yang sedang berfungsi — membeli kekemasan skema dengan
-- risiko pada laluan yang tiada kaitan.
--
-- Kenapa bukan token bertandatangan tanpa keadaan (JWT): token reset
-- MESTI sekali-guna dan MESTI boleh dibatalkan sebelum luput. Token
-- tanpa keadaan tak boleh jadi kedua-duanya.
create table password_reset_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  -- SHA-256 bagi token legap 32 bait. Token MENTAH hanya wujud dalam
  -- emel — kalau DB bocor, hash tak boleh mereset apa-apa.
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index password_reset_tokens_user_id_idx on password_reset_tokens(user_id);

-- +goose Down
drop table if exists password_reset_tokens;
```

- [ ] **Step 2: Tulis fail query**

Cipta `queries/password_reset_tokens.sql`:

```sql
-- name: CreatePasswordResetToken :one
insert into password_reset_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: GetPasswordResetTokenByHash :one
select * from password_reset_tokens where token_hash = $1;

-- name: DeletePasswordResetTokensByUser :exec
-- Dipanggil DUA tempat, atas sebab berbeza:
--   request — permintaan baharu membunuh pautan lama
--   confirm — sekali-guna, dalam transaksi yang sama dgn tukar kata laluan
delete from password_reset_tokens where user_id = $1;
```

Tambah di HUJUNG `queries/users.sql`:

```sql
-- name: UpdateUserPassword :exec
update users set password_hash = $2 where id = $1;
```

- [ ] **Step 3: Jana sqlc**

Run: `sqlc generate && go build ./...`
Expected: tiada output, exit 0.

- [ ] **Step 4: Tulis ujian yang gagal**

Cipta `internal/http/handlers/password_reset_query_live_test.go`:

```go
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
)

// Lapisan query reset kata laluan (L32). Diuji berasingan daripada
// handler supaya invarian skema (unik, cascade, sekali-guna melalui
// padam) dipegang oleh ujiannya sendiri.

func TestPasswordResetTokenPusinganPenuh(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	raw, err := auth.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}

	created, err := q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(raw),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	got, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw))
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash: %v", err)
	}
	if got.ID != created.ID || got.UserID != userID {
		t.Fatalf("baris tak sepadan: got=%v created=%v", got.ID, created.ID)
	}

	// Token MENTAH tak boleh mencari apa-apa — hanya hash disimpan.
	if _, err := q.GetPasswordResetTokenByHash(ctx, raw); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token MENTAH memadankan baris — token disimpan tanpa hash")
	}

	if err := q.DeletePasswordResetTokensByUser(ctx, userID); err != nil {
		t.Fatalf("DeletePasswordResetTokensByUser: %v", err)
	}
	if _, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token masih wujud selepas dipadam")
	}
}

func TestUpdateUserPasswordMenukarHash(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	baharu, err := auth.HashPassword("kata-laluan-baharu")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
		ID: userID, PasswordHash: baharu,
	}); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	user, err := q.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if !auth.VerifyPassword(user.PasswordHash, "kata-laluan-baharu") {
		t.Fatal("kata laluan baharu tak disahkan selepas kemas kini")
	}
}

// `on delete cascade` — memadam user mesti membawa tokennya sekali,
// kalau tidak baris yatim menghalang pemadaman akaun.
func TestPasswordResetTokenCascadeBilaUserDipadam(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	raw, _ := auth.GenerateOpaqueToken()
	if _, err := q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(raw),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	if _, err := pool.Exec(ctx, `delete from profiles where user_id = $1`, userID); err != nil {
		t.Fatalf("padam profil: %v", err)
	}
	if _, err := pool.Exec(ctx, `delete from users where id = $1`, userID); err != nil {
		t.Fatalf("padam user (cascade token gagal?): %v", err)
	}

	if _, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token bertahan selepas user dipadam — cascade tak berkuat kuasa")
	}
}
```

- [ ] **Step 5: Jalankan ujian, sahkan ia LULUS**

Run:
```bash
createdb marc_l32 2>/dev/null
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestPasswordResetToken|TestUpdateUserPassword' -v
```
Expected: 3 PASS.

- [ ] **Step 6: Kemas kini DATABASE.md**

Dalam jadual "Identiti & akses", selepas baris `email_verification_tokens`:

```markdown
| `password_reset_tokens` | hash token reset kata laluan (TTL 1 jam). Jadual BERASINGAN drpd pengesahan emel — dua kitaran hayat berbeza; lihat `docs/superpowers/specs/2026-08-22-reset-kata-laluan-design.md` |
```

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/20260823090000_create_password_reset_tokens.sql \
  queries/password_reset_tokens.sql queries/users.sql internal/db/sqlc/ \
  internal/http/handlers/password_reset_query_live_test.go DATABASE.md
git commit -m "feat(auth): skema + query token reset kata laluan (L32)"
```

---

### Task 2: `POST /auth/password-reset/request`

**Files:**
- Modify: `internal/config/config.go` (medan `PasswordResetURL`)
- Modify: `.env.example`
- Modify: `internal/http/handlers/auth.go` (medan handler + `RequestPasswordReset`)
- Modify: `internal/http/router.go` (baldi had kadar + route)
- Modify: `cmd/api/main.go` (hantar `cfg.PasswordResetURL`)
- Test: `internal/http/handlers/password_reset_live_test.go`

**Interfaces:**
- Consumes: Task 1 queries
- Produces: `(*AuthHandler).RequestPasswordReset(c *gin.Context)`; `NewAuthHandler(pool, jwtSvc, refreshTTL, emailClient, publicBaseURL, emailVerifyURL, passwordResetURL string) *AuthHandler` (parameter KETUJUH ditambah)

- [ ] **Step 1: Tulis ujian yang gagal**

Cipta `internal/http/handlers/password_reset_live_test.go`:

```go
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
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run:
```bash
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestRequestReset 2>&1 | head -5
```
Expected: `[build failed]` — `NewAuthHandler` belum menerima tujuh argumen, `RequestPasswordReset` belum wujud.

- [ ] **Step 3: Tambah config**

Dalam `internal/config/config.go`, tambah medan selepas `CertificateVerifyURL`:

```go
	// PasswordResetURL — URL PENUH halaman Astro tempat ahli menaip kata
	// laluan baharu (token dilampir sebagai `?token=`). Padanan pola
	// EmailVerifyURL, TAPI dengan satu perbezaan: kosong bermakna ciri
	// DIMATIKAN (503), bukan jatuh balik ke halaman Go sendiri. Borang
	// kata laluan bukan sesuatu yang patut muncul daripada halaman
	// sandaran yang tiada siapa reka.
	PasswordResetURL string
```

Dalam `Load()`, selepas `CertificateVerifyURL`:

```go
		PasswordResetURL: os.Getenv("PASSWORD_RESET_URL"),
```

Dalam `.env.example`, selepas blok `CERTIFICATE_VERIFY_URL`:

```bash
# Optional — halaman Astro tempat ahli taip kata laluan baharu selepas
# klik pautan reset. Kosong = ciri reset kata laluan DIMATIKAN (endpoint
# pulang 503), bukan fallback ke halaman Go. Perlukan juga
# CORS_ALLOWED_ORIGINS diisi dgn origin laman web tu.
PASSWORD_RESET_URL=
```

- [ ] **Step 4: Tambah medan + handler**

Dalam `internal/http/handlers/auth.go`, tambah pemalar berhampiran `emailVerificationTTL`:

```go
// passwordResetTTL — sama 1 jam dengan pengesahan emel. Token reset
// memberi kawalan PENUH akaun, jadi tetingkapnya tak patut lebih longgar
// daripada token yang cuma mengesahkan alamat.
const passwordResetTTL = time.Hour
```

Tambah medan pada `AuthHandler` selepas `emailVerifyURL`:

```go
	passwordResetURL string
```

Tukar `NewAuthHandler` — tambah parameter ketujuh dan tetapkan medan:

```go
func NewAuthHandler(
	pool *pgxpool.Pool,
	jwtSvc *auth.JWT,
	refreshTTL time.Duration,
	emailClient *email.Client,
	publicBaseURL string,
	emailVerifyURL string,
	passwordResetURL string,
) *AuthHandler {
	return &AuthHandler{
		pool:             pool,
		queries:          sqlc.New(pool),
		jwt:              jwtSvc,
		refreshTTL:       refreshTTL,
		emailClient:      emailClient,
		publicBaseURL:    publicBaseURL,
		emailVerifyURL:   emailVerifyURL,
		passwordResetURL: passwordResetURL,
	}
}
```

Tambah handler di hujung `auth.go`:

```go
type passwordResetRequestBody struct {
	Email string `json:"email" binding:"required,email"`
}

// RequestPasswordReset — POST /auth/password-reset/request. AWAM.
//
// Pulang 204 SENTIASA, sama ada akaun wujud atau tidak. Kalau ia
// membezakan, endpoint ni jadi alat menyenaraikan emel mana yang
// berdaftar. UI mengimbangi dgn mesej "Kalau emel itu berdaftar, kami
// dah hantar pautan reset" — ahli yang tersilap taip tetap dapat maklum
// balas berguna tanpa server mengesahkan kewujudan akaun.
//
// TIADA gate status: ahli `pending`/`rejected` yang paling mungkin
// terkunci keluar, dan tiada laluan lain untuk mereka pulih. Alasan sama
// dengan `/me` (lihat ARCHITECTURE.md, Lapisan akses).
func (h *AuthHandler) RequestPasswordReset(c *gin.Context) {
	// Ciri dimatikan bila halaman belum dikonfigur — disemak SEBELUM
	// sebarang kerja DB supaya tiada token ditulis untuk pautan yang
	// takkan pernah boleh dibuka.
	if h.passwordResetURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "reset kata laluan belum tersedia",
		})
		return
	}

	var req passwordResetRequestBody
	if !bindJSON(c, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	ctx := c.Request.Context()
	user, err := h.queries.GetUserByEmail(ctx, req.Email)
	if err != nil {
		// Akaun tiada. Pulang 204 yang SAMA — lihat komen fungsi.
		c.Status(http.StatusNoContent)
		return
	}

	// Permintaan baharu membunuh pautan lama: tanpa ni, setiap permintaan
	// menambah satu lagi kelayakan hidup pada akaun yang sama.
	if err := h.queries.DeletePasswordResetTokensByUser(ctx, user.ID); err != nil {
		log.Printf("padam token reset lama (user=%s): %v", user.ID, err)
		c.Status(http.StatusNoContent)
		return
	}

	token, err := auth.GenerateOpaqueToken()
	if err != nil {
		log.Printf("jana token reset (user=%s): %v", user.ID, err)
		c.Status(http.StatusNoContent)
		return
	}
	if _, err := h.queries.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		UserID:    user.ID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(passwordResetTTL), Valid: true},
	}); err != nil {
		log.Printf("simpan token reset (user=%s): %v", user.ID, err)
		c.Status(http.StatusNoContent)
		return
	}

	link := fmt.Sprintf("%s?token=%s", h.passwordResetURL, token)
	if !h.emailClient.Enabled() {
		log.Printf("reset kata laluan (provider belum configure) untuk user %s: %s", user.ID, link)
		c.Status(http.StatusNoContent)
		return
	}

	// Dihantar dalam GOROUTINE untuk MASA, bukan latensi. Kalau akaun
	// wujud kita panggil Resend (~200ms); kalau tidak kita pulang
	// serta-merta. Perbezaan itu ialah oracle enumerasi yang mengalahkan
	// keputusan 204 di atas.
	//
	// Mitigasi SEPARA: kerja DB masih berbeza beberapa milisaat antara
	// dua laluan. Jauh di bawah bunyi rangkaian, jadi diterima — tapi
	// bukan sifar, dan tiada siapa patut membaca ni dan menganggap
	// masanya seragam.
	//
	// ctx permintaan SENGAJA tidak digunakan: ia dibatalkan sebaik
	// respons ditulis (padanan notifyMembers, activities.go).
	html := fmt.Sprintf(
		`<p>Kami terima permintaan untuk reset kata laluan akaun MARC anda. `+
			`Klik pautan di bawah untuk tetapkan kata laluan baharu (luput dalam 1 jam):</p>`+
			`<p><a href="%s">%s</a></p>`+
			`<p>Kalau bukan anda yang minta, abaikan emel ni — kata laluan anda tak berubah.</p>`,
		link, link,
	)
	go func(to string) {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.emailClient.Send(sendCtx, to, "Reset Kata Laluan MARC", html); err != nil {
			log.Printf("gagal hantar emel reset kata laluan: %v", err)
		}
	}(user.Email)

	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 5: Wire route + rate limiter + main.go**

Dalam `internal/http/router.go`, tambah parameter `passwordResetURL string` selepas `certificateVerifyURL`, hantar ke `NewAuthHandler`, dan tambah selepas baris `authSessionRateLimiter`:

```go
	// Baldi BERASINGAN daripada 'auth' (pengajaran L26): trafik reset tak
	// patut menghabiskan kuota log masuk ahli, dan sebaliknya. Seketat
	// 'auth' sebab setiap permintaan yang berjaya mencetuskan penghantaran
	// emel.
	passwordResetRateLimiter := rateLimiter.Limit("password-reset", authRateLimit, authRateBurst)
```

Tambah route dalam `authGroup` selepas baris verify-email:

```go
	authGroup.POST("/password-reset/request", passwordResetRateLimiter, authHandler.RequestPasswordReset)
```

Dalam `cmd/api/main.go`, tambah `cfg.PasswordResetURL` sebagai argumen TERAKHIR panggilan `httpapi.NewRouter(...)`.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run:
```bash
go build ./... && ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestRequestReset -v
```
Expected: 6 PASS.

- [ ] **Step 7: Ujian mutasi — sahkan guard "batalkan yang lama" nyata**

Run:
```bash
cp internal/http/handlers/auth.go /tmp/auth.bak
perl -0pi -e 's/\tif err := h\.queries\.DeletePasswordResetTokensByUser\(ctx, user\.ID\); err != nil \{\n\t\tlog\.Printf\("padam token reset lama \(user=%s\): %v", user\.ID, err\)\n\t\tc\.Status\(http\.StatusNoContent\)\n\t\treturn\n\t\}\n//' internal/http/handlers/auth.go
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestRequestResetKeduaMembatalkan 2>&1 | grep -E "FAIL|ok"
cp /tmp/auth.bak internal/http/handlers/auth.go
```
Expected: `FAIL` dengan "token = 2 selepas dua permintaan". Kemudian pulih dan sahkan `ok`.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go .env.example internal/http/handlers/auth.go \
  internal/http/router.go cmd/api/main.go \
  internal/http/handlers/password_reset_live_test.go
git commit -m "feat(auth): endpoint minta reset kata laluan (L32)"
```

---

### Task 3: `POST /auth/password-reset/confirm`

**Files:**
- Modify: `internal/http/handlers/auth.go` (`ConfirmPasswordReset`)
- Modify: `internal/http/router.go` (route + CORS + OPTIONS)
- Modify: `ARCHITECTURE.md` (bahagian Auth)
- Test: `internal/http/handlers/password_reset_live_test.go` (tambah)

**Interfaces:**
- Consumes: Task 1 queries; `resetHandler`, `countResetTokens`, `emailOf` daripada Task 2
- Produces: `(*AuthHandler).ConfirmPasswordReset(c *gin.Context)`

- [ ] **Step 1: Tulis ujian yang gagal**

Tambah ke `internal/http/handlers/password_reset_live_test.go`:

```go
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
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run:
```bash
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestConfirmReset 2>&1 | head -5
```
Expected: `[build failed]` — `ConfirmPasswordReset` belum wujud.

- [ ] **Step 3: Tulis handler**

Tambah ke `internal/http/handlers/auth.go`:

```go
type passwordResetConfirmBody struct {
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required,min=6,max=72"`
}

// ConfirmPasswordReset — POST /auth/password-reset/confirm. AWAM.
//
// Dipanggil dari halaman Astro (bukan app), jadi route ni dapat CORS +
// pengendali OPTIONS — padanan tepat verify-email/confirm.
//
// Keempat-empat tulisan berlaku dalam SATU transaksi. Kalau mana-mana
// gagal, tiada satu pun berlaku: kata laluan yang bertukar tanpa
// pembatalan sesi meninggalkan sesi penyerang hidup, dan token yang
// dipadam tanpa tukar kata laluan mengunci ahli keluar sepenuhnya.
func (h *AuthHandler) ConfirmPasswordReset(c *gin.Context) {
	var req passwordResetConfirmBody
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	rec, err := h.queries.GetPasswordResetTokenByHash(ctx, auth.HashToken(req.Token))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pautan tidak sah"})
		return
	}
	if rec.ExpiresAt.Time.Before(time.Now()) {
		// Dipadam supaya token luput tak berlonggok dalam jadual.
		_ = h.queries.DeletePasswordResetTokensByUser(ctx, rec.UserID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "pautan sudah luput"})
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	if err := q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
		ID: rec.UserID, PasswordHash: hash,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}
	// Sekali-guna.
	if err := q.DeletePasswordResetTokensByUser(ctx, rec.UserID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}
	// Batalkan SETIAP sesi — lihat komen fungsi.
	if err := q.DeleteRefreshTokensByUser(ctx, rec.UserID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tukar kata laluan"})
		return
	}

	log.Printf("kata laluan direset untuk user %s, semua sesi dibatalkan", rec.UserID)
	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 4: Wire route dengan CORS**

Dalam `internal/http/router.go`, selepas route `password-reset/request`:

```go
	// CORS + OPTIONS: laluan ni dipanggil oleh halaman Astro melalui
	// fetch() silang-origin, sama seperti verify-email/confirm. Instance
	// BERASINGAN drpd verifyEmailCORS walaupun konfigurasinya sama —
	// menamakannya ikut laluan yang ia lindungi menjadikan niat boleh
	// dibaca, dan kedua-duanya bebas berubah kemudian.
	passwordResetCORS := middleware.CORS(corsAllowedOrigins, "POST, OPTIONS")
	authGroup.POST("/password-reset/confirm", passwordResetCORS, passwordResetRateLimiter, authHandler.ConfirmPasswordReset)
	authGroup.OPTIONS("/password-reset/confirm", passwordResetCORS)
```

- [ ] **Step 5: Jalankan ujian, sahkan ia LULUS**

Run:
```bash
go build ./... && ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestConfirmReset|TestRequestReset' -v
```
Expected: 14 PASS.

- [ ] **Step 6: Ujian mutasi — dua invarian teras**

Run:
```bash
cp internal/http/handlers/auth.go /tmp/auth.bak
# Mutasi A: buang pembatalan sesi
perl -0pi -e 's/\tif err := q\.DeleteRefreshTokensByUser\(ctx, rec\.UserID\); err != nil \{\n\t\tc\.JSON\(http\.StatusInternalServerError, gin\.H\{"error": "gagal tukar kata laluan"\}\)\n\t\treturn\n\t\}\n//' internal/http/handlers/auth.go
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestConfirmResetMembatalkanSemua 2>&1 | grep -E "FAIL|ok"
cp /tmp/auth.bak internal/http/handlers/auth.go

# Mutasi B: buang sekali-guna
perl -0pi -e 's/\tif err := q\.DeletePasswordResetTokensByUser\(ctx, rec\.UserID\); err != nil \{\n\t\tc\.JSON\(http\.StatusInternalServerError, gin\.H\{"error": "gagal tukar kata laluan"\}\)\n\t\treturn\n\t\}\n//' internal/http/handlers/auth.go
ACTIVITY_TEST_DB="postgres://$(whoami)@localhost:5432/marc_l32?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestConfirmResetSekaliGuna 2>&1 | grep -E "FAIL|ok"
cp /tmp/auth.bak internal/http/handlers/auth.go
go build ./...
```
Expected: kedua-dua mutasi menghasilkan `FAIL` dengan mesej penegasan yang betul; selepas dipulihkan, `go build` bersih.

- [ ] **Step 7: Kemas kini ARCHITECTURE.md**

Dalam bahagian "Auth: JWT access + opaque refresh (rotated)", tambah di hujung:

```markdown
### Reset kata laluan

Token legap 32 bait, disimpan sebagai hash SHA-256 dalam
`password_reset_tokens`, TTL 1 jam. Ahli menaip kata laluan baharu pada
halaman `marc_astro` — tiada app-link https dikonfigur, jadi pautan emel
membuka pelayar, bukan app.

Tiga sifat yang saling bergantung, kesemuanya dalam satu transaksi:

- **Sekali-guna** — token dipadam bersama tukar kata laluan.
- **Permintaan baharu membunuh yang lama** — kalau tidak setiap
  permintaan menambah satu lagi kelayakan hidup pada akaun yang sama.
- **Setiap sesi dibatalkan** — orang reset selalunya kerana syak akaun
  dikompromi; membiarkan refresh token penyerang hidup mengalahkan
  tujuannya.

`request` pulang **204 sentiasa** (bukan-enumerasi) dan menghantar emel
dalam goroutine supaya masa respons tak membocorkan kewujudan akaun —
mitigasi separa; lihat komennya. Ia TIDAK menanda `email_verified`.

`PASSWORD_RESET_URL` kosong = ciri dimatikan (503), bukan fallback HTML Go.
```

- [ ] **Step 8: Commit**

```bash
git add internal/http/handlers/auth.go internal/http/router.go \
  internal/http/handlers/password_reset_live_test.go ARCHITECTURE.md
git commit -m "feat(auth): endpoint sahkan reset kata laluan (L32)"
```

---

### Task 4: Halaman Astro

**Files:**
- Create: `../marc_astro/src/pages/reset-kata-laluan.astro`

**Interfaces:**
- Consumes: `POST {PUBLIC_API_BASE_URL}/auth/password-reset/confirm` daripada Task 3

- [ ] **Step 1: Cipta halaman**

Cipta `../marc_astro/src/pages/reset-kata-laluan.astro`. Ia mencerminkan
`sahkan-emel.astro` (Layout/Header/Footer, kelas `status__*`, blok
`<style>` disalin daripada fail itu) dengan SATU perbezaan: `sahkan-emel`
auto-hantar, ini perlukan borang.

```astro
---
import Layout from "../layouts/Layout.astro";
import Header from "../components/Header.astro";
import Footer from "../components/Footer.astro";
---

<Layout
	title="Reset Kata Laluan — MARC"
	description="Tetapkan kata laluan baharu untuk akaun MARC anda."
>
	<Header />
	<main class="status">
		<div class="status__card" role="status" aria-live="polite">
			<div id="state-form" class="status__state" hidden>
				<h1>Tetapkan kata laluan baharu</h1>
				<form id="reset-form" novalidate>
					<label for="password">Kata laluan baharu</label>
					<input id="password" type="password" autocomplete="new-password"
						minlength="6" required />
					<label for="confirm">Sahkan kata laluan</label>
					<input id="confirm" type="password" autocomplete="new-password"
						minlength="6" required />
					<p id="form-error" class="status__error" hidden></p>
					<button type="submit">Simpan kata laluan</button>
				</form>
			</div>
			<div id="state-loading" class="status__state" hidden>
				<div class="status__spinner" aria-hidden="true"></div>
				<h1>Menyimpan&hellip;</h1>
				<p>Sila tunggu sebentar.</p>
			</div>
			<div id="state-success" class="status__state" hidden>
				<div class="status__icon status__icon--ok" aria-hidden="true">&check;</div>
				<h1>Kata laluan ditukar!</h1>
				<p>Buka app MARC dan log masuk dengan kata laluan baharu anda.</p>
			</div>
			<div id="state-error" class="status__state" hidden>
				<div class="status__icon status__icon--err" aria-hidden="true">!</div>
				<h1>Tidak berjaya</h1>
				<p id="error-message">Pautan tidak sah atau telah luput.</p>
				<p class="status__hint">
					Kembali ke app MARC dan minta pautan reset yang baharu.
				</p>
			</div>
		</div>
	</main>
	<Footer />
</Layout>

<script define:vars={{ apiBaseUrl: import.meta.env.PUBLIC_API_BASE_URL ?? "" }}>
	const form = document.getElementById("state-form");
	const loading = document.getElementById("state-loading");
	const success = document.getElementById("state-success");
	const error = document.getElementById("state-error");
	const errorMessage = document.getElementById("error-message");
	const formError = document.getElementById("form-error");

	function show(target) {
		for (const node of [form, loading, success, error]) {
			node.hidden = node !== target;
		}
	}

	const token = new URLSearchParams(window.location.search).get("token");

	if (!token) {
		errorMessage.textContent = "Pautan tidak sah — token tiada.";
		show(error);
	} else if (!apiBaseUrl) {
		errorMessage.textContent =
			"Ralat konfigurasi laman web. Sila cuba lagi kemudian.";
		show(error);
	} else {
		show(form);
		document.getElementById("reset-form").addEventListener("submit", (ev) => {
			ev.preventDefault();
			const password = document.getElementById("password").value;
			const confirm = document.getElementById("confirm").value;

			// Semakan sisi klien untuk maklum balas serta-merta SAHAJA —
			// server tetap menguatkuasakan peraturan yang sama.
			if (password.length < 6) {
				formError.textContent = "Kata laluan mesti sekurang-kurangnya 6 aksara.";
				formError.hidden = false;
				return;
			}
			if (password !== confirm) {
				formError.textContent = "Kata laluan tidak sepadan.";
				formError.hidden = false;
				return;
			}
			formError.hidden = true;
			show(loading);

			fetch(`${apiBaseUrl}/auth/password-reset/confirm`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ token, password }),
			})
				.then((res) => {
					if (res.status === 204) {
						show(success);
						return;
					}
					return res
						.json()
						.catch(() => ({}))
						.then((body) => {
							errorMessage.textContent =
								body?.error || "Pautan tidak sah atau telah luput.";
							show(error);
						});
				})
				.catch(() => {
					errorMessage.textContent =
						"Tidak dapat menghubungi pelayan. Semak sambungan internet anda.";
					show(error);
				});
		});
	}
</script>

<style>
	/* Salin blok <style> penuh daripada src/pages/sahkan-emel.astro,
	   kemudian tambah gaya borang di bawah. */

	#reset-form {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 20px;
		text-align: left;
	}

	#reset-form label {
		font-size: 0.9rem;
		color: var(--color-muted);
	}

	#reset-form input {
		padding: 10px 12px;
		border: 1px solid var(--color-muted);
		border-radius: 8px;
		font: inherit;
		margin-bottom: 10px;
	}

	#reset-form button {
		margin-top: 8px;
		padding: 12px;
		border: 0;
		border-radius: 8px;
		font: inherit;
		font-weight: 600;
		cursor: pointer;
	}

	.status__error {
		color: #b00020;
		font-size: 0.9rem;
		margin: 4px 0 0;
	}
</style>
```

- [ ] **Step 2: Sahkan ia menyemak dan membina**

Run:
```bash
cd ../marc_astro && npx astro check && npm run build
```
Expected: sifar ralat, build berjaya.

> ⚠️ Repo ni **tiada suite ujian**. `astro check` + `build` ialah
> keseluruhan gate automatik; selebihnya pemeriksaan manual. Ini
> dinyatakan dalam spec sebagai jurang yang diketahui, bukan diabaikan.

- [ ] **Step 3: Commit (dalam marc_astro)**

```bash
cd ../marc_astro
git add src/pages/reset-kata-laluan.astro
git commit -m "feat: halaman reset kata laluan (marc_go L32)"
```

---

### Task 5: Skrin Flutter

**Files:**
- Create: `../marc_flutter/lib/features/auth/forgot_password_page.dart`
- Modify: `../marc_flutter/lib/features/auth/auth_service.dart`
- Modify: `../marc_flutter/lib/features/auth/auth_providers.dart`
- Modify: `../marc_flutter/lib/features/auth/login_page.dart:95`
- Modify: `../marc_flutter/lib/app/router.dart:72`
- Test: `../marc_flutter/test/features/auth/forgot_password_test.dart`

**Interfaces:**
- Consumes: `POST /auth/password-reset/request` daripada Task 2
- Produces: `AuthService.requestPasswordReset(String email) → Future<AuthResult>`; `forgotPasswordControllerProvider`; laluan `/forgot-password`

- [ ] **Step 1: Tulis ujian yang gagal**

Cipta `../marc_flutter/test/features/auth/forgot_password_test.dart`:

```dart
import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/features/auth/forgot_password_page.dart';

/// Mesej selepas hantar MESTI neutral — ia sama sama ada akaun wujud
/// atau tidak. Kalau ia berbeza, UI membocorkan apa yang backend sengaja
/// sembunyikan (lihat L32: request pulang 204 sentiasa).
void main() {
  test('mesej selepas hantar tidak mengesahkan kewujudan akaun', () {
    final m = forgotPasswordSentMessage.toLowerCase();

    expect(m, contains('kalau'));
    for (final bocor in ['tidak dijumpai', 'tak wujud', 'berjaya dihantar']) {
      expect(m, isNot(contains(bocor)),
          reason: 'mesej mengesahkan kewujudan akaun: "$forgotPasswordSentMessage"');
    }
  });

  test('429 dilayan sebagai ralat boleh cuba lagi, bukan kegagalan kekal', () {
    final e = DioException(
      requestOptions: RequestOptions(path: '/auth/password-reset/request'),
      response: Response(
        requestOptions: RequestOptions(path: '/auth/password-reset/request'),
        statusCode: 429,
      ),
    );
    expect(isRetryableResetError(e), isTrue);
  });
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `cd ../marc_flutter && flutter test test/features/auth/forgot_password_test.dart`
Expected: gagal kompil — `forgot_password_page.dart` belum wujud.

- [ ] **Step 3: Tambah kaedah servis**

Dalam `auth_service.dart`, selepas `signUp`:

```dart
  /// Minta pautan reset kata laluan. Backend pulang 204 SENTIASA (tiada
  /// enumerasi akaun), jadi "berjaya" di sini bermakna "permintaan
  /// diterima" — BUKAN "akaun itu wujud". Mesej UI mesti kekal neutral.
  Future<AuthResult> requestPasswordReset(String email) async {
    try {
      await _dio.post(
        '/auth/password-reset/request',
        data: {'email': email},
      );
      return const AuthResult(success: true);
    } on DioException catch (e) {
      return AuthResult(success: false, error: extractErrorMessage(e));
    } catch (_) {
      return const AuthResult(
        success: false,
        error: 'Ralat tidak dijangka. Cuba lagi.',
      );
    }
  }
```

- [ ] **Step 4: Tambah controller**

Dalam `auth_providers.dart`, selepas `RegisterController`:

```dart
class ForgotPasswordController extends AsyncNotifier<void> {
  @override
  Future<void> build() async {}

  /// Pulang true kalau permintaan diterima (page papar mesej neutral).
  Future<bool> submit(String email) async {
    state = const AsyncLoading();
    final result = await ref
        .read(authServiceProvider)
        .requestPasswordReset(email);
    if (result.success) {
      state = const AsyncData(null);
      return true;
    }
    state = AsyncError(
      result.error ?? 'Gagal hantar pautan reset',
      StackTrace.current,
    );
    return false;
  }
}

final forgotPasswordControllerProvider =
    AsyncNotifierProvider<ForgotPasswordController, void>(
      ForgotPasswordController.new,
    );
```

- [ ] **Step 5: Cipta halaman**

Cipta `../marc_flutter/lib/features/auth/forgot_password_page.dart`:

```dart
import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:marc/features/auth/auth_providers.dart';
import 'package:marc/features/auth/widgets/auth_field.dart';
import 'package:marc/features/auth/widgets/button_busy.dart';
import 'package:marc/shared/validators.dart';
import 'package:marc/shared/widgets/my_snackbar.dart';

/// Mesej selepas permintaan diterima.
///
/// SENGAJA neutral: backend pulang 204 sama ada akaun wujud atau tidak
/// (bukan-enumerasi, L32). Mesej yang berkata "Pautan dihantar!" akan
/// mengesahkan kewujudan akaun dan membatalkan keputusan itu di UI.
const forgotPasswordSentMessage =
    'Kalau emel itu berdaftar, kami dah hantar pautan reset. '
    'Semak peti masuk anda.';

/// 429 bermakna terlalu banyak percubaan, bukan permintaan tak sah —
/// pengguna patut cuba lagi sebentar, bukan menganggap ia gagal kekal.
bool isRetryableResetError(Object error) {
  return error is DioException && error.response?.statusCode == 429;
}

class ForgotPasswordPage extends ConsumerStatefulWidget {
  const ForgotPasswordPage({super.key});

  @override
  ConsumerState<ForgotPasswordPage> createState() => _ForgotPasswordPageState();
}

class _ForgotPasswordPageState extends ConsumerState<ForgotPasswordPage> {
  final _formKey = GlobalKey<FormState>();
  final _email = TextEditingController();
  bool _sent = false;

  @override
  void dispose() {
    _email.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    final ok = await ref
        .read(forgotPasswordControllerProvider.notifier)
        .submit(_email.text.trim());
    if (ok && mounted) setState(() => _sent = true);
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(forgotPasswordControllerProvider);
    final loading = state.isLoading;

    ref.listen(forgotPasswordControllerProvider, (prev, next) {
      if (next.hasError && !next.isLoading) {
        MySnackBar.error(context, '${next.error}');
      }
    });

    return Scaffold(
      appBar: AppBar(title: const Text('Lupa Kata Laluan')),
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.fromLTRB(28, 32, 28, 28),
          child: _sent
              ? Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(
                      Icons.mark_email_read_outlined,
                      size: 48,
                      color: Theme.of(context).colorScheme.primary,
                    ),
                    const SizedBox(height: 16),
                    Text(
                      forgotPasswordSentMessage,
                      style: Theme.of(context).textTheme.bodyLarge,
                    ),
                  ],
                )
              : Form(
                  key: _formKey,
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        'Masukkan emel akaun anda. Kami akan hantar pautan '
                        'untuk tetapkan kata laluan baharu.',
                        style: Theme.of(context).textTheme.bodyMedium,
                      ),
                      const SizedBox(height: 24),
                      AuthField(
                        controller: _email,
                        label: 'Emel',
                        keyboardType: TextInputType.emailAddress,
                        validator: validateEmail,
                      ),
                      const SizedBox(height: 28),
                      // `ButtonBusy` BUKAN butang — ia penunjuk sibuk
                      // (spinner + label) yang diletak SEBAGAI child
                      // butang. Corak ni disalin daripada
                      // `login_page.dart:82-87`.
                      FilledButton(
                        onPressed: loading ? null : _submit,
                        child: loading
                            ? const ButtonBusy(label: 'Sedang hantar…')
                            : const Text('Hantar pautan reset'),
                      ),
                    ],
                  ),
                ),
        ),
      ),
    );
  }
}
```

Tandatangan yang disahkan sebelum pelan ni ditulis:
- `AuthField({required controller, required label, icon, obscureText, keyboardType, validator})`
- `ButtonBusy({required label})` — **hanya** `label`; ia bukan butang.

- [ ] **Step 6: Wire laluan + pautan**

Dalam `lib/app/router.dart`, selepas baris `/register`:

```dart
      GoRoute(
        path: '/forgot-password',
        builder: (_, _) => const ForgotPasswordPage(),
      ),
```
(tambah import `package:marc/features/auth/forgot_password_page.dart`)

Dalam `lib/features/auth/login_page.dart`, selepas `TextButton` "Daftar":

```dart
                TextButton(
                  onPressed: () => context.push('/forgot-password'),
                  child: const Text('Lupa kata laluan?'),
                ),
```

- [ ] **Step 7: Jalankan ujian + analyze**

Run:
```bash
cd ../marc_flutter && flutter analyze && flutter test
```
Expected: `No issues found!`, semua ujian lulus.

- [ ] **Step 8: Commit (dalam marc_flutter)**

```bash
cd ../marc_flutter
git add lib/features/auth/forgot_password_page.dart \
  lib/features/auth/auth_service.dart lib/features/auth/auth_providers.dart \
  lib/features/auth/login_page.dart lib/app/router.dart \
  test/features/auth/forgot_password_test.dart
git commit -m "feat(auth): skrin lupa kata laluan (marc_go L32)"
```

---

### Task 6: Tutup dokumentasi

**Files:**
- Modify: `README.md` (jadual endpoint Auth)
- Modify: `TODO.md` (tutup L32)
- Modify: `docs/README.md` (pautkan pelan)
- Modify: `../marc_flutter/TODO.md`

- [ ] **Step 1: README.md — tambah dua baris ke jadual Auth**

```markdown
| POST | `/auth/password-reset/request` | — | sentiasa 204 (tiada enumerasi); 503 kalau `PASSWORD_RESET_URL` kosong |
| POST | `/auth/password-reset/confirm` | — | dari halaman Astro; tukar kata laluan + batal SEMUA sesi |
```

- [ ] **Step 2: TODO.md — tutup L32**

Tukar `- [ ] **L32 — tiada laluan tukar/reset kata laluan langsung (MEDIUM,` kepada `- [x]`, dan tambah di hujung item itu:

```markdown
      **RESET dibina 2026-08-23** (spec:
      `docs/superpowers/specs/2026-08-22-reset-kata-laluan-design.md`,
      pelan: `docs/superpowers/plans/2026-08-22-reset-kata-laluan.md`).
      Jadual `password_reset_tokens`, dua endpoint awam, halaman
      `marc_astro/src/pages/reset-kata-laluan.astro`, skrin
      `marc_flutter` `forgot_password_page.dart`.

      **TUKAR kata laluan semasa log masuk KEKAL TERBUKA** — ditolak
      secara eksplisit semasa brainstorm untuk memendekkan skop. Bukan
      penyekat: ahli yang syak akaun dikompromi ada
      `POST /auth/logout-all`. Buka item baharu kalau ia diperlukan.
```

- [ ] **Step 3: docs/README.md — pautkan pelan**

Tukar baris spec 2026-08-22 kepada:

```markdown
| 2026-08-22 | [Reset kata laluan — spec](./superpowers/specs/2026-08-22-reset-kata-laluan-design.md) · [plan](./superpowers/plans/2026-08-22-reset-kata-laluan.md) (L32) |
```

- [ ] **Step 4: marc_flutter/TODO.md — rekod perubahan silang-repo**

Tambah bahagian baharu berhampiran bahagian backend lain:

```markdown
## Backend L32 (2026-08-23) — reset kata laluan ✅

Skrin `forgot_password_page.dart` baharu + pautan "Lupa kata laluan?" pada
`login_page.dart`. Flutter hanya mengumpul EMEL; kata laluan baharu ditaip
pada halaman `marc_astro` (tiada app-link https dikonfigur, jadi pautan
emel membuka pelayar).

⚠️ `forgotPasswordSentMessage` MESTI kekal neutral — backend pulang 204
sama ada akaun wujud atau tidak, dan mesej yang berkata "Pautan dihantar!"
akan membocorkan apa yang backend sengaja sembunyikan. Dikunci oleh ujian.
```

- [ ] **Step 5: Pengesahan penuh setara-CI**

Run:
```bash
cd /Users/hafiz/Developments/marc_go
redis-server --port 6399 --daemonize yes --save '' 2>/dev/null; sleep 1
for db in marc_ci_handler marc_ci_reaper marc_ci_retention marc_ci_authz \
          marc_ci_sweep marc_ci_lifecycle marc_ci_reconcile; do
  dropdb $db 2>/dev/null; createdb $db; done
B="postgres://$(whoami)@localhost:5432"
go build ./... && go vet ./... && gofmt -l . && golangci-lint run
HANDLER_TEST_DB="$B/marc_ci_handler?sslmode=disable" ACTIVITY_TEST_DB="$B/marc_ci_handler?sslmode=disable" \
REAPER_TEST_DB="$B/marc_ci_reaper?sslmode=disable" RETENTION_TEST_DB="$B/marc_ci_retention?sslmode=disable" \
AUTHZ_TEST_DB="$B/marc_ci_authz?sslmode=disable" ACTIVITYSWEEP_TEST_DB="$B/marc_ci_sweep?sslmode=disable" \
LIFECYCLE_TEST_DB="$B/marc_ci_lifecycle?sslmode=disable" RECONCILE_TEST_DB="$B/marc_ci_reconcile?sslmode=disable" \
REDIS_TEST_URL="redis://localhost:6399/15" go test ./... -race -count=1
redis-cli -p 6399 shutdown nosave 2>/dev/null
for db in marc_ci_handler marc_ci_reaper marc_ci_retention marc_ci_authz \
          marc_ci_sweep marc_ci_lifecycle marc_ci_reconcile marc_l32; do
  dropdb $db 2>/dev/null; done
```
Expected: `0 issues`, semua pakej `ok`.

- [ ] **Step 6: Commit**

```bash
cd /Users/hafiz/Developments/marc_go
git add README.md TODO.md docs/README.md
git commit -m "docs: tutup L32 (reset kata laluan)"
cd ../marc_flutter && git add TODO.md
git commit -m "docs: rekod reset kata laluan silang-repo (marc_go L32)"
```
