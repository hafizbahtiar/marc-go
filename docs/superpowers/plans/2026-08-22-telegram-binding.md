# Integrasi Telegram Fasa 1 (binding akaun) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Benarkan ahli MARC mengikat (bind) akaun Telegram mereka ke
akaun MARC melalui deep-link sekali-guna, supaya fasa seterusnya
(notifikasi, 2FA) ada asas identiti Telegram untuk dibina.

**Architecture:** `marc_go` terima update Telegram via **webhook**
(bukan long polling — Telegram `409 Conflict` pada >1 replica
`getUpdates` serentak, dan `marc_go` tiada kunci teragih). Endpoint
auth baharu (`POST /me/telegram-link/token`, `DELETE
/me/telegram-link`) jana/padam token sementara guna corak SAMA persis
`password_reset_tokens`. Handler `/start` webhook memproses token dan
menulis binding kekal ke lajur baharu pada `profiles`.

**Tech Stack:** Go 1.26, `github.com/go-telegram/bot` (sifar
dependency, `net/http`), Gin, sqlc/pgx, Flutter/Riverpod/`url_launcher`.

**Spec:** `docs/superpowers/specs/2026-08-22-telegram-binding-design.md`

## Global Constraints

- Library Telegram: `github.com/go-telegram/bot` — TIADA library lain
- Cara terima update: **webhook**, bukan long polling
- Storan binding: lajur pada `profiles` (BUKAN `users` — dibetulkan
  drpd draf awal spec, lihat spec bahagian "Pembetulan")
- Binding 1:1 ketat: `telegram_chat_id unique` di lapisan DB
- Pertindihan (chat lain dah terikat akaun lain) → TOLAK
- User sedia ada binding, bind chat baharu → GANTIKAN (chat lama
  senyap terputus, tiada notifikasi — mekanisme notifikasi belum wujud)
- Token binding: legap 32-bait (`auth.GenerateOpaqueToken`), hash
  SHA-256 (`auth.HashToken`), TTL **10 minit**, tuntutan atomik
  `DELETE...RETURNING` sbg statement PERTAMA
- Config kosong (`TELEGRAM_BOT_TOKEN`) = ciri MATI (503), bukan
  degradasi senyap — padanan corak `PASSWORD_RESET_URL`/R2/Stripe
- Webhook `/webhooks/telegram` MESTI sentiasa pulang 200 selepas
  pengesahan header rahsia lulus — ralat "kpd pengguna" dihantar sbg
  mesej bot, BUKAN status HTTP
- Pautan Play Store:
  `https://play.google.com/store/apps/details?id=com.hafizbahtiar.marc`
- Ujian mutasi wajib pada: tuntutan atomik token, semakan pertindihan,
  pengesahan header rahsia webhook

---

## Peta permukaan (rujukan pantas)

| Fail | Tindakan |
|---|---|
| `internal/db/migrations/20260823100000_add_telegram_binding.sql` | Cipta |
| `queries/telegram_link_tokens.sql` | Cipta |
| `queries/profiles.sql` | Ubah — tambah 3 query |
| `internal/config/config.go` | Ubah — 3 field baharu |
| `.env.example` | Ubah — 3 baris baharu |
| `go.mod` / `go.sum` | Ubah — tambah `github.com/go-telegram/bot` |
| `internal/http/handlers/telegram.go` | Cipta |
| `internal/http/handlers/telegram_live_test.go` | Cipta |
| `internal/http/handlers/profile.go` | Ubah — `profileResponse` + `Me` |
| `internal/http/router.go` | Ubah — param baharu + 3 route |
| `internal/http/router_telegram_test.go` | Cipta |
| `cmd/api/main.go` | Ubah — bina `telegramHandler` + `tgBot` |
| `marc_flutter/lib/features/profile/telegram_link_page.dart` | Cipta |
| `marc_flutter/lib/features/profile/profile_providers.dart` | Ubah — model `Profile` |
| `marc_flutter/lib/features/profile/profile_page.dart` | Ubah — 1 `ListTile` |
| `marc_flutter/lib/features/auth/auth_service.dart` | Ubah — 3 kaedah |
| `marc_flutter/lib/app/router.dart` | Ubah — 1 route |
| `marc_flutter/test/.../telegram_link_page_test.dart` | Cipta |

---

## Task 1: Skema, query, config

**Files:**
- Create: `internal/db/migrations/20260823100000_add_telegram_binding.sql`
- Create: `queries/telegram_link_tokens.sql`
- Modify: `queries/profiles.sql` (tambah di hujung fail)
- Modify: `internal/config/config.go`
- Modify: `.env.example`
- Modify: `go.mod` (tambah dependency)

**Interfaces:**
- Produces: sqlc akan jana `sqlc.TelegramLinkToken`,
  `sqlc.CreateTelegramLinkToken(ctx, CreateTelegramLinkTokenParams)`,
  `sqlc.ConsumeTelegramLinkToken(ctx, tokenHash string)
  (TelegramLinkToken, error)`,
  `sqlc.DeleteTelegramLinkTokensByUser(ctx, userID uuid.UUID) error`,
  `sqlc.GetUserIDByTelegramChatID(ctx, pgtype.Int8) (uuid.UUID, error)`,
  `sqlc.SetTelegramLink(ctx, SetTelegramLinkParams) error`,
  `sqlc.ClearTelegramLink(ctx, userID uuid.UUID) error`. Task 2 & 3
  guna kesemuanya.
- Produces: `Profile.TelegramChatID pgtype.Int8`,
  `Profile.TelegramUsername pgtype.Text`,
  `Profile.TelegramLinkedAt pgtype.Timestamptz` — muncul automatik
  dlm `GetProfileByUserID` (`select p.*`). Task 2 guna dlm respons `/me`.
- Produces: `config.Config.TelegramBotToken`,
  `.TelegramBotUsername`, `.TelegramWebhookSecret` (semua `string`).
  Task 2 & 3 baca.

- [ ] **Step 1: Tulis migration**

```sql
-- +goose Up

-- Integrasi Telegram Fasa 1 (binding akaun). Lajur pada `profiles`
-- (BUKAN `users`) -- `users` cuma pegang kelayakan (id/email/
-- password_hash); atribut akaun spt `email_verified`/`avatar_r2_key`
-- sedia ada duduk pada `profiles`. Binding ni keadaan kekal-tunggal,
-- padanan profil yg sama.
alter table profiles
  add column telegram_chat_id bigint unique,
  add column telegram_username text,
  add column telegram_linked_at timestamptz;

-- Token deep-link sementara, sekali-guna -- cerminan
-- password_reset_tokens, TTL lebih pendek (10 minit, bukan 1 jam)
-- sebab aliran ni app->Telegram serta-merta, bukan tunggu emel.
create table telegram_link_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index telegram_link_tokens_user_id_idx on telegram_link_tokens(user_id);

-- +goose Down
drop table if exists telegram_link_tokens;
alter table profiles
  drop column if exists telegram_chat_id,
  drop column if exists telegram_username,
  drop column if exists telegram_linked_at;
```

