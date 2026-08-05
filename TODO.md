# MARC Backend (Go + Gin + Postgres + sqlc) — TODO

Konteks: convert dari Supabase (Auth + PostgREST + RLS) ke backend sendiri.
Auth custom penuh di Go (bukan pakai Supabase Auth). Postgres: dev guna
Postgres lokal (Homebrew), prod deploy ke Railway (Postgres plugin dia).
Tiada Docker dalam project ni. Query layer guna sqlc (raw SQL, type-safe
generated code — gantian "rasa" Drizzle di Go).

Rujukan schema asal (Supabase project "MARC", `iybmmtytsibthkcmpajw`):
tables `roles`, `profiles`, `device_tokens`, `sequences`; functions
`handle_new_user`, `is_management`, `mark_email_verified`, `next_sequence`,
`upsert_device_token`; RLS berdasarkan `auth.uid()` + `is_management()`.
Semua tu perlu di-reimplement sebagai app-level logic di Go sebab dah
tiada Supabase Auth (`auth.uid()`) atau RLS automatik.

---

## Stage 0 — Keputusan & setup asas ✅ (done)
- [x] Hosting Postgres: dev = Postgres lokal (Homebrew, tiada Docker); prod = Railway (Postgres plugin)
- [x] Struktur project: `cmd/api`, `internal/config`, `internal/db`, `internal/http` (handlers/middleware), `internal/auth` (nanti Stage 2), dsb
- [x] Config loading dari env (`godotenv` untuk dev, plain `os.Getenv` untuk prod/Railway)
- [x] Migration tool: `goose` (bukan `golang-migrate`) — timestamp naming (`yyyymmddhhmmss_name.sql`), satu fail per migration dengan marker `-- +goose Up` / `-- +goose Down`. Embedded via `go:embed`, auto-run on startup guna `database/sql` + `pgx/v5/stdlib` driver.
- [x] Setup sqlc (`sqlc.yaml`, folder `queries/`, generate ke `internal/db/sqlc`) — queries sebenar tunggu Stage 1
- [x] Init Gin router + health check endpoint (`GET /healthz`) — verified 200 OK lawan Postgres lokal

## Stage 1 — Schema Postgres (port dari Supabase) ✅ (done)
- [x] `users` table (baru — gantikan `auth.users` Supabase): id (uuid), email (unique), password_hash, created_at
- [x] `roles` table (sama macam Supabase): id, key, name, category (check: management/ahli), rank
  - [x] Seed data: ahli(10), supervisor(50,mgmt), manager(60,mgmt), superadmin(100,mgmt)
- [x] `profiles` table: id, user_id (FK users, unique), member_id (unique, format `MARC{YYYY}/{MM}/{0000}` — generated di Stage 2 guna `NextSequence`), display_name, phone, role_id (FK roles), email_verified, created_at
- [x] `device_tokens` table: id, user_id (FK users), onesignal_id (unique), platform, created_at, updated_at
- [x] `sequences` table: key (PK), current_value, updated_at — verified atomic increment via `on conflict ... do update`
- [x] Migration untuk semua di atas + indexes (`profiles.role_id`, `device_tokens.user_id`) — goose format, `20260805223000`–`20260805223600`
- [x] sqlc queries: CRUD asas untuk semua table (`queries/*.sql` → generated `internal/db/sqlc`) — users, roles, profiles (dgn join role), device_tokens (upsert/delete/list), sequences (`NextSequence`)

## Stage 2 — Auth (custom, Go) ✅ (done — termasuk hantar email sebenar)
- [x] Password hashing (bcrypt) — `internal/auth/password.go`
- [x] JWT: access token (15min, HS256) + refresh token (opaque random 32-byte, disimpan sebagai SHA-256 hash dalam `refresh_tokens`, TTL 30 hari) — `internal/auth/jwt.go`, `internal/auth/token.go`
- [x] `POST /auth/register` — create user + profile dalam satu `pgx.Tx`:
  - `member_id` guna `NextSequence` (port `handle_new_user`, key `auth:{YYYY}:{MM}`, timezone Asia/Kuala_Lumpur) — verified hasilkan `MARC2026/08/0001`
  - assign role default 'ahli' — verified
  - 409 kalau email dah wujud (unique violation `23505`)
