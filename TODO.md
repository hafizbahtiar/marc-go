# MARC Backend (Go + Gin + Postgres + sqlc) — TODO

Konteks: convert dari Supabase (Auth + PostgREST + RLS) ke backend sendiri.
Auth custom penuh di Go (bukan pakai Supabase Auth). Postgres: dev guna
Postgres lokal (Homebrew), prod di Railway (`marc` projek, `staging` +
`production` environment — staging live). Tiada Docker. Query layer sqlc.

Sejarah penuh (keputusan, gotcha, hasil verifikasi setiap stage) ada dalam
git log — cari commit message ikut nombor stage kalau perlu rujuk balik.
Fail ni fokus status semasa + kerja pending sahaja.

---

## Selesai (Stage 0–8)

- **Stage 0-1** — Setup projek (Gin, goose, sqlc, no Docker) + schema asas
  (`users`, `roles`, `profiles`, `device_tokens`, `sequences`)
- **Stage 2** — Auth custom penuh: bcrypt, JWT access + refresh token
  rotated (atomic single-use), email verification (Resend, link boleh
  diklik)
- **Stage 3** — RBAC: `IsManagement` + `RequireManagement` middleware,
  ownership pattern (query di-scope guna JWT user id, bukan client-supplied id)
- **Stage 4** — Core endpoints: `/me`, `/members`, `/device-tokens`
- **Stage 5** — OneSignal client sedia (`internal/onesignal`,
  `internal/push`) — trigger di-wire dalam Stage 10 (Posts) di bawah
- **Stage 6** — Flutter integration penuh (repo `marc_flutter`) — Supabase
  dibuang sepenuhnya, ganti Dio + backend Go. Independent review (Opus)
  jumpa & fix 2 bug HIGH/MEDIUM dalam refresh interceptor + beberapa LOW
- **Stage 7** — Deploy Railway staging (`https://marc-go-staging.up.railway.app`),
  CI (GitHub Actions + golangci-lint), structured logging (slog JSON +
  request id), rate limit `/auth/login`+`/auth/register` (5/min per IP)
- **Stage 8** — Email verification landing page di `hafizbahtiar.com`
  (cross-repo dengan `portfolio-astro`), SSR server-to-server call

**Belum siap dari stage atas**:
- [ ] Deploy `production` environment Railway (staging je live sekarang)
- [ ] Migrate data lama (2 profiles, 4 roles) dari Supabase ke DB baru
- [ ] Audit Flutter app penuh (9 isu jumpa & fix — auth session handling,
  device token unlink, UX error states, dsb — lihat commit `marc_flutter`)

---

## Stage 9 — Postgres RLS sebagai defense-in-depth (keputusan: YA, belum implement)
Keputusan dibuat: RLS lebih selamat, nak tambah sebagai lapisan kedua atas
app-level ownership check yang dah ada (Stage 3) — bukan gantikan dia.

**Gotcha teknikal penting** (kena selesai dulu sebelum RLS ni ada makna):
Railway `DATABASE_URL` sekarang connect guna role **`postgres`** (superuser
— **automatik bypass RLS**, tak kira apa policy pun ada). Kalau backend
terus guna role ni, RLS jadi decoration je.

Kerja sebenar yang perlu:
- [ ] Cipta DB role baru khas untuk app (`NOSUPERUSER`, tiada `BYPASSRLS`),
  `GRANT` privilege secukup, tukar `DATABASE_URL` (dev + staging + prod)
- [ ] Migration: `ENABLE ROW LEVEL SECURITY` + `CREATE POLICY` untuk
  `profiles`, `device_tokens`, `refresh_tokens`, `email_verification_tokens`
  (dan table Posts baru — Stage 10 — bila siap)
- [ ] **Perubahan besar cara app query DB**: perlu `SET LOCAL
  app.current_user_id = '<uuid>'` setiap transaction authenticated request
  (policy rujuk `current_setting(...)`) — impact kod luas (semua handler
  yang query terus atas `pool`), perlu helper baru elak boilerplate
- [ ] Test: app-level check + RLS dua-dua enforce (cuba bypass app-level
  logic secara sengaja dalam test, RLS patut masih tahan)

**Belum start** — scope lebih besar dari nampak (bukan cuma tambah
migration). Return to this bila ada slot khusus.

---

## Stage 10 — Posts feature (feed + comment + like, macam Twitter/Facebook)

Design penuh dibrainstorm & confirmed dengan user (2026-08-06). Keputusan
produk:
- Post: text + gambar (1+ imej), jenis `normal` atau `announcement`
  (management-only, ahli biasa 403 kalau cuba create jenis ni)
- Comment: **nested**, cap depth 2 paras app-level (reply kat reply-paras-2
  jadi top-level reply baru merujuk comment asal — padanan Facebook betul)