- [ ] **Step 2: Tulis `queries/telegram_link_tokens.sql`**

```sql
-- name: CreateTelegramLinkToken :one
insert into telegram_link_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: ConsumeTelegramLinkToken :one
-- Tuntut token secara ATOMIK: satu pernyataan, `delete ... returning`.
-- Padanan tepat ConsumePasswordResetToken (queries/password_reset_tokens.sql)
-- dan atas sebab yang SAMA -- baca-dahulu-kemudian-tulis ada jurang
-- TOCTOU yang membenarkan dua permintaan serentak kedua-duanya lulus.
delete from telegram_link_tokens
where token_hash = $1
returning *;

-- name: DeleteTelegramLinkTokensByUser :exec
delete from telegram_link_tokens where user_id = $1;
```

- [ ] **Step 3: Tambah tiga query ke `queries/profiles.sql`**

Tambah di HUJUNG fail sedia ada (jangan ubah query lain):

```sql

-- name: GetUserIDByTelegramChatID :one
select user_id from profiles where telegram_chat_id = $1;

-- name: SetTelegramLink :exec
update profiles
set telegram_chat_id = $2, telegram_username = $3, telegram_linked_at = now()
where user_id = $1;

-- name: ClearTelegramLink :exec
update profiles
set telegram_chat_id = null, telegram_username = null, telegram_linked_at = null
where user_id = $1;
```

- [ ] **Step 4: Jana kod sqlc**

Run: `sqlc generate` (dari root `marc_go`)
Expected: `internal/db/sqlc/telegram_link_tokens.sql.go` dicipta;
`internal/db/sqlc/profiles.sql.go` dan `querier.go` diubah; tiada ralat.

- [ ] **Step 5: Tambah dependency `go-telegram/bot`**

Run: `go get github.com/go-telegram/bot@latest`
Expected: `go.mod`/`go.sum` diubah, satu baris `require` baharu.

- [ ] **Step 6: Tambah 3 field ke `config.Config`**

Dalam `internal/config/config.go`, tambah selepas field
`PasswordResetURL` (baris ~33):

```go
	// TelegramBotToken/BotUsername/WebhookSecret -- Integrasi Telegram
	// Fasa 1 (binding akaun). Kosong = ciri MATI sepenuhnya (503 pd
	// endpoint token, route webhook tak didaftar) -- padanan corak
	// PasswordResetURL/R2/Stripe, BUKAN degradasi senyap.
	TelegramBotToken     string
	TelegramBotUsername  string
	TelegramWebhookSecret string
```

Dalam `Load()`, tambah selepas baris `PasswordResetURL:
os.Getenv("PASSWORD_RESET_URL"),`:

```go
		TelegramBotToken:      os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramBotUsername:   os.Getenv("TELEGRAM_BOT_USERNAME"),
		TelegramWebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
```

- [ ] **Step 7: Tambah 3 baris ke `.env.example`**

Selepas baris `PASSWORD_RESET_URL=`:

```
# Optional -- Integrasi Telegram Fasa 1 (binding akaun). Kosong =
# ciri dimatikan sepenuhnya (503), bukan fallback.
TELEGRAM_BOT_TOKEN=
# Username bot TANPA @, cth "MarcKelabBot" -- dipakai bina deep-link.
TELEGRAM_BOT_USERNAME=
# Rahsia dikongsi, dihantar semasa setWebhook, disahkan setiap
# panggilan webhook via header X-Telegram-Bot-Api-Secret-Token
# (library go-telegram/bot sahkan ni secara automatik).
TELEGRAM_WEBHOOK_SECRET=
```

- [ ] **Step 8: Sahkan build**

Run: `go build ./...`
Expected: bersih, tiada ralat.

- [ ] **Step 9: Commit**

```bash
git add internal/db/migrations/20260823100000_add_telegram_binding.sql \
  queries/telegram_link_tokens.sql queries/profiles.sql \
  internal/db/sqlc/ internal/config/config.go .env.example go.mod go.sum
git commit -m "feat(telegram): skema + query + config binding akaun (fasa 1)"
```

---

## Task 2: Endpoint token & nyahikat

**Files:**
- Create: `internal/http/handlers/telegram.go`
- Create: `internal/http/handlers/telegram_live_test.go`
- Modify: `internal/http/handlers/profile.go`

**Interfaces:**
- Consumes: semua query dari Task 1 (`CreateTelegramLinkToken`,
  `DeleteTelegramLinkTokensByUser`, `ClearTelegramLink`); `Profile`
  struct field baharu; `auth.GenerateOpaqueToken()`, `auth.HashToken()`
  (`internal/auth/token.go`, sedia ada, tandatangan sama persis
  corak reset kata laluan)
- Produces: `handlers.NewTelegramHandler(pool *pgxpool.Pool,
  botUsername string) *TelegramHandler`,
  `(*TelegramHandler).RequestLinkToken(c *gin.Context)`,
  `(*TelegramHandler).DeleteLink(c *gin.Context)`. Task 3 tambah
  `HandleUpdate` pada struct SAMA; Task 4/router guna kedua-dua kaedah.
  503 gate: `botUsername == ""`.

- [ ] **Step 1: Tulis ujian gagal `telegram_live_test.go`**

```go
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
)

func telegramHandler(pool *pgxpool.Pool) *TelegramHandler {
	return NewTelegramHandler(pool, "MarcKelabBot")
}

func telegramTokenRequestCall(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/me/telegram-link/token", nil)
	c.Set("user_id", userID)

	telegramHandler(pool).RequestLinkToken(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func countTelegramLinkTokens(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from telegram_link_tokens where user_id = $1`,
		userID).Scan(&n); err != nil {
		t.Fatalf("kira token: %v", err)
	}
	return n
}

func TestRequestTelegramLinkTokenMenciptaToken(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	rec := telegramTokenRequestCall(t, pool, userID)

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}
	if got := countTelegramLinkTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d, mahu 1", got)
	}
	var body struct {
		DeepLink string `json:"deep_link"`
	}
	if err := decodeJSON(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !contains(body.DeepLink, "https://t.me/MarcKelabBot?start=") {
		t.Fatalf("deep_link tak sepadan corak: %s", body.DeepLink)
	}
}

// Permintaan kedua mesti membunuh token pertama -- padanan invarian
// reset kata laluan.
func TestRequestTelegramLinkTokenKeduaMembatalkanYangPertama(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	telegramTokenRequestCall(t, pool, userID)
	telegramTokenRequestCall(t, pool, userID)

	if got := countTelegramLinkTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d selepas dua permintaan, mahu 1", got)
	}
}