- [x] `POST /auth/login` — verify password, issue access+refresh (401 generik bila salah, tak bocor sama ada email/password yang salah)
- [x] `POST /auth/refresh` — rotation: token lama dipadam, pasangan baru dikeluarkan — verified token lama tak boleh dipakai balik
- [x] `POST /auth/logout` — hapus refresh token (idempotent)
- [x] Middleware `RequireAuth` (`internal/http/middleware/auth.go`) — verify JWT dari header `Authorization: Bearer`, inject user id ke gin context
- [x] Email verification flow — **provider email sebenar dah sambung (Resend)**:
  - [x] `internal/email/client.go` — Resend REST API client (`POST https://api.resend.com/emails`), pattern sama macam `internal/onesignal` (`Enabled()` no-op senyap kalau `RESEND_API_KEY`/`EMAIL_FROM` kosong). 3 unit test (`httptest`, bukan API sebenar) — payload shape, auth header, no-op, error non-2xx
  - [x] `POST /auth/verify-email/request` (perlu auth) — jana opaque token, simpan hash dalam `email_verification_tokens` (TTL 1 jam), bina link `{PUBLIC_BASE_URL}/auth/verify-email/confirm?token=...`, hantar guna Resend. Kalau provider belum configure → fallback `log.Printf` link tu (dev boleh test tanpa Resend)
  - [x] `POST /auth/verify-email/confirm` (JSON body, app punya API call) — set `profiles.email_verified = true`
  - [x] **`GET /auth/verify-email/confirm?token=...`** (BARU) — endpoint sama logic tapi untuk diklik terus dari email (bukan panggil app), render HTML ringkas. Tanpa ni, hantar email dengan token tapi takde tempat nak "consume" dia = separuh siap
  - **Verified end-to-end** (bukan andaian): (1) call **Resend API sebenar** guna key betul dalam `.env`, hantar ke `delivered@resend.dev` (alamat test rasmi Resend, tak deliver kemana-mana tapi validate API call) — 204, ~710ms (network round-trip sebenar, bukan no-op), tiada error log; (2) klik link GET — 200, HTML "berjaya disahkan", `profiles.email_verified` bertukar `true` dalam DB; (3) klik link sama sekali lagi — 400 "token tidak sah" (single-use terkuatkuasa); (4) GET tanpa token — 400 "Pautan tidak sah"

**Nota teknikal**: sqlc `sqlc.yaml` ditambah override `uuid` → `github.com/google/uuid.UUID` (bukan `pgtype.UUID` generated default) — lebih ergonomic untuk JWT subject/perbandingan id, verified pgx v5 boleh encode/decode terus tanpa masalah.

## Stage 3 — Authorization (RBAC, gantikan RLS) ✅ (done)
- [x] Helper `IsManagement(ctx, q, userID)` — `internal/authz/authz.go`, port dari `is_management()` (query `GetRoleCategoryByUserID`, check category)
- [x] Middleware `RequireManagement` (`internal/http/middleware/management.go`) — mesti selepas `RequireAuth` dalam chain. Verified via httptest + DB sebenar: ahli → 403, management → 200, tiada token → 401
- [x] Ownership check pattern — didokumenkan dalam `internal/authz/authz.go`: **tiada** fungsi generic, sebaliknya handler WAJIB scope query guna `middleware.UserID(c)` (dari JWT), bukan id dari URL/body client. Ini corak yang dipakai di Stage 2 punya `/auth/*` handlers (contoh: `Logout`, `RequestEmailVerification`) dan akan dipakai sama di Stage 4

## Stage 4 — Core API endpoints ✅ (done)
- [x] `GET /me` — `internal/http/handlers/profile.go` `Me` — verified return member_id/role/etc betul
- [x] `PATCH /me` — `UpdateMe` — verified trim + string kosong → NULL, macam Flutter punya `ProfileRepository.update`
- [x] `GET /members` — `Members`:
  - [x] ahli biasa → verified return diri sendiri sahaja
  - [x] management → verified return semua (2 profile termasuk ahli tadi)