- Like: toggle tunggal (bukan multi-reaction), untuk post DAN comment
- Visibility: semua ahli **yang email_verified** boleh access (`payment`
  gate akan ditambah lepas payment system siap — bincang lain hari, jangan
  block Stage 10 sebab ni)
- Media storage: Cloudflare R2, upload guna **presigned URL** (client
  upload terus ke R2, bukan proxy through Go — elak Go jadi bottleneck
  bandwidth)
- Moderation: owner boleh edit/delete sendiri; **management boleh
  delete/moderate post/comment sesiapa**
- Edit: post & comment boleh diedit, papar indicator "(disunting)"
  (`edited_at` bukan null) — bukan full edit history
- Delete: **soft delete** (`deleted_at`), bukan hard delete — audit trail
  moderation + child comment tak "reput" tiba-tiba (UI papar
  "[Post telah dipadam]" untuk parent yang dipadam)
- Push notification: like + comment pada post/comment **sendiri** sahaja
  (bukan reply-to-reply dsb — scope masa depan kalau perlu)
- **Tambah table `notifications` (in-app)** sekali — bukan cuma push
  external. `NotificationsPage` Flutter sekarang placeholder kosong,
  feature ni beri sebab konkrit untuk isi tab tu

### Data model (migration baru)

```
posts             id, author_id, type ('normal'|'announcement'), content,
                  created_at, edited_at (null), deleted_at (null)
post_images       id, post_id, r2_key, position
post_likes        post_id, user_id, created_at  (PK composite, toggle)
comments          id, post_id, parent_comment_id (null=top-level),
                  author_id, content, created_at, edited_at, deleted_at
comment_likes     comment_id, user_id, created_at  (PK composite)
notifications     id, recipient_id, actor_id, type ('post_like'|'post_comment'),
                  post_id, comment_id (null), read_at (null), created_at
```

### API endpoints

```
GET    /posts?cursor=&limit=          -- feed reverse-chron, cursor-based
POST   /posts                         -- create (content + image r2_keys[])
GET    /posts/:id
PATCH  /posts/:id                     -- owner sahaja
DELETE /posts/:id                     -- owner ATAU management (soft delete)

POST   /posts/:id/like                -- idempotent
DELETE /posts/:id/like
POST   /comments/:id/like
DELETE /comments/:id/like

GET    /posts/:id/comments            -- flat list + parent_comment_id, client bina tree
POST   /posts/:id/comments            -- content, parent_comment_id optional
PATCH  /comments/:id                  -- owner sahaja
DELETE /comments/:id                  -- owner ATAU management (soft delete)

POST   /uploads/presign               -- {content_type} -> {upload_url, r2_key}, expire 5min

GET    /notifications?cursor=&limit=
POST   /notifications/:id/read
POST   /notifications/read-all
```

### Kerja backend

- [ ] Migration: `posts`, `post_images`, `post_likes`, `comments`,
  `comment_likes`, `notifications` (goose format, single-file)
- [ ] sqlc queries untuk semua table atas (`queries/posts.sql`,
  `queries/comments.sql`, `queries/notifications.sql`)
- [ ] `internal/storage/r2.go` — Cloudflare R2 client (S3-compatible,
  `aws-sdk-go-v2` dengan custom endpoint), generate presigned PUT URL.
  Env baru: `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`,
  `R2_BUCKET_NAME`, `R2_PUBLIC_URL` (untuk bina URL baca gambar)
- [ ] Middleware `RequireVerifiedEmail` (lepas `RequireAuth`) — check
  `profiles.email_verified`, 403 kalau belum. Letak comment jelas: extension
  point untuk payment gate nanti
- [ ] Handler `internal/http/handlers/posts.go` — semua endpoint posts+likes
- [ ] Handler `internal/http/handlers/comments.go` — semua endpoint comments+likes
- [ ] Handler `internal/http/handlers/notifications.go`
- [ ] Handler `internal/http/handlers/uploads.go` — presign
- [ ] Wire `push.NotifyUser` bila like/comment berlaku pada content sendiri
  — insert row `notifications` DAN hantar push (dua-dua, bukan salah satu)
- [ ] Cap nested comment depth 2 — logic di handler create comment (kalau
  `parent_comment_id` merujuk comment yang sendiri dah ada parent, guna
  parent punya parent sebagai rujukan sebenar, bukan reject)
- [ ] Update router.go — wire semua route baru, authz check (`type:
  announcement` → `RequireManagement`)
- [ ] Test: RBAC (announcement create ahli vs management), ownership
  (edit/delete bukan-milik → 403/404), moderation (management delete orang
  lain punya post), soft-delete behavior (comment lepas parent post
  dipadam), cap nested depth, presign URL flow

---

## Belum putus / perlu bincang lagi
- Payment/membership dues system — akan tentukan gate tambahan untuk Posts
  visibility bila siap (bincang berasingan, bukan sekarang)