func TestRequestTelegramLinkTokenTanpaBotUsernamePulang503(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/me/telegram-link/token", nil)
	c.Set("user_id", userID)

	NewTelegramHandler(pool, "").RequestLinkToken(c)
	c.Writer.WriteHeaderNow()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("kod = %d, mahu 503", rec.Code)
	}
	if got := countTelegramLinkTokens(t, pool, userID); got != 0 {
		t.Errorf("token = %d ditulis walaupun ciri dimatikan", got)
	}
}

func TestDeleteTelegramLinkIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	for i := 0; i < 2; i++ {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodDelete, "/me/telegram-link", nil)
		c.Set("user_id", userID)

		telegramHandler(pool).DeleteLink(c)
		c.Writer.WriteHeaderNow()

		if rec.Code != http.StatusNoContent {
			t.Fatalf("panggilan %d: kod = %d, mahu 204", i+1, rec.Code)
		}
	}
}

func TestDeleteTelegramLinkKosongkanLajur(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	// Seed binding SEBENAR (TelegramChatID.Valid: true) -- kalau cuma
	// UserID diberi, TelegramChatID kekal zero-value (Valid: false) dan
	// ujian ni lulus walau DeleteLink tak buat apa-apa, sebab tiada apa
	// utk dikosongkan pun.
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           userID,
		TelegramChatID:   pgtype.Int8{Int64: 8008, Valid: true},
		TelegramUsername: pgtype.Text{String: "sblm_nyahikat", Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID); err != nil || !row.TelegramChatID.Valid {
		t.Fatalf("seed gagal -- binding tak tertulis (err=%v, valid=%v)", err, row.TelegramChatID.Valid)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/me/telegram-link", nil)
	c.Set("user_id", userID)
	telegramHandler(pool).DeleteLink(c)
	c.Writer.WriteHeaderNow()

	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Error("telegram_chat_id masih terisi selepas nyahikat")
	}
}
```

`decodeJSON`/`contains` — helper kecil, tambah di hujung fail ujian ni
(bukan dlm fail production):

```go
func decodeJSON(b []byte, v interface{}) error {
	return json.Unmarshal(b, v)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
```

Tambah `"encoding/json"` dan `"strings"` ke import block.

`c.Set("user_id", userID)` guna kunci yg SAMA dgn
`middleware.RequireAuth` set (sahkan di Step 2 tandatangan
`middleware.UserID` -- kalau kunci konteks berbeza drpd `"user_id"`,
laraskan baris ni supaya padan, JANGAN ubah middleware).

- [ ] **Step 2: Jalankan ujian, sahkan GAGAL (fungsi belum wujud)**

Run: `HANDLER_TEST_DB=<dsn> go test ./internal/http/handlers/ -run TestRequestTelegramLinkToken -v`
Expected: FAIL — `undefined: NewTelegramHandler`

- [ ] **Step 3: Tulis `internal/http/handlers/telegram.go`**

```go
package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// telegramLinkTTL -- 10 minit, bukan 1 jam spt reset kata laluan.
// Aliran ni app->Telegram serta-merta (deep-link dibuka sebaik
// ditekan), bukan tunggu emel dibaca.
const telegramLinkTTL = 10 * time.Minute

type TelegramHandler struct {
	pool        *pgxpool.Pool
	queries     *sqlc.Queries
	botUsername string
}

// NewTelegramHandler -- botUsername kosong bermakna ciri binding
// Telegram DIMATIKAN sepenuhnya (503), padanan corak PasswordResetURL.
func NewTelegramHandler(pool *pgxpool.Pool, botUsername string) *TelegramHandler {
	return &TelegramHandler{
		pool:        pool,
		queries:     sqlc.New(pool),
		botUsername: botUsername,
	}
}

// RequestLinkToken -- POST /me/telegram-link/token. Auth.
func (h *TelegramHandler) RequestLinkToken(c *gin.Context) {
	if h.botUsername == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "binding Telegram belum tersedia",
		})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	// Permintaan baharu membunuh token lama -- padanan
	// RequestPasswordReset.
	if err := h.queries.DeleteTelegramLinkTokensByUser(ctx, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}

	token, err := auth.GenerateOpaqueToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}
	if _, err := h.queries.CreateTelegramLinkToken(ctx, sqlc.CreateTelegramLinkTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(telegramLinkTTL), Valid: true},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deep_link": fmt.Sprintf("https://t.me/%s?start=%s", h.botUsername, token),
	})
}