- [x] `POST /device-tokens` — `DeviceTokenHandler.Upsert` — verified 204, row masuk DB
- [x] `DELETE /device-tokens/:id` — `Delete`, discope terus dalam SQL (`id AND user_id`) — verified: user lain cuba padam token bukan miliknya → 204 (idempotent, sama pattern dgn logout) tapi row **tak terpadam** (ownership dikuatkuasakan betul-betul, bukan sekadar response code)

## Stage 5 — Push notifications (server-side)
- [x] Integrasi OneSignal REST API dari Go — `internal/onesignal/client.go` (`Client.Send`), `internal/push/service.go` (`Service.NotifyUser` — gabung `ListDeviceTokensByUser` + OneSignal client). Diuji penuh guna `go test` + `httptest` (bukan credential sebenar): payload shape, auth header, no-op bila disabled/takde token, error bila non-2xx — 7 test kesemuanya PASS
- [ ] Tentukan trigger: bila notification patut dihantar (event apa dalam app) — **belum putus**, `NotifyUser` sedia dipakai bila keputusan dibuat, tapi belum di-wire ke mana-mana route
- [ ] (Optional, kalau nak notification history dalam app) table `notifications` + endpoint `GET /notifications` — `NotificationsPage` di Flutter sekarang masih placeholder kosong; skip buat masa ni sebab keperluan produk belum jelas

## Stage 6 — Flutter integration (repo `marc_flutter`) ✅ (done, kecuali migrate data prod)
- [x] Buang `supabase_flutter` dari `pubspec.yaml`, tambah `dio` + `flutter_secure_storage`
- [x] `AuthService` (`lib/features/auth/auth_service.dart`) — guna Dio call `/auth/*` endpoints Go; error mapping backend (`{"error": "..."}`) → `extractErrorMessage`
- [x] Token storage — `lib/core/token_storage.dart` (`flutter_secure_storage`, access+refresh)
- [x] `lib/core/auth_state.dart` (`AuthNotifier`/`authNotifierProvider`) gantikan `authStateProvider` (stream Supabase) — hydrate dari storage di startup sebelum `runApp` (elak flicker redirect)
- [x] `lib/core/api_client.dart` — Dio + interceptor: lampir Bearer token automatik, auto-refresh-dan-retry sekali bila 401 (kecuali endpoint `/auth/login|register|refresh` sendiri), auto-logout kalau refresh pun gagal
- [x] `lib/core/jwt.dart` — decode claim `sub` dari access token (untuk `OneSignal.login(userId)`, gantian `session.user.id` Supabase)
- [x] `profile_providers.dart` (`myProfileProvider`, `membersProvider`, `ProfileRepository`) — guna Dio (`GET /me`, `PATCH /me`, `GET /members`); `Profile` ditambah field `email` (backend `/me` pun ditambah join `users.email` sebab UI perlukannya, sebelum ni terlepas pandang)
- [x] `push_service.dart` — `POST /device-tokens` gantikan RPC Supabase
- [x] `router.dart` — `_GoRouterRefreshNotifier` dengar `authNotifierProvider` (gantian stream Supabase), redirect guna `isLoggedIn`
- [x] `profile_page.dart` — buang rujukan `Supabase.instance.client.auth.currentUser`, guna `profile.email`
- [x] `verify_email_banner.dart` — butang "Sahkan" sekarang panggil `POST /auth/verify-email/request` betul-betul (dulu cuma snackbar "akan datang" — dah tak tepat lepas Resend disambung)
- [ ] Migrate data sedia ada (2 profiles, 4 roles) dari Supabase ke DB baru — **belum**, tunggu Postgres prod (Railway) sedia di Stage 7

**Verified** (bukan cuma `flutter analyze`):
- `flutter analyze` — 0 isu; `flutter test` — semua test lulus (termasuk test lama `auth_service_test.dart` yang di-rewrite sebab fungsi `mapAuthErrorToMessage` asal dah tak wujud)
- `flutter build web --debug` — compile penuh berjaya (deep smoke test, bukan cuma static analysis)
- **Contract test langsung lawan backend Go sebenar** (bukan mock): register → `GET /me` → `PATCH /me` → `GET /members`, parse guna `Profile.fromJson`/`MemberRow.fromJson` app sebenar — semua field padan; login salah password → `DioException` dengan mesej Melayu betul
- Nota teknikal tambahan Go: mesej ralat validation (400) yang dulu raw Go validator text ditukar ke mesej Melayu mesra (`internal/http/handlers/bind.go`) — perlu sebab Flutter display terus mesej tu ke UI

