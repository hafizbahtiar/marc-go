# Architecture

## Struktur direktori

```
cmd/api/main.go              — entrypoint: load env → migrate → connect DB → start Gin
internal/
  config/                    — load & validate env vars
  db/
    db.go                    — pgxpool connection + goose migration runner (embedded)
    migrations/               — fail migration goose (single-file Up/Down)
    sqlc/                     — kod generated sqlc (JANGAN edit terus)
  auth/                      — password hashing, JWT, opaque token generation
  authz/                     — RBAC helper (IsManagement) + dokumentasi ownership pattern
  email/                     — Resend client
  onesignal/                 — OneSignal REST API client
  push/                      — gabung device_tokens + onesignal client (NotifyUser)
  http/
    router.go                — wiring semua route
    handlers/                — satu fail per domain (auth, profile, device_tokens, health, bind)
    middleware/               — RequireAuth, RequireApprovedStatus, RequireVerifiedEmail
queries/                     — SQL sumber untuk sqlc (input, bukan output)
```

## Alir request (contoh: `GET /me`)

```
Client
  │ Authorization: Bearer <access_token>
  ▼
Gin router (router.go)
  │
  ▼
middleware.RequireAuth        — verify JWT (HS256), reject kalau invalid/expired,
  │                              set user id ke gin.Context
  ▼
handlers.ProfileHandler.Me
  │
  ▼
sqlc.Queries.GetProfileByUserID(ctx, userID)   — query di-scope terus guna
  │                                               userID dari JWT, BUKAN dari
  │                                               URL/body client
  ▼
pgxpool → Postgres
  │
  ▼
JSON response
```

## Kenapa custom auth (bukan Supabase Auth)

Keputusan awal projek (lihat `TODO.md` Stage 0): full rewrite, tak bergantung
Supabase langsung. Trade-off: lebih kerja (semua yang Supabase Auth bagi
percuma — hashing, token, RLS — kena bina sendiri), tapi full control & tiada
vendor dependency untuk sistem yang critical (auth).

## Auth: JWT access token + opaque refresh token (rotated)

- **Access token**: JWT (HS256), TTL 15 minit, `sub` claim = user id. Stateless
  — server tak simpan, verify terus guna secret.
- **Refresh token**: random opaque string (32 byte), **disimpan sebagai
  SHA-256 hash** dalam `refresh_tokens` (bukan plaintext — kalau DB bocor,
  token asal tak boleh dipakai terus).
- **Rotation, single-use, atomic**: `POST /auth/refresh` guna SATU statement
  `DELETE ... RETURNING` (`ConsumeRefreshToken` query) — bukan
  `SELECT` diikuti `DELETE` berasingan. Ini sengaja: kalau dua request
  refresh serentak hantar token yang sama (race), Postgres row-level lock
  jamin cuma SATU dapat row balik (menang); yang satu lagi dapat 0 rows →
  401 bersih. Reka bentuk asal (get-then-delete berasingan) ada TOCTOU gap
  yang boleh buat DUA-DUA refresh berjaya serentak — dibetulkan lepas
  independent review jumpa isu ni (lihat `TODO.md` Stage 6 punya bug log).

## Authorization (RBAC) — gantian Postgres RLS

Supabase asal guna RLS (`auth.uid() = id`, function `is_management()`).
Backend Go ni tiada RLS automatik — semua enforcement app-level:

1. **Role check** (`internal/authz/authz.go` `IsManagement`) — dipanggil
   terus inline dalam handler (bukan middleware route-group-level, sebab
   check management selalunya perlu logic tambahan ikut target — cth.
   `setMemberStatus` kena tahu SIAPA target sebelum putuskan, bukan
   cuma "route ni management-only").
2. **Ownership check** — **tiada fungsi generic untuk ni.** Pattern yang
   dipakai: handler WAJIB scope query guna user id daripada
   `middleware.UserID(c)` (hasil verify JWT), **tak pernah** terima "whose
   resource" daripada URL/body yang client hantar. Contoh:
   `DeleteDeviceToken(id, user_id)` — query itu sendiri yang jamin user
   cuma boleh padam device token dia sendiri, walaupun dia teka id device
   token orang lain.

## Email verification

Token opaque (macam refresh token — random + SHA-256 hash di DB, TTL 1 jam).
Dua cara consume:

- `POST /auth/verify-email/confirm` (JSON body) — dipanggil dari app.
- `GET /auth/verify-email/confirm?token=` — dipanggil dari klik link email
  terus (render HTML ringkas, bukan JSON, sebab dibuka browser bukan app).

Kedua-dua guna logic sama (`consumeEmailVerificationToken`), elak duplicate.

**Rancangan**: link akan diarah ke `hafizbahtiar.com/verify-email` (branded
page, server-side call ke backend ni) bukan Go punya HTML mentah — lihat
`TODO.md` Stage 8. Bergantung pada backend ni deployed awam dulu (Stage 7).

## Push notification

`internal/onesignal` (REST API client) + `internal/push` (`NotifyUser` —
gabung `device_tokens` + client). **Belum di-wire ke mana-mana route** —
trigger (event apa dalam app yang patut hantar push) belum diputuskan
produk-wise. Primitive sedia pakai bila keputusan dibuat.

## Config / env

`internal/config/config.go` — semua env var, kebanyakan optional dengan
no-op senyap kalau kosong (OneSignal, Resend). Wajib: `DATABASE_URL`,
`JWT_SECRET`. `PUBLIC_BASE_URL` dipakai bina link dalam email (default
`http://localhost:8080` untuk dev).

## Kenapa sqlc + goose (bukan ORM penuh macam GORM)

Keputusan awal (Stage 0/2): user biasa dengan Drizzle (node). sqlc paling
dekat dengan "rasa" tu di Go — tulis SQL raw, generate types, bukan query
builder magic. goose dipilih atas `golang-migrate` sebab format satu-fail +
timestamp naming (lihat diskusi penuh dalam `TODO.md`).

## Deployment

Railway, projek `marc`, environment `staging` + `production`. Tiada Docker
— `config.Load()` baca `DATABASE_URL` terus daripada Postgres plugin Railway,
tiada kod berubah antara dev/staging/prod.