// DeleteLink -- DELETE /me/telegram-link. Auth. Idempoten: 204 sentiasa,
// tak kira sebelum ni terikat atau tidak.
func (h *TelegramHandler) DeleteLink(c *gin.Context) {
	userID := middleware.UserID(c)
	if err := h.queries.ClearTelegramLink(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal nyahikat"})
		return
	}
	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 4: Jalankan ujian, sahkan LULUS**

Run: `HANDLER_TEST_DB=<dsn> go test ./internal/http/handlers/ -run 'TestRequestTelegramLinkToken|TestDeleteTelegramLink' -v`
Expected: semua PASS

- [ ] **Step 5: Tambah medan Telegram ke respons `/me`**

Dalam `internal/http/handlers/profile.go`, tambah ke struct
`profileResponse` (selepas `RegistrationPaymentStatus`):

```go
	TelegramLinked   bool    `json:"telegram_linked"`
	TelegramUsername *string `json:"telegram_username"`
```

Dalam `Me()`, tambah ke pembinaan `profileResponse{...}` (selepas
`RegistrationPaymentStatus: paymentStatus,`):

```go
		TelegramLinked:            row.TelegramChatID.Valid,
		TelegramUsername:          textToPtr(row.TelegramUsername),
```

- [ ] **Step 6: Ujian `/me` papar keadaan binding**

Tambah ke `internal/http/handlers/telegram_live_test.go`:

```go
func TestMeResponseMemaparkanKeadaanTelegram(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil sblm bind: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Fatal("telegram_chat_id patut kosong sblm binding")
	}

	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           userID,
		TelegramChatID:   pgtype.Int8{Int64: 12345, Valid: true},
		TelegramUsername: pgtype.Text{String: "ahliuji", Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	row, err = sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil selepas bind: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != 12345 {
		t.Fatalf("telegram_chat_id = %+v, mahu 12345", row.TelegramChatID)
	}
	if !row.TelegramUsername.Valid || row.TelegramUsername.String != "ahliuji" {
		t.Fatalf("telegram_username = %+v, mahu ahliuji", row.TelegramUsername)
	}
}
```

(`pgtype` dah diimport sejak Task 2 Step 1 — tiada import tambahan
diperlukan di sini.)

Run: `HANDLER_TEST_DB=<dsn> go test ./internal/http/handlers/ -run TestMeResponseMemaparkanKeadaanTelegram -v`
Expected: PASS

- [ ] **Step 7: Sahkan build + format**

Run: `go build ./... && gofmt -l .`
Expected: build bersih, `gofmt -l` tiada output

- [ ] **Step 8: Commit**

```bash
git add internal/http/handlers/telegram.go \
  internal/http/handlers/telegram_live_test.go \
  internal/http/handlers/profile.go
git commit -m "feat(telegram): endpoint token binding + nyahikat + status /me"
```

---

## Task 3: Webhook `/start` + wiring

**Files:**
- Modify: `internal/http/handlers/telegram.go`
- Modify: `internal/http/handlers/telegram_live_test.go`
- Modify: `internal/http/router.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Consumes: `TelegramHandler` dari Task 2 (struct + kaedah sedia ada);
  `sqlc.ConsumeTelegramLinkToken`, `sqlc.GetUserIDByTelegramChatID`,
  `sqlc.SetTelegramLink` dari Task 1
- Produces: `(*TelegramHandler).HandleUpdate(ctx context.Context, b
  *bot.Bot, update *models.Update)` — tandatangan tepat
  `bot.HandlerFunc`, dihantar terus ke `bot.WithDefaultHandler()` dlm
  `main.go`
- Produces: `NewRouter(..., telegramHandler *handlers.TelegramHandler,
  tgBot *bot.Bot) *gin.Engine` — 2 param baharu di HUJUNG senarai
  param sedia ada

**Nota reka bentuk penting:** `telegramHandler` dibina dlm `main.go`
(BUKAN di dalam `NewRouter` spt `authHandler`/`profileHandler`) sebab
`bot.New()` perlu `bot.WithDefaultHandler(telegramHandler.HandleUpdate)`
sbg *option* semasa dibina — kalau `telegramHandler` dibina di dalam
`NewRouter`, ia belum wujud lagi pada ketika `bot.New()` dipanggil.
Ini padanan corak `paymentReconciler`: dibina + `.Start(ctx)` dlm
`main.go`, dihantar SUDAH-DIBINA ke `NewRouter`.

- [ ] **Step 1: Tulis ujian gagal utk `resolveStart`**

Tambah ke `internal/http/handlers/telegram_live_test.go`:

```go
func seedTelegramLinkToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ttl time.Duration) string {
	t.Helper()
	raw, err := auth.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into telegram_link_tokens (user_id, token_hash, expires_at)
		 values ($1, $2, now() + $3::interval)`,
		userID, auth.HashToken(raw), ttl.String()); err != nil {
		t.Fatalf("sisip token: %v", err)
	}
	return raw
}

func TestResolveStartTanpaTokenAhliBaharuBalasGreeting(t *testing.T) {
	pool := activityTestPool(t)
	reply := telegramHandler(pool).resolveStart(context.Background(), 999, "sesiapa", "")
	if !contains(reply, "play.google.com") {
		t.Fatalf("balasan tak ada pautan Play Store: %s", reply)
	}
}

func TestResolveStartTokenLuputDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, -time.Minute)

	reply := telegramHandler(pool).resolveStart(ctx, 1001, "u", token)

	if !contains(reply, "tidak sah") && !contains(reply, "luput") {
		t.Fatalf("balasan tak sebut token tak sah/luput: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Error("binding tertulis walaupun token luput")
	}
}

func TestResolveStartBerjayaMenulisBinding(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)

	reply := telegramHandler(pool).resolveStart(ctx, 2002, "ujian_user", token)

	if contains(reply, "tidak sah") || contains(reply, "Ralat") {
		t.Fatalf("balasan tak dijangka: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != 2002 {
		t.Fatalf("telegram_chat_id = %+v, mahu 2002", row.TelegramChatID)
	}
	if !row.TelegramUsername.Valid || row.TelegramUsername.String != "ujian_user" {
		t.Fatalf("telegram_username = %+v, mahu ujian_user", row.TelegramUsername)
	}
}

// Ujian perlumbaan -- padanan TestConfirmResetSekaliGunaDiBawahPerlumbaan.
// Buang tuntutan atomik (Consume...) MESTI buat ujian ni gagal.
func TestResolveStartSekaliGunaDiBawahPerlumbaan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	berjaya := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			reply := telegramHandler(pool).resolveStart(ctx, chatID, "u", token)
			if !contains(reply, "tidak sah") {
				mu.Lock()
				berjaya++
				mu.Unlock()
			}
		}(int64(3000 + i))
	}
	wg.Wait()

	if berjaya != 1 {
		t.Fatalf("%d permintaan serentak berjaya, mahu TEPAT 1", berjaya)
	}
}

func TestResolveStartChatTerikatAkaunLainDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userA := seedMember(t, ctx, pool, "ahli", "approved")
	userB := seedMember(t, ctx, pool, "ahli", "approved")

	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userA,
		TelegramChatID: pgtype.Int8{Int64: 4004, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding userA: %v", err)
	}

	token := seedTelegramLinkToken(t, ctx, pool, userB, time.Hour)
	reply := telegramHandler(pool).resolveStart(ctx, 4004, "userB_tg", token)

	if !contains(reply, "disambungkan ke akaun MARC lain") {
		t.Fatalf("balasan tak sebut pertindihan: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userA)
	if err != nil {
		t.Fatalf("baca profil userA: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != 4004 {
		t.Fatal("binding userA berubah -- sepatutnya tak tersentuh")
	}
}

func TestResolveStartUserSediaBindingGantiChatBaharu(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userID,
		TelegramChatID: pgtype.Int8{Int64: 5005, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding lama: %v", err)
	}

	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)
	reply := telegramHandler(pool).resolveStart(ctx, 6006, "chat_baharu", token)

	if contains(reply, "tidak sah") || contains(reply, "Ralat") {
		t.Fatalf("balasan tak dijangka: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Int64 != 6006 {
		t.Fatalf("telegram_chat_id = %d, mahu 6006 (chat baharu)", row.TelegramChatID.Int64)
	}
}

func TestResolveStartChatSediaTerikatBalasSudahDisambung(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userID,
		TelegramChatID: pgtype.Int8{Int64: 7007, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	reply := telegramHandler(pool).resolveStart(ctx, 7007, "u", "")

	if !contains(reply, "dah disambung") {
		t.Fatalf("balasan tak sebut 'dah disambung': %s", reply)
	}
}
```

Tambah import `"sync"`, `"time"` (kalau belum ada), dan
`"marc/internal/auth"` ke `telegram_live_test.go`.

- [ ] **Step 2: Jalankan ujian, sahkan GAGAL**

Run: `HANDLER_TEST_DB=<dsn> go test ./internal/http/handlers/ -run TestResolveStart -v`
Expected: FAIL — `undefined: (*TelegramHandler).resolveStart`

- [ ] **Step 3: Tulis `resolveStart` + `HandleUpdate` dlm `telegram.go`**

Tambah import `"context"`, `"log"`, `"time"` (sedia ada),
`"github.com/go-telegram/bot"`, `"github.com/go-telegram/bot/models"`
ke `telegram.go`. Tambah kod ni di hujung fail:

```go
// resolveStart -- logik TULEN /start, tanpa kebergantungan rangkaian
// Telegram. Diuji terus (lihat telegram_live_test.go) tanpa perlu
// mock HTTP -- HandleUpdate di bawah cuma wrapper nipis yang hantar
// rentetan pulangan ni sbg mesej bot.
func (h *TelegramHandler) resolveStart(ctx context.Context, chatID int64, username, token string) string {
	const (
		msgGreeting = "Selamat datang ke bot MARC! Muat turun app di " +
			"https://play.google.com/store/apps/details?id=com.hafizbahtiar.marc " +
			"untuk sambungkan akaun anda."
		msgTokenTidakSah = "Pautan tidak sah atau sudah luput. Cuba jana pautan baharu dari app."
		msgPertindihan   = "Akaun Telegram ini sudah disambungkan ke akaun MARC lain. Guna akaun Telegram yang berbeza."
		msgSudahDisambung = "Akaun kamu dah disambungkan ke MARC."
		msgBerjaya       = "Akaun MARC anda berjaya disambungkan!"
		msgRalat         = "Ralat dalaman. Cuba lagi dari app."
	)

	chatIDParam := pgtype.Int8{Int64: chatID, Valid: true}

	if token == "" {
		// Chat ni dah bind akaun (mana-mana) -- balas status, jangan
		// greeting. Merangkumi baris jadual spec "/start (chat dah
		// bind akaun ni)" -- disemak tanpa perlu token sebab tujuan
		// mesej ni cuma maklum balas status, bukan tindakan.
		if _, err := h.queries.GetUserIDByTelegramChatID(ctx, chatIDParam); err == nil {
			return msgSudahDisambung
		}
		return msgGreeting
	}

	rec, err := h.queries.ConsumeTelegramLinkToken(ctx, auth.HashToken(token))
	if err != nil {
		return msgTokenTidakSah
	}
	if rec.ExpiresAt.Time.Before(time.Now()) {
		return msgTokenTidakSah
	}

	if existingUserID, err := h.queries.GetUserIDByTelegramChatID(ctx, chatIDParam); err == nil && existingUserID != rec.UserID {
		return msgPertindihan
	}

	if err := h.queries.SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           rec.UserID,
		TelegramChatID:   chatIDParam,
		TelegramUsername: pgtype.Text{String: username, Valid: username != ""},
	}); err != nil {
		log.Printf("telegram: simpan binding gagal (user=%s): %v", rec.UserID, err)
		return msgRalat
	}

	return msgBerjaya
}

// HandleUpdate -- tandatangan tepat bot.HandlerFunc, dihantar terus
// ke bot.WithDefaultHandler() (cmd/api/main.go). Webhook Telegram
// MESTI sentiasa terima 200 -- ralat "kpd pengguna" dihantar sbg
// mesej bot (lihat resolveStart), BUKAN status HTTP. bot.WebhookHandler()
// sendiri yg uruskan pengesahan header X-Telegram-Bot-Api-Secret-Token
// (bot.WithWebhookSecretToken semasa bot.New()) SEBELUM handler ni
// dipanggil.
func (h *TelegramHandler) HandleUpdate(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	if !strings.HasPrefix(text, "/start") {
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(text, "/start"))

	username := ""
	if update.Message.From != nil {
		username = update.Message.From.Username
	}

	reply := h.resolveStart(ctx, update.Message.Chat.ID, username, token)
	if reply == "" {
		return
	}
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   reply,
	}); err != nil {
		log.Printf("telegram: hantar balasan gagal (chat=%d): %v", update.Message.Chat.ID, err)
	}
}
```

Tambah `"strings"` ke import block `telegram.go`.

- [ ] **Step 4: Jalankan ujian, sahkan LULUS**

Run: `HANDLER_TEST_DB=<dsn> go test ./internal/http/handlers/ -run TestResolveStart -v`
Expected: semua PASS

- [ ] **Step 5: Ujian mutasi -- buang tuntutan atomik**

Tukar SEMENTARA `ConsumeTelegramLinkToken` (dlm
`queries/telegram_link_tokens.sql`) drpd `delete ... returning`
kepada `select ...` biasa, `sqlc generate`, jalankan
`TestResolveStartSekaliGunaDiBawahPerlumbaan` — SAHKAN ia GAGAL.
Kemudian PULIHKAN query asal + `sqlc generate` semula.

Run: `git diff queries/telegram_link_tokens.sql` selepas pulih
Expected: kosong (tiada perubahan kekal)

- [ ] **Step 6: Ujian mutasi -- buang semakan pertindihan**

Dalam `resolveStart`, komen SEMENTARA blok
`if existingUserID, err := ...; err == nil && existingUserID != rec.UserID { return msgPertindihan }`,
jalankan `TestResolveStartChatTerikatAkaunLainDitolak` — SAHKAN GAGAL.
PULIHKAN kod.

Run: `git diff internal/http/handlers/telegram.go` selepas pulih
Expected: kosong

- [ ] **Step 7: Wiring `router.go`**

Tambah import `"github.com/go-telegram/bot"` ke `router.go`.

Tambah 2 param BAHARU di HUJUNG senarai param `NewRouter` (selepas
`passwordResetURL string,`):

```go
	telegramHandler *handlers.TelegramHandler,
	tgBot *bot.Bot,
```

Dalam badan `NewRouter`, selepas blok route `protected` sedia ada
(selepas baris `protected.POST("/auth/logout-all",
authHandler.LogoutAll)`), tambah:

```go
	// Baldi berasingan drpd 'auth'/'password-reset' (pengajaran L26):
	// trafik binding Telegram tak patut kongsi kuota dgn laluan lain.
	telegramLinkRateLimiter := rateLimiter.Limit("telegram-link", authRateLimit, authRateBurst)
	protected.POST("/me/telegram-link/token", telegramLinkRateLimiter, telegramHandler.RequestLinkToken)
	protected.DELETE("/me/telegram-link", telegramHandler.DeleteLink)

	// Webhook Telegram -- AWAM, dipanggil Telegram (bukan app). tgBot
	// nil bila TELEGRAM_BOT_TOKEN kosong ATAU bot.New() gagal (token
	// tak sah) -- route tak didaftar langsung, bukan 503 runtime,
	// padanan corak lain dlm sistem ni (route hilang, bukan handler
	// yg semak config setiap panggilan).
	if tgBot != nil {
		r.POST("/webhooks/telegram", gin.WrapF(tgBot.WebhookHandler()))
	}
```

- [ ] **Step 8: Wiring `cmd/api/main.go`**

Tambah import `"github.com/go-telegram/bot"` dan
`"github.com/go-telegram/bot/models"` ke `main.go`.

Selepas blok `paymentReconciler.Start(ctx)` (sebelum baris
`router := httpapi.NewRouter(...)`), tambah:

```go
	// Integrasi Telegram Fasa 1 (binding akaun). telegramHandler
	// dibina DULU (tak bergantung rangkaian) sebab bot.New() perlukan
	// kaedahnya sbg default handler -- lihat nota dlm plan Task 3.
	telegramHandler := handlers.NewTelegramHandler(pool, cfg.TelegramBotUsername)

	var tgBot *bot.Bot
	if cfg.TelegramBotToken != "" {
		b, err := bot.New(cfg.TelegramBotToken,
			bot.WithWebhookSecretToken(cfg.TelegramWebhookSecret),
			bot.WithDefaultHandler(telegramHandler.HandleUpdate),
		)
		if err != nil {
			// TIDAK fatal -- padanan corak Redis Ping: config salah tak
			// patut halang app boot, cuma matikan ciri ni.
			log.Printf("AMARAN telegram: bot tak dapat dimulakan (%v) -- ciri binding Telegram tak aktif", err)
		} else {
			if _, err := b.SetWebhook(ctx, &bot.SetWebhookParams{
				URL:         cfg.PublicBaseURL + "/webhooks/telegram",
				SecretToken: cfg.TelegramWebhookSecret,
			}); err != nil {
				log.Printf("AMARAN telegram: setWebhook gagal: %v", err)
			}
			go b.StartWebhook(ctx)
			tgBot = b
			log.Printf("telegram: bot aktif, webhook didaftar")
		}
	}
```

Tambah import `"marc/internal/http/handlers"` kalau belum ada
(semak dulu — kemungkinan besar sudah ada via `httpapi` alias;
kalau `handlers` package belum diimport terus dlm `main.go`,
tambah `"marc/internal/http/handlers"`).

Ubah baris panggilan `NewRouter` (tambah 2 argumen di HUJUNG):

```go
	router := httpapi.NewRouter(pool, jwtSvc, cfg.RefreshTokenTTL, emailClient, cfg.PublicBaseURL, cfg.EmailVerifyURL, logger, r2Client, pushSvc, paymentGateways, cfg.RegistrationFeeCents, redisCli, paymentReconciler, cfg.CORSAllowedOrigins, cfg.RegistrationPaymentReturnURL, cfg.ActivityPaymentReturnURL, cfg.CertificateVerifyURL, cfg.PasswordResetURL, telegramHandler, tgBot)
```

`models` import dipakai secara IMPLISIT via tandatangan
`telegramHandler.HandleUpdate` (Go perlukan import package walaupun
tak sebut nama `models.` terus dlm `main.go` -- kalau compiler
sebut "imported and not used", buang import `models` drpd `main.go`;
ia hanya perlu dlm `telegram.go`).

- [ ] **Step 9: Sahkan build penuh**

Run: `go build ./... && go vet ./...`
Expected: bersih

- [ ] **Step 10: Ujian wiring webhook (header rahsia + kontrak 200)**

Tambah fail BAHARU `internal/http/router_telegram_test.go`. Kedua-dua
ujian guna `WithSkipGetMe()` supaya `bot.New()` tak buat panggilan
rangkaian sebenar ke Telegram semasa ujian, dan token berformat sah
scr sintaks (`"123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"`, format
rasmi Telegram) — tak perlu wujud sebenar sebab `WithSkipGetMe()`
langkau pengesahan rangkaian:

```go
package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/gin-gonic/gin"
)

const telegramTestToken = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

func telegramTestBot(t *testing.T) *tgbot.Bot {
	t.Helper()
	b, err := tgbot.New(telegramTestToken,
		tgbot.WithSkipGetMe(),
		tgbot.WithWebhookSecretToken("rahsia-ujian"),
		tgbot.WithDefaultHandler(func(ctx context.Context, b *tgbot.Bot, update *models.Update) {}),
	)
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	return b
}

// Nilai fokus: WebhookHandler() TOLAK request tanpa header rahsia
// betul, walau tanpa sebarang state DB.
func TestWebhookTelegramTolakTanpaHeaderRahsiaBetul(t *testing.T) {
	b := telegramTestBot(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/webhooks/telegram", gin.WrapF(b.WebhookHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", nil)
	// TIADA header X-Telegram-Bot-Api-Secret-Token disertakan.
	r.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("webhook terima request tanpa header rahsia yang betul")
	}
}

// Nilai fokus: header rahsia BETUL tapi update tanpa Message (cth
// service message jenis lain Telegram hantar -- poll, my_chat_member,
// dll) MESTI pulang 200, bukan 500 -- ini invarian
// TelegramHandler.HandleUpdate (gard `update.Message == nil`, Task 3
// Step 3), BUKAN kelakuan library go-telegram/bot yg diuji di sini.
func TestWebhookTelegramUpdateTanpaMessagePulang200(t *testing.T) {
	b := telegramTestBot(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/webhooks/telegram", gin.WrapF(b.WebhookHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram",
		strings.NewReader(`{"update_id": 1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "rahsia-ujian")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200 (update tanpa Message tak patut sebabkan ralat pelayan)", rec.Code)
	}
}
```

Run: `go test ./internal/http/... -run TestWebhookTelegram -v`
Expected: kedua-dua PASS

**Nota liputan:** spec turut senaraikan "`TELEGRAM_BOT_TOKEN` kosong
-> route webhook 404". Ni TAK diuji dgn ujian router penuh di sini
sebab `NewRouter` perlukan pool DB + banyak kebergantungan lain utk
dibina -- membina keseluruhan router hanya utk sahkan satu laluan
tak berdaftar ialah usaha tak seimbang dgn nilainya, dan tiada
endpoint config-kosong LAIN dlm sistem ni (R2/Stripe/ToyyibPay) ada
ujian "laluan tiada" yg serupa (semuanya diuji pada aras 503 handler
terus, spt `TestRequestTelegramLinkTokenTanpaBotUsernamePulang503`
Task 2). Invarian ni dikuatkuasakan struktur oleh gard `if tgBot !=
nil` (router.go Step 7) -- semakan kod, bukan ujian automatik.

- [ ] **Step 11: Sahkan build + format akhir**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: bersih semua

- [ ] **Step 12: Commit**

```bash
git add internal/http/handlers/telegram.go \
  internal/http/handlers/telegram_live_test.go \
  internal/http/router.go internal/http/router_telegram_test.go \
  cmd/api/main.go