**Independent review (Opus 4.8, satu pas penuh)** jumpa 2 bug sebenar dalam `api_client.dart` punya refresh interceptor + beberapa isu LOW — semua dah dibetulkan & diuji semula:
- [x] **HIGH** — refresh concurrent 401 boleh clear() sesi yang baru sahaja berjaya di-refresh oleh sibling request. Fix: cache in-flight refresh `Future` (dedupe bila overlap), + fallback "kalau token dah bertukar time refresh kita gagal, guna token baru tu, jangan clear". Root cause sebenar: backend punya `Refresh` handler **tak atomic** (`GetRefreshTokenByHash` + `DeleteRefreshToken` berasingan = TOCTOU gap boleh buat DUA refresh request serentak dua-dua berjaya guna token sama). Fix backend: `ConsumeRefreshToken` — satu `DELETE ... RETURNING` statement. **Verified**: 10 goroutine Go betul-betul serentak refresh token yang sama → tepat 1 berjaya (200), 9 lain 401 bersih (`success_count: 1`)
- [x] **MEDIUM** — retry-lepas-refresh boleh infinite loop kalau retry pun 401. Fix: guard `retried_after_refresh` dalam `RequestOptions.extra`, sekali gagal terus clear+propagate, tak cuba refresh lagi. **Verified** via test langsung
- [x] **MEDIUM** — `friendlyBindError` (bind.go) bagi mesej salah untuk login: password kosong pun cakap "minimum 6 aksara" (itu cuma betul untuk register yang ada tag `min=6`). Fix: switch guna `Field()+Tag()`, bukan `Field()` je
- [x] **LOW** — `hydrate()` di `main.dart` boleh throw tanpa try/catch kalau secure storage rosak (keystore corrupt lepas OS upgrade) → black screen kekal. Fix: wrap try/catch, anggap logged-out kalau gagal
- [x] **LOW** — `AuthService.signIn/signUp` cuma tangkap `DioException`; ralat lain (cth `.env` hilang) buat butang submit stuck loading selama-lamanya. Fix: tambah catch generic dengan mesej fallback
- [x] **LOW** — `members_page.dart` papar raw `DioException.toString()` (termasuk URL) terus ke user bila gagal load. Fix: mesej generik je
- [x] **LOW** — `middleware/auth.go` punya mesej ralat dalam English (`"missing bearer token"`, `"invalid token"`) tak konsisten dengan seluruh app yang Bahasa Melayu. Fix: tukar ke Melayu
- [x] **LOW** — `expires_in` dalam token response hardcoded `15 * time.Minute`, tak sync dengan `cfg.AccessTokenTTL` sebenar. Fix: `auth.JWT.AccessTTL()` getter, guna terus

## Stage 7 — Deployment & ops (staging ✅ deployed; production belum)
- [x] Deploy Go API + Postgres ke Railway — **staging live**: `https://marc-go-staging.up.railway.app`
  - Projek Railway `marc` (workspace hafizbahtiar), environment `staging` + `production` — verified dua-dua wujud
  - Service `marc-go` (staging) — build via Nixpacks auto-detect (tiada Dockerfile custom), deploy status `SUCCESS`
  - `Postgres` service (image `postgres-ssl:18`) — migration goose auto-apply on startup, verified (`goose: no migrations to run` = dah up to date)
  - Env vars staging verified set: `DATABASE_URL` (private networking `postgres.railway.internal`), `JWT_SECRET`, `ONESIGNAL_APP_ID`/`API_KEY`, `RESEND_API_KEY`, `EMAIL_FROM` (`hafiz@hafizbahtiar.com` — **nota**: perlu domain verified kat Resend, belum confirm), `PUBLIC_BASE_URL` (betul point ke domain staging sendiri)
  - **Full smoke test lawan staging live** (bukan localhost): `POST /auth/register` → `GET /me` → 200, `member_id` generate betul, migration schema penuh wujud — confirm seluruh stack (Go + Postgres + goose + sqlc) berfungsi di persekitaran produksi sebenar
  - **2 isu produksi jumpa & dibetulkan** (dari baca log staging sebenar, bukan andaian):
    - [x] Gin jalan `debug` mode → set `GIN_MODE=release` (env var Railway, tiada perlu ubah kod — Gin baca env ni native)
    - [x] Gin "trust all proxies" (client IP boleh spoof) → `router.SetTrustedProxies(...)`. **Percubaan pertama guna RFC1918 standard (10.0.0.0/8 dll) SALAH** — log lepas deploy tunjuk `ClientIP()` still papar IP dalaman Railway (`100.64.0.2`), bukan IP awam sebenar. Punca: Railway punya private network guna **CGNAT range `100.64.0.0/10`**, bukan RFC1918. Fix kedua (tambah `100.64.0.0/10`) verified betul — log lepas tu papar IP awam sebenar (`79.127.228.17`)
- [ ] Deploy production environment (sekarang staging je live)
- [x] CI: build + test Go, lint (`golangci-lint`) — `.github/workflows/ci.yml` (2 job: `build-test`, `lint`), `.golangci.yml` (v2 config, exclude errcheck untuk `defer Close()/Rollback()`). **Percubaan pertama gagal**: `golangci-lint-action@v6` tak support config schema v2 (perlu v7+) — dibetulkan, verified run pass on GitHub (`build-test` + `lint` dua-dua `success`) untuk setiap commit lepas ni
- [x] Structured logging (request id, error tracking) — `internal/http/middleware/logging.go`, `slog` JSON handler (stdout), satu baris log per request (`request_id`, `method`, `path`, `status`, `latency`, `client_ip`), `request_id` turut dihantar balik via header `X-Request-Id`. Gantikan `gin.Default()` punya plain-text logger. Verified: JSON log line betul-betul structured, header wujud
- [x] Basic rate limiting untuk `/auth/login`, `/auth/register` — `internal/http/middleware/ratelimit.go`, token-bucket per-IP (`golang.org/x/time/rate`, 5/min, burst 5), cleanup goroutine buang entry lama. Route lain (`/healthz`, `/me`, dsb) tak terjejas. Verified: 5 request lulus, request ke-6/7 dapat 429, refill semula lepas beberapa saat
- [x] Domain `hafizbahtiar.com` verified kat Resend — `EMAIL_FROM=hafiz@hafizbahtiar.com` staging, confirmed hantar email berjaya (204, ~server-to-server round trip, bukan reject)
- [x] **Prasyarat Stage 8 siap**: `PUBLIC_BASE_URL` staging betul, `EMAIL_VERIFY_URL` staging dah set — Stage 8 siap sepenuhnya (bawah)

## Stage 8 — Email verification landing page di `hafizbahtiar.com` (cross-repo) ✅ (done)
Keputusan: link email verification buka page branded di portfolio
(`hafizbahtiar.com/verify-email`) — bukan HTML mentah yang Go backend
render sendiri.

- [x] Resend: domain `hafizbahtiar.com` verified — `EMAIL_FROM` staging
  dah guna `hafiz@hafizbahtiar.com`, confirmed hantar email berjaya
- [x] marc_go: config baru `EMAIL_VERIFY_URL` (`internal/config/config.go`)
  — kalau diisi, `RequestEmailVerification` (auth.go) bina link
  `{EMAIL_VERIFY_URL}?token=...` (arah portfolio); kalau kosong, fallback
  ke Go punya HTML page sendiri (`{PUBLIC_BASE_URL}/auth/verify-email/confirm`)
  — **backward compatible**, dev/persekitaran tanpa portfolio config tetap
  jalan. Verified dua-dua path (dengan & tanpa env) lokal