git commit -m "feat(telegram): webhook /start + wiring bot + route"
```

---

## Task 4: Flutter — skrin binding

**Files:**
- Create: `lib/features/profile/telegram_link_page.dart`
- Modify: `lib/features/profile/profile_providers.dart` (model `Profile`)
- Modify: `lib/features/profile/profile_page.dart`
- Modify: `lib/features/auth/auth_service.dart`
- Modify: `lib/app/router.dart`
- Create: `test/features/profile/telegram_link_page_test.dart`

**Interfaces:**
- Consumes: `AuthService` (`lib/features/auth/auth_service.dart`),
  corak `AuthResult` sedia ada; `_dio` field (Dio client sedia ada
  dlm `AuthService`); respons `GET /me` kini bawa `telegram_linked:
  bool` + `telegram_username: String?` (Task 2, marc_go)
- Produces: `AuthService.requestTelegramLinkToken() ->
  Future<({bool success, String? deepLink, String? error})>`,
  `AuthService.deleteTelegramLink() -> Future<AuthResult>`

- [ ] **Step 1: Tambah 2 kaedah ke `AuthService`**

Dalam `lib/features/auth/auth_service.dart`, tambah selepas kaedah
`requestPasswordReset` sedia ada:

```dart
  /// Jana deep-link binding Telegram. Backend pulang 503 kalau ciri
  /// belum dikonfigur (TELEGRAM_BOT_TOKEN kosong) -- error itu
  /// diteruskan terus, BUKAN dineutralkan spt reset kata laluan (tiada
  /// isu enumerasi di sini -- binding perlukan auth, bukan endpoint awam).
  Future<({bool success, String? deepLink, String? error})>
  requestTelegramLinkToken() async {
    try {
      final res = await _dio.post('/me/telegram-link/token');
      return (
        success: true,
        deepLink: res.data['deep_link'] as String,
        error: null,
      );
    } on DioException catch (e) {
      return (success: false, deepLink: null, error: extractErrorMessage(e));
    } catch (_) {
      return (
        success: false,
        deepLink: null,
        error: 'Ralat tidak dijangka. Cuba lagi.',
      );
    }
  }

  Future<AuthResult> deleteTelegramLink() async {
    try {
      await _dio.delete('/me/telegram-link');
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

- [ ] **Step 2: Tambah 2 field Telegram ke model `Profile`**

`myProfileProvider` (`lib/features/profile/profile_providers.dart:86`)
ialah `FutureProvider<Profile?>` — Profile itu sendiri tak parse
`telegram_linked`/`telegram_username` lagi (respons `/me` kini bawa
kedua-duanya, Task 2 marc_go). Tambah ke class `Profile`:

Dalam constructor (`lib/features/profile/profile_providers.dart:6-19`),
tambah selepas `this.registrationPaymentStatus,`:

```dart
    required this.telegramLinked,
    this.telegramUsername,
```

Dalam senarai field (selepas baris 38, `final String?
registrationPaymentStatus;`):

```dart
  final bool telegramLinked;
  final String? telegramUsername;
```

Dalam `copyWith` (baris 47-62), tambah param + pulangan:

```dart
  Profile copyWith({
    Object? avatarUrl = _sentinel,
    bool? telegramLinked,
    Object? telegramUsername = _sentinel,
  }) {
    return Profile(
      memberId: memberId,
      email: email,
      emailVerified: emailVerified,
      status: status,
      displayName: displayName,
      phone: phone,
      roleKey: roleKey,
      roleName: roleName,
      roleRank: roleRank,
      category: category,
      avatarUrl: avatarUrl == _sentinel ? this.avatarUrl : avatarUrl as String?,
      registrationPaymentStatus: registrationPaymentStatus,
      telegramLinked: telegramLinked ?? this.telegramLinked,
      telegramUsername: telegramUsername == _sentinel
          ? this.telegramUsername
          : telegramUsername as String?,
    );
  }
```

Dalam `Profile.fromJson` (baris 66-81), tambah selepas
`registrationPaymentStatus: json['registration_payment_status'] as String?,`:

```dart
      telegramLinked: (json['telegram_linked'] as bool?) ?? false,
      telegramUsername: json['telegram_username'] as String?,
```

- [ ] **Step 3: Sahkan build sebelum teruskan**

Run: `flutter analyze`
Expected: ralat pada mana-mana tempat lain yg bina `Profile(...)` tanpa
`telegramLinked` (parameter `required` baharu) — kalau ada, itu
kemungkinan besar fail ujian sedia ada (cth `comment_tile_test.dart`
guna `Profile(...)` terus utk `_me`). Tambah `telegramLinked: false`
ke SETIAP tapak pembinaan `Profile(...)` yg gagal compile (guna
`grep -rn "Profile(" test/ lib/ --include='*.dart'` utk cari semua).
Expected selepas dibetulkan: `No issues found!`

- [ ] **Step 4: Cipta `telegram_link_page.dart`**

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';

import 'package:marc/features/auth/auth_providers.dart';
import 'package:marc/features/profile/profile_providers.dart';

class TelegramLinkPage extends ConsumerStatefulWidget {
  const TelegramLinkPage({super.key});

  @override
  ConsumerState<TelegramLinkPage> createState() => _TelegramLinkPageState();
}

class _TelegramLinkPageState extends ConsumerState<TelegramLinkPage> {
  bool _busy = false;
  String? _error;

  Future<void> _connect() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    final authService = ref.read(authServiceProvider);
    final result = await authService.requestTelegramLinkToken();
    if (!mounted) return;
    setState(() => _busy = false);

    if (!result.success || result.deepLink == null) {
      setState(() => _error = result.error ?? 'Gagal jana pautan. Cuba lagi.');
      return;
    }
    await launchUrl(
      Uri.parse(result.deepLink!),
      mode: LaunchMode.externalApplication,
    );
  }

  Future<void> _disconnect() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    final authService = ref.read(authServiceProvider);
    final result = await authService.deleteTelegramLink();
    if (!mounted) return;
    setState(() => _busy = false);

    if (!result.success) {
      setState(() => _error = result.error ?? 'Gagal nyahikat. Cuba lagi.');
      return;
    }
    ref.invalidate(myProfileProvider);
  }

  @override
  Widget build(BuildContext context) {
    final profileAsync = ref.watch(myProfileProvider);

    return Scaffold(
      appBar: AppBar(title: const Text('Telegram')),
      body: SafeArea(
        child: profileAsync.when(
          loading: () => const Center(child: CircularProgressIndicator()),
          error: (_, _) => const Center(child: Text('Gagal muat status.')),
          data: (profile) {
            // profile null cuma bila belum log masuk -- skrin ni duduk
            // atas laluan berautentikasi (Step 6 di bawah), jadi ni
            // sepatutnya tak berlaku dlm guna sebenar. Kekal sbg gard
            // jenis, bukan aliran dijangka.
            if (profile == null) {
              return const Center(child: Text('Log masuk diperlukan.'));
            }
            final linked = profile.telegramLinked;
            return ListView(
              padding: const EdgeInsets.all(24),
              children: [
                Icon(
                  linked ? Icons.check_circle : Icons.link,
                  size: 48,
                  color: linked
                      ? Theme.of(context).colorScheme.primary
                      : Theme.of(context).colorScheme.onSurfaceVariant,
                ),
                const SizedBox(height: 16),
                Text(
                  linked
                      ? 'Disambungkan${profile.telegramUsername != null ? ' sbg @${profile.telegramUsername}' : ''}'
                      : 'Sambungkan akaun Telegram anda utk menerima notifikasi tambahan.',
                  textAlign: TextAlign.center,
                  style: Theme.of(context).textTheme.bodyLarge,
                ),
                const SizedBox(height: 24),
                if (_error != null) ...[
                  Text(
                    _error!,
                    textAlign: TextAlign.center,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                  const SizedBox(height: 16),
                ],
                FilledButton(
                  onPressed: _busy ? null : (linked ? _disconnect : _connect),
                  child: _busy
                      ? const SizedBox(
                          width: 20,
                          height: 20,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : Text(linked ? 'Nyahikat' : 'Sambung Telegram'),
                ),
              ],
            );
          },
        ),
      ),
    );
  }
}
```

- [ ] **Step 5: Tambah `ListTile` ke `profile_page.dart`**

Cari blok `ListTile` sedia ada (cth `onTap: () =>
context.push('/donate')`, sekitar baris 246-252 mengikut bacaan
awal). Tambah SATU `ListTile` baharu bersebelahan yg serupa (padanan
gaya `leading`/`title`/`onTap` yg sedia ada):

```dart
                ListTile(
                  leading: const Icon(Icons.send_outlined),
                  title: const Text('Telegram'),
                  onTap: () => context.push('/telegram-link'),
                ),
```

- [ ] **Step 6: Tambah route ke `router.dart`**

Cari corak route lain yg guna `context.push` dgn laluan mudah
(bukan `onAuthPage`, sebab skrin ni perlu log masuk). Tambah:

```dart
      GoRoute(
        path: '/telegram-link',
        builder: (context, state) => const TelegramLinkPage(),
      ),
```

Tambah import `package:marc/features/profile/telegram_link_page.dart`.

- [ ] **Step 7: Sahkan `flutter analyze`**

Run: `flutter analyze`
Expected: `No issues found!`

- [ ] **Step 8: Ujian widget**

Corak override provider ni disahkan sedia ada dlm codebase
(`test/features/posts/comment_tile_test.dart:39`):
`myProfileProvider.overrideWith((ref) async => _me)`. Ikut persis:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/features/profile/profile_providers.dart';
import 'package:marc/features/profile/telegram_link_page.dart';

const _profileBelumTerikat = Profile(
  memberId: 'MARC/2026/01/0001',
  email: 'ujian@test.local',
  emailVerified: true,
  status: 'approved',
  displayName: null,
  phone: null,
  roleKey: 'ahli',
  roleName: 'Ahli',
  roleRank: 0,
  category: 'ahli',
  telegramLinked: false,
);

const _profileTerikat = Profile(
  memberId: 'MARC/2026/01/0002',
  email: 'ujian2@test.local',
  emailVerified: true,
  status: 'approved',
  displayName: null,
  phone: null,
  roleKey: 'ahli',
  roleName: 'Ahli',
  roleRank: 0,
  category: 'ahli',
  telegramLinked: true,
  telegramUsername: 'ahliuji',
);

Widget _wrap(Profile profile) => ProviderScope(
  overrides: [myProfileProvider.overrideWith((ref) async => profile)],
  child: const MaterialApp(home: TelegramLinkPage()),
);

void main() {
  testWidgets('papar butang Sambung bila belum terikat', (tester) async {
    await tester.pumpWidget(_wrap(_profileBelumTerikat));
    await tester.pumpAndSettle();

    expect(find.text('Sambung Telegram'), findsOneWidget);
    expect(find.text('Nyahikat'), findsNothing);
  });

  testWidgets('papar keadaan Disambungkan + username bila terikat', (
    tester,
  ) async {
    await tester.pumpWidget(_wrap(_profileTerikat));
    await tester.pumpAndSettle();

    expect(find.text('Nyahikat'), findsOneWidget);
    expect(find.textContaining('@ahliuji'), findsOneWidget);
  });
}
```

Run: `flutter test test/features/profile/telegram_link_page_test.dart`
Expected: PASS

- [ ] **Step 9: Sahkan suite penuh**

Run: `flutter analyze && flutter test`
Expected: `No issues found!`, semua ujian PASS

- [ ] **Step 10: Commit**

```bash
git add lib/features/profile/telegram_link_page.dart \
  lib/features/profile/profile_page.dart \
  lib/features/profile/profile_providers.dart \
  lib/features/auth/auth_service.dart lib/app/router.dart \
  test/features/profile/telegram_link_page_test.dart
git commit -m "feat(telegram): skrin binding akaun Telegram di app"
```

---

## Task 5: Kemas kini dokumen

**Files:**
- Modify: `marc_go/TODO.md`
- Modify: `marc_go/README.md`
- Modify: `marc_go/ARCHITECTURE.md`
- Modify: `marc_go/DATABASE.md`
- Modify: `marc_go/docs/README.md`
- Modify: `marc_flutter/TODO.md`

**Interfaces:**
- Consumes: hasil Task 1-4 (fail sebenar yg dicipta/diubah)

- [ ] **Step 1: `ARCHITECTURE.md`** — tambah subseksyen "Binding
  Telegram" selepas subseksyen "Reset kata laluan" (padanan format:
  keputusan reka bentuk + rujukan fail), dan tambah
  `TELEGRAM_BOT_TOKEN` ke jadual config bahagian "503 yang jelas".

- [ ] **Step 2: `DATABASE.md`** — dokumen 3 lajur baharu `profiles`
  + jadual `telegram_link_tokens` (padanan cara `password_reset_tokens`
  didokumen).

- [ ] **Step 3: `TODO.md`** — tambah entri `L38` (atau nombor
  seterusnya ikut keadaan semasa) menutup ciri ni, tandakan fasa 2
  (notifikasi) dan fasa 3 (2FA) sbg kerja masa depan berasingan yg
  BERGANTUNG pada L38.

- [ ] **Step 4: `README.md`** + **`docs/README.md`** — tambah baris
  jadual Spec & plan (padanan baris L32 sedia ada), sebut env var
  baharu dlm mana-mana senarai config sedia ada.

- [ ] **Step 5: `marc_flutter/TODO.md`** — tambah entri "Backend
  L38 (2026-08-22) — binding Telegram" padanan format entri L32/L33
  sedia ada, sebut skrin `telegram_link_page.dart` + route
  `/telegram-link`.

- [ ] **Step 6: Commit**

```bash
cd marc_go && git add TODO.md README.md ARCHITECTURE.md DATABASE.md docs/README.md
git commit -m "docs: tutup L38 (binding Telegram fasa 1)"
cd ../marc_flutter && git add TODO.md
git commit -m "docs: rekod binding Telegram fasa 1 (backend L38)"
```