- [x] portfolio-astro: `src/pages/verify-email.astro` (BARU) — SSR (server-
  side `fetch` dalam frontmatter Astro, bukan client-side — CSP
  `connect-src` tak perlu diubah langsung sebab call server-to-server dalam
  Cloudflare Worker tak lalu CSP). Baca `?token=`, `POST` ke
  `{MARC_API_URL}/auth/verify-email/confirm`, render success/fail card
  ikut design system sedia ada (`CoreLayout` + `Background`, style match
  `login.astro`). `MARC_API_URL` env baru — sengaja bukan `PUBLIC_*`
  (server-only, tak pernah masuk client bundle)
- [x] **Verified end-to-end penuh** (bukan andaian):
  - `npm run build` (portfolio) — TypeScript check + Vite bundle bersih
  - Test lokal: `astro dev` + Go backend lokal (token sebenar dari log) →
    hit `/verify-email?token=...` → page papar "Email disahkan", DB
    `profiles.email_verified` bertukar `true` — verified terus query DB
  - Failure path: token invalid → "Pengesahan gagal"; tiada token →
    "Pautan tidak sah" — dua-dua verified
  - Link construction di Go: verified `EMAIL_VERIFY_URL` diisi → link jadi
    `https://hafizbahtiar.com/verify-email?token=...`; kosong → fallback
    Go punya HTML page — dua-dua path verified lokal
  - Railway staging: `EMAIL_VERIFY_URL` env di-set, redeploy `SUCCESS`,
    full flow (register → request verification → Resend 204) diuji lawan
    staging live
- Fail berkaitan: `portfolio-astro` commit `a1b13a3` ("feat(verify-email):
  add email verification landing page with server-side handling")

---

## Stage 9 — Postgres RLS sebagai defense-in-depth (keputusan: YA, belum implement)
Keputusan dibuat: RLS lebih selamat, nak tambah sebagai lapisan kedua atas
app-level ownership check yang dah ada (Stage 3) — bukan gantikan dia.

**Gotcha teknikal penting** (kena selesai dulu sebelum RLS ni ada makna):
Railway `DATABASE_URL` sekarang connect guna role **`postgres`** (verified
`railway variable list` — `postgresql://postgres:...@postgres.railway.internal/railway`).
Role `postgres` di Postgres **superuser**, dan **superuser automatik bypass
RLS**, tak kira apa policy pun ada. Kalau backend terus guna role ni, tambah
`ENABLE ROW LEVEL SECURITY` + policy **tak buat apa-apa** — masih 100%
bergantung app-level check macam sekarang, RLS jadi decoration je.

Kerja sebenar yang perlu (bukan setakat `CREATE POLICY`):
- [ ] Cipta DB role baru khas untuk app (bukan superuser, `NOSUPERUSER`,
  tiada `BYPASSRLS`), `GRANT` privilege secukup pada table yang perlu
- [ ] Tukar `DATABASE_URL` (dev + Railway staging/prod) guna role baru ni
- [ ] Migration: `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` + `CREATE POLICY`
  untuk `profiles`, `device_tokens`, `refresh_tokens`,
  `email_verification_tokens` (bukan `users`/`roles`/`sequences` — table
  tu tak per-user secara sama)
- [ ] **Perubahan besar cara app query DB**: RLS policy perlu tahu "siapa
  user semasa" — Postgres tak tahu tentang JWT/gin context kita. Kena
  `SET LOCAL app.current_user_id = '<uuid>'` di **setiap** transaction
  authenticated request sebelum query jalan (policy rujuk
  `current_setting('app.current_user_id')::uuid`). Ni bermakna:
  - Semua handler yang guna `sqlc.Queries` terus atas `pool` (bukan `tx`)
    kena tukar untuk sentiasa buka transaction dulu — impact kod agak luas
    (`profile.go`, `device_tokens.go`, sebahagian `auth.go`)
  - Perlu helper baru (cth `db.WithUserContext(ctx, pool, userID, fn)`)
    untuk elak duplicate boilerplate di setiap handler
- [ ] Test: pastikan app-level check + RLS dua-dua enforce (defense-in-depth
  sebenar — cuba bypass app-level logic secara sengaja dalam test, RLS
  patut masih tahan)

**Belum start** — scope lebih besar dari nampak kat permukaan (bukan cuma
tambah migration), sengaja tak diselitkan dalam sprint semasa. Return to
this bila ada slot khusus.
