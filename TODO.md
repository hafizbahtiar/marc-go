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

### Kerja backend ✅ (done)

- [x] Migration: `posts`, `post_images`, `post_likes`, `comments`,
  `comment_likes`, `notifications` — verified up/down bersih lawan Postgres
- [x] sqlc queries (`queries/posts.sql`, `likes.sql`, `comments.sql`,
  `notifications.sql`) + `profiles.sql` tambah `GetEmailVerifiedByUserID`
- [x] `internal/storage/r2.go` — R2 client (`aws-sdk-go-v2`), presigned PUT.
  No-op graceful (503, bukan crash) kalau env R2 kosong — verified
- [x] Middleware `RequireVerifiedEmail` (`internal/http/middleware/verified.go`)
- [x] Handler `posts.go`, `comments.go`, `notifications.go`, `uploads.go`
  + `posts_common.go` (response builder batched — elak N+1 query untuk
  like/comment count + liked-by-me bila list feed)
- [x] `push.NotifyUser` + insert `notifications` row bila like/comment pada
  content sendiri (`notifyOwner` helper, self-notify di-skip)
- [x] Cap nested comment depth 2 (`resolveParentCommentID`) — reply pada
  comment depth-2 automatik "flatten" ke parent depth-1 asal
- [x] Router + main.go wired penuh

**Verified end-to-end lawan Postgres sebenar** (bukan andaian):
- Gate `email_verified`: create post sebelum verify → 403; lepas verify → berjaya
- RBAC: ahli cipta `type: announcement` → 403; management → 201
- Nested depth cap: reply pada comment depth-2 → `parent_comment_id` betul-betul
  flatten ke comment depth-1 asal (diverify field response tepat)
- Like: `like_count` bertambah, `liked_by_me` betul, self-like TAK cipta
  notification (verified count notification tak bertambah)
- Notification: like + comment pada post sendiri → row `notifications`
  tercipta dengan `type` betul; mark-read verified (`read_at` bertukar di DB)
- Moderation: management padam/edit post/comment **bukan miliknya** → 204/200;
  ahli biasa cuba sama → 403 (dua-dua arah diverify)
- Soft delete: post dipadam → `deleted_at` set (row masih wujud), hilang
  dari feed, tapi **comment di bawah masih boleh dibaca** (design intent:
  comment tak "reput" bila parent dipadam) — verified
- Presign upload: R2 belum configure → 503 graceful, bukan crash — verified

**Update**: credential R2 dah di-provision (`.env` diisi). Presign generate
URL betul (200, URL sah). Tapi jumpa bug real bila test PUT upload sebenar
ke R2:
- [x] **Fix**: `aws-sdk-go-v2` default auto-tambah checksum CRC32 pada
  request S3 (termasuk presigned URL) — R2 tak fully compatible, signature
  jadi tak sah, client dapat 403 walaupun presign sendiri berjaya. Fix:
  `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired`
  (commit `7c2fe81`)
- [ ] **Belum selesai**: lepas fix atas, PUT sebenar ke R2 **masih** 403
  `AccessDenied` (generic, bukan `SignatureDoesNotMatch` — jadi kemungkinan
  besar bukan isu signature/code lagi). Paling mungkin: **R2 API token
  permission scope** — perlu semak Cloudflare dashboard: token ada "Object
  Read & Write" untuk bucket `marc-staging` yang betul? Ini bukan sesuatu
  saya boleh diagnose lanjut tanpa akses dashboard — perlu tindakan kau.
  Re-verified lagi sekali semasa kerja had saiz gambar di bawah — 403 sama
  berlaku, confirm ini isu permission token yang consistent, bukan regresi
  code baru
- [ ] `R2_PUBLIC_URL` masih kosong dalam `.env` — perlu diisi (r2.dev
  subdomain atau custom domain) sebelum gambar yang berjaya upload boleh
  dipaparkan balik

### Had saiz & bilangan gambar ✅ (done)

Keputusan: R2 **tak support presigned POST** (`PresignPostObject` →
`501 Presigned post requests are not yet implemented`) — jadi
`content-length-range` policy condition (cara S3 standard enforce had saiz
di peringkat presign) tak boleh dipakai. Pivot: had dikuatkuasakan LEPAS
upload, sebelum r2_key diterima masuk post.

- [x] `internal/storage/r2.go` — `MaxImageSizeBytes` (5MB), `MaxImagesPerPost`
  (4), `VerifyImageSize` (`HeadObject`, tolak kalau `ContentLength` lebih
  had), `DeleteImage` (cleanup gambar ditolak, elak orphan bucket)
- [x] `posts.go` `Create`: tolak `len(r2_keys) > 4` (400, sebelum sentuh R2
  langsung), loop `VerifyImageSize` setiap key sebelum `tx.Begin` (400 +
  delete kalau lebih 5MB atau r2_key tak sah/belum diupload)
- [x] Verified: 5 r2_keys → 400 "maksimum 4 gambar setiap post"; 4 r2_keys
  palsu → 400 "gambar tidak sah atau belum diupload" (VerifyImageSize
  correctly reject sebab objek tak wujud di R2); post text-je tanpa gambar
  → 201 berjaya (r2_keys tetap optional)
- [ ] **Belum verified end-to-end**: happy-path (upload kecil berjaya →
  post berjaya) dan reject-path saiz-lebih-5MB — kedua-dua block oleh isu
  R2 permission token di atas (PUT sebenar masih 403), bukan isu logic
  code. Verify bila isu token diselesaikan.

### Kerja Flutter ✅ (done, kecuali R2 upload — sekat oleh isu atas)
Lihat `marc_flutter/TODO.md` Stage 10 untuk detail penuh + hasil verifikasi.

---

## Stage 11 — Status pendaftaran ahli (approval khusus MAIWP)

App ni khusus untuk kakitangan MAIWP — pendaftaran akaun baru kena melalui
1 lapisan verify/approve oleh pihak berkuasa (management), bukan cuma
`email_verified` macam sekarang. Design penuh dibrainstorm & confirmed
dengan user (2026-08-07), spec di
`docs/superpowers/specs/2026-08-07-member-approval-status-design.md`.

Keputusan design (soalan asal semua dah dijawab):
- Status: `pending` → `approved`/`rejected` je (tiada `suspended` buat masa ni)
- Gate: `status` disimpan di `profiles` (bukan `users`), default `pending`
  untuk akaun baru; akaun sedia ada backfill terus ke `approved`
- Approve/reject oleh sesiapa dalam kategori role `management` sedia ada
  (`IsManagement`) — tiada role `admin` berasingan
- Reject **tak** padam data (boleh apply semula lain hari)
- Notification dua arah: management dapat `member_pending` bila ada
  pendaftaran baru; ahli dapat `member_approved`/`member_rejected` bila
  status dia berubah — guna infra `notifications` (in-app) + email sedia ada

### Kerja backend ✅ (done)

- [x] Migration: tambah column `status` (`pending`/`approved`/`rejected`,
  default `pending`), `approved_by`, `approved_at` kat `profiles` —
  backfill akaun sedia ada ke `approved`
- [x] Migration: widen `notifications.type` terima `member_pending`/
  `member_approved`/`member_rejected`
- [x] sqlc queries: list ahli pending, approve/reject (set status +
  `approved_by`/`approved_at`)
- [x] Middleware `RequireApprovedStatus` (padanan `RequireVerifiedEmail`,
  `internal/http/middleware/verified.go`) — gate semua endpoint kecuali
  `/me` (GET/PATCH) sehingga status `approved`
- [x] Router wired: `GET /members?status=pending`,
  `POST /members/:id/approve`, `POST /members/:id/reject`
  (`ProfileHandler.ApproveMember`/`RejectMember`, guna `IsManagement`)
- [x] Registration fan-out: notification in-app `member_pending` ke
  setiap management user semasa akaun baru register

**Verified end-to-end lawan Postgres sebenar** (DB `marc_test`, bukan andaian):
- Register: akaun baru → 201, mula `status: pending`
- Gate `pending`: `/me` → 200 (`status: pending`); `/members` → 403
  `"akaun anda belum diluluskan pihak pengurusan"`; `/auth/verify-email/request`
  → 403 mesej sama — tiga-tiga diverify
- Management: seed akaun, promote role + status `approved` terus di DB,
  `GET /members?status=pending` pulang tepat 2 entri (dua akaun ahli baru)
- Approve: `POST /members/:id/approve` → 200, `status: approved`,
  `approved_by`/`approved_at` terisi betul (user id + timestamp management)
- Reject: `POST /members/:id/reject` → 200, `status: rejected`,
  `approved_by`/`approved_at` terisi sama macam approve
- RBAC: ahli biasa (status `approved`, bukan management) cuba
  `POST /members/:id/approve` → 403 `"cuma pengurusan boleh luluskan/tolak ahli"`
- Selepas approve: akaun terbabit `/members` → 200 (unlock),
  `/auth/verify-email/request` → 204 (tak lagi 403)
- Selepas reject: `/me` → 200 tetap papar `status: rejected`, `/members`
  masih 403 (kekal blocked, data tak dipadam — confirm `RejectProfile`
  tak delete row)
- Notification: `member_approved` (1 row) dan `member_rejected` (1 row)
  tercipta dengan `recipient_id`/`actor_id` tepat. `member_pending`: **0
  row** dalam run ni — sebab ketiga-tiga akaun (`pending-test`,
  `approve-test`, `mgmt-test`) register **sebelum** `mgmt-test` di-promote
  jadi management, jadi tiada recipient management wujud lagi masa
  masing-masing register (fan-out logic betul — ia hantar ke management
  yang wujud *pada masa insert*, bukan retroactive). Bukan bug — dijangka
  dalam brief task ni.
- `member_pending` fan-out disahkan berasingan (2026-08-07, final review):
  register `mgmt-first@example.com` dulu, promote ke management + `approved`
  di DB, baru register `pending-after-mgmt@example.com` — hasil: tepat
  **1 row** `member_pending`, `recipient_id` = user id `mgmt-first`
  (`c316b258-7e77-49e3-bce7-18c592b06fde`). Confirm insert path (nullable
  `post_id` + type baru, lalu `sqlc.CreateNotification`) betul bila
  management wujud pada masa registration.

**Note**: kerja frontend (`marc_flutter/TODO.md` Stage 11) masih pending,
**tak blocked** oleh backend — boleh mula bila-bila.

### Bootstrap management user pertama (baca dulu sebelum deploy)

Registration sentiasa assign role `ahli` (ordinary member) — **tiada**
endpoint untuk promote user ke management. Kalau environment ada 0
management user, setiap pendaftaran baru akan stuck `pending` selama-lamanya
sebab tiada sesiapa boleh approve. Sebelum deploy Stage 11 ke mana-mana
environment:

1. Semak dulu sama ada environment tu dah ada sekurang-kurangnya 1
   management user:
   ```sql
   select count(*) from profiles p join roles r on r.id = p.role_id where r.category = 'management';
   ```
   Kalau pulang 0, promote seorang dulu (langkah 2) sebelum benarkan
   sesiapa register.
2. Promote manual (satu-satunya cara buat masa ni):
   ```sql
   update profiles set role_id = (select id from roles where category = 'management' limit 1)
   where user_id = (select id from users where email = '<email>');
   ```
3. **Tiada** cara in-app/API untuk promote user ke management — known gap,
   bukan dalam skop Stage 11 (design spec dah deferred role `admin`
   berasingan secara eksplisit). Kalau ni jadi kesakitan operational
   sebenar, boleh jadi task untuk stage akan datang:
   - [ ] Endpoint/tooling untuk promote user ke management (role `admin`
     berasingan ke, atau superuser bootstrap script ke) — bincang bila
     jadi keperluan sebenar, bukan sekarang

---

## Security audit (Opus, 2026-08-07) — High + Medium + 9/11 Low fixed

Independent security/bug audit atas backend penuh (bukan diff-scoped —
seluruh `internal/`). Ringkasan: tiada Critical, auth core (bcrypt, token
hashing, ownership check, sqlc parameterization) solid, Stage 11 gate
routing di-verify route-by-route — tiada lubang.

**High (2/2 fixed, commits `6de90ba`, `c8a8347`, `170dae0`):**
- [x] **H1**: Tiada had saiz request body + `router.Run()` tiada timeout
  → memory-exhaustion DoS pada endpoint unauthenticated
  (`/auth/refresh`, `/logout`, `/verify-email/confirm`). Fix:
  `middleware.MaxBodySize` (1MiB, global) + `http.Server` eksplisit
  dengan 4 timeout.
- [x] **H2**: `/uploads/presign` tiada rate limit + tiada rekod siapa
  minta key mana → unbounded R2 storage cost, orphan upload. Fix: table
  `pending_uploads` (r2_key↔user_id), ownership check di `POST /posts`
  sebelum attach, rate limit presign endpoint. Follow-up fix
  (`170dae0`): cleanup pending_uploads row dipindah ke DALAM transaction
  post-creation (asalnya di luar tx — kalau tx rollback lepas cleanup
  jalan, gambar yang sah jadi tak boleh attach lagi, kena re-upload).

**Medium (5/5 fixed, commits `8c8045d`, `2dab964`, `2711ab6`, `e955ad1`,
`4af4562`, `5cf9e5f`, `40c866d`):**
- [x] **M1**: `PATCH /me` full-replace bukan patch — `{}` atau field
  kosong dulu silently NULL-kan `phone`/`display_name` sedia ada. Fix:
  request field jadi `*string` (nil = tak sentuh), query guna
  `coalesce(sqlc.narg(...), col)` — field dihantar (termasuk `""`)
  set terus, field tak dihantar dibiarkan.
- [x] **M2**: Device-token upsert (`on conflict (onesignal_id) do update
  set user_id = ...`) dulu tiada check row tu kepunyaan caller — boleh
  hijack push notification orang lain kalau `onesignal_id` bocor. Fix:
  conflict guard `where device_tokens.user_id = excluded.user_id`,
  `:execrows` + 409 kalau 0 rows (row wujud, kepunyaan user lain).
- [x] **M3+M4**: `setMemberStatus` dulu tiada self-check/rank-check
  (management boleh reject diri sendiri ATAU management lain — reject
  yang terakhir = lockout permanent) DAN tiada state precondition
  (replay hantar email/notification tak terhingga, rosak audit trail).
  Fix: block self-target (approve+reject), block reject kalau target
  category management, query guna `and status <> '<target>'` (replay
  jadi idempotent no-op, bukan resend).
- [x] **M5+M6**: email uniqueness case-sensitive (`Ahmad@` vs `ahmad@` =
  2 akaun) + password >72 byte crash bcrypt dengan 500 generic. Fix:
  normalize email (`ToLower`+`TrimSpace`) di register+login, unique
  index `lower(email)` + backfill row sedia ada (follow-up `4af4562` —
  round pertama terlepas backfill, login user lama jadi locked out kalau
  email tersimpan mixed-case), `max=72` pada password field dua-dua.
- [x] **M7**: refresh token rotation dulu tiada reuse detection — token
  dicuri+ditukar oleh attacker buat user asli senyap-senyap logout
  (bukan alert), chain attacker terus valid. Tiada juga "log out semua
  device". Fix: `family_id` + `consumed_at` (row tak dipadam lagi bila
  consume, kekal untuk detect reuse), replay token yang dah consumed →
  revoke SEMUA token dalam family tu (verified: chain attacker DAN
  session asli sama-sama terputus, paksa re-login). `POST
  /auth/logout-all` baru (RequireAuth sahaja, sama exemption macam
  `/me`). Follow-up (`40c866d`): tambah grace window 5 saat — replay
  DALAM tempoh ni (race/retry concurrent, bukan attack) 401 tapi tak
  revoke family; round pertama terlepas ni, boleh false-positive
  lockout user sah kalau ada concurrent refresh request (client
  `marc_flutter` dah ada dedupe untuk elak ni, tapi bukan client lain).

**Residual diketahui, bukan bug (trade-off sengaja, macam pattern H2's
orphan-sweep gap):**
- `refresh_tokens` sekarang grow tak terhingga (row consumed tak
  dipadam lagi — sengaja, untuk reuse detection). Belum ada cleanup
  job (tiada infra cron/scheduler dalam app ni). Kalau jadi keperluan:
  `delete from refresh_tokens where consumed_at < now() - interval
  '30 days' or expires_at < now()` — TAPI window cleanup MESTI lebih
  panjang dari window reuse-detection, kalau tidak pruning buka balik
  lubang M7.

**Low (9/11 fixed, commits `6909e27`, `2562aee`, `8e2ece5`, `5ff2c15`,
`d49cf5d`, `265c881`, `b7c6935`):**
- [x] **L1**: `r2_key` client-controlled + tak divalidasi masa attach —
  dah resolved sebagai side-effect H2 (`pending_uploads` ownership
  check di `posts.go` Create, commit `c8a8347`), tak perlu kerja baru.
- [x] **L2**: presigned PUT boleh host arbitrary bytes bawah domain R2
  awam (Content-Type di header PUT boleh dipalsukan). Fix:
  `VerifyImageFormat` — semak magic number byte pertama (JPEG/PNG/WEBP)
  guna ranged `GetObject` (bytes=0-11), dipanggil lepas VerifyImageSize.
- [x] **L3+L4**: comment threading tak constrain kat post yang sama
  (`parent_comment_id` boleh rujuk comment post lain); FK violation
  (like/comment kat resource tak wujud) pulang 500 generic bukan 404.
  Fix: `resolveParentCommentID` semak `parent.PostID == postID`; helper
  `isForeignKeyViolation` (code `23503`, padanan `isUniqueViolation`)
  dipakai di 3 tempat (`Like` post, `Like` comment, `CreateComment`).
- [x] **L5**: cursor pagination (`ListPosts`/`ListNotifications`) boleh
  skip row bila timestamp tie. Fix: keyset pagination atas
  `(created_at, id)`, row-value comparison SQL + cursor format baru
  `<rfc3339nano>|<uuid>` (client treat cursor opaque, tak perlu ubah
  Flutter — confirmed semasa fix).
- [x] **L6**: login jadi user-enumeration oracle (timing bcrypt vs
  early-return path tak wujud). Fix: burn bcrypt compare (dummy hash
  tetap) pada path email-tak-wujud jugak, samakan masa response.
- [x] **L7**: dead code `RequireManagement` middleware (tak wired ke
  mana-mana route). Fix: buang terus (fail + rujukan dalam
  `authz.go`/`ARCHITECTURE.md`) — semua check management memang inline
  dalam handler, bukan middleware route-group-level.
- [x] **L8+L9**: tiada rate limit verify-email request/confirm; rejected
  user tak revoke refresh token sedia ada. Fix: `authRateLimiter` sedia
  ada dipakai kat 3 route verify-email; `setMemberStatus` panggil
  `DeleteRefreshTokensByUser` (dari fix M7) bila reject berjaya.
- [ ] **L10**: trusted-proxy config (`100.64.0.0/10` + RFC1918) betul
  untuk Railway sekarang, tapi rapuh kalau topology proxy berubah
  (fronting proxy ganti XFF bukan append) atau scale >1 instance
  (rate limiter in-memory per-process). Bukan bug semasa, catatan
  sahaja untuk masa depan.
- [ ] **L11**: tiada CORS config — okay sekarang (client mobile-only,
  browser tak boleh reach cross-origin). Perlu tambah allowlist
  eksplisit bila landing page verify-email `hafizbahtiar.com`
  (Stage 8) betul-betul call backend ni terus dari browser.

---

## Stage 12 — Payment module (yuran ahli + donation) — Stripe slice ✅, sambungan belum start

Design awal dibincang dengan user (2026-08-09). **Dua sub-sistem berasingan
sepenuhnya**, akaun gateway berbeza, tiada overlap data:

1. **Yuran ahli MAIWP** — akaun **ToyyibPay** rasmi organisasi (non-profit
   MAIWP). Keputusan: jadi **gate akses Posts/feed** — ahli yang belum
   bayar yuran tak boleh akses feed, lapisan gate ketiga selepas
   `email_verified` (Stage 10) dan `status=approved` (Stage 11). Ini
   menyelesaikan nota lama "payment gate akan ditambah bila payment
   system siap" yang disebut sejak Stage 10. **Belum start.**
2. **Donation page ("Donate to us")** — akaun **berasingan** (peribadi,
   untuk pemaju app — aku & kau, bukan MAIWP), threshold **RM500**:
   - Amount ≥ RM500 → **Stripe** ✅ (checkout + webhook backend siap,
     lihat bawah)
   - Amount < RM500 → **SociaBuzz** — belum start, belum research API
   - Currency: **MYR sahaja** — keputusan eksplisit elak isu penukaran
     mata wang. Checkout Stripe tetap dalam RM walaupun donor guna kad
     asing (Stripe caj + settle dalam RM di pihak Stripe sendiri); tiada
     FX API/rate table perlu dibina di backend.
   - **Payment Element custom** (bukan Stripe Checkout hosted page) —
     UI kad dalam app Flutter sendiri (keputusan 2026-08-09, lihat kerja
     Flutter di bawah).
   - **Traceability** (keputusan 2026-08-09): SEMUA donation kena ada
     jejak dalaman, walau anonymous. Guna *optional auth*
     (`middleware.OptionalAuth`) — ahli MARC yang log masuk dikaitkan
     `user_id` automatik (trace balik ke emel akaun); anonymous (tiada
     token) **wajib isi `donor_email`** — endpoint tolak 400 kalau
     kosong. Donation TAK perlu akaun MARC untuk guna (guest boleh
     donate), cuma perlu emel kalau guest.

### Kerja backend — Stripe donation ✅ (done, kini via interface `payment.Gateway`)

Refactor 2026-08-09 (keputusan user, "professional standard" + loose
coupling): payment gateway TAK lagi struct konkrit terus dalam handler —
semua provider (Stripe sekarang, ToyyibPay/SociaBuzz akan datang)
implement satu interface sepunya `payment.Gateway`
(`internal/payment/payment.go`). Strategy pattern — tambah gateway baru
= implement interface + satu baris registry, tiada perubahan
handler/routing sedia ada.

```go
type Gateway interface {
    Name() string
    Enabled() bool
    CreatePayment(ctx, CreateParams) (CreateResult, error)
    VerifyWebhook(payload []byte, headers http.Header) (WebhookEvent, error)
}
```

`CreateResult{GatewayRef, ClientSecret, RedirectURL}` sengaja union
type — HANYA SATU antara `ClientSecret`/`RedirectURL` diisi ikut model
gateway (Stripe = client-side confirm, isi `ClientSecret`; ToyyibPay/
SociaBuzz akan datang = hosted-redirect page, isi `RedirectURL`).
Response `/donations/checkout` pulangkan kedua-dua field (kosong kalau
tak relevan) + `gateway` — client tengok field mana terisi untuk tentukan
flow, tak perlu hardcode andaian Stripe.

- [x] `internal/payment/payment.go` — interface `Gateway` + types
  (`CreateParams`, `CreateResult`, `WebhookEvent`, `ErrNotConfigured`,
  `ErrIgnoredEvent`)
- [x] `internal/payment/stripe.go` — `StripeGateway` implements `Gateway`
  (`stripe-go/v82`, API client-based bukan global `stripe.Key`)
- [x] `internal/config/config.go`: `StripeSecretKey`, `StripePublishableKey`,
  `StripeWebhookSecret` (optional — kosong = gateway disabled, 503
  graceful, sama pattern R2)
- [x] Migration `donations`: `id, user_id (nullable, FK users), donor_name,
  donor_email, amount_cents, currency, gateway ('stripe'|'sociabuzz'),
  gateway_ref, status ('pending'|'succeeded'|'failed'), created_at`.
  Constraint `donations_traceable`: `user_id is not null or donor_email
  is not null` (kuatkuasa traceability di peringkat DB, bukan cuma app
  code). Unique index `(gateway, gateway_ref)` untuk webhook idempotency.
- [x] `middleware.OptionalAuth` + `UserIDOptional` — parse Bearer token
  kalau ada, TAK abort kalau tiada/tak sah (padanan `RequireAuth` tapi
  untuk route awam yang benarkan anonymous)
- [x] `handlers/donations.go` — `DonationHandler{gateways
  map[string]payment.Gateway}`, TAK bergantung Stripe terus:
  - `POST /donations/checkout` (awam, `OptionalAuth` + rate limit
    5/6s) — `selectGateway(amountCents)` (buat masa ni selalu pulang
    Stripe; threshold RM500 vs SociaBuzz masuk SATU tempat ni bila siap),
    validate `amount_cents` (RM1–RM50k pagar munasabah), block anonymous
    tanpa `donor_email`, panggil `gw.CreatePayment(...)` + rekod
    `donations` status `pending`
  - `POST /webhooks/:gateway` (awam, tiada rate limit — gateway boleh
    retry) — lookup `gateways[gateway]`, `gw.VerifyWebhook(payload,
    headers)` (signature/format spesifik kekal dlm implementation
    masing-masing), `ErrIgnoredEvent` → 200 OK senyap (elak retry-loop
    gateway), lain → update `donations` guna `(gateway, gateway_ref)`
- [x] `main.go`: registry `paymentGateways :=
  map[string]payment.Gateway{"stripe": payment.NewStripeGateway(...)}`
  — daftar ToyyibPay/SociaBuzz sini bila siap, satu baris setiap satu
- [x] Resit emel (2026-08-09) — `DonationHandler.sendReceiptEmail`
  dipanggil dalam `Webhook` lepas transisi **sebenar** (bukan replay) ke
  status `succeeded`: guna `donor_email` kalau ada, kalau tidak lookup
  profil (emel + member_id) via `GetProfileByUserID(donation.UserID)`.
  Best-effort (log je kalau hantar gagal, tak gagalkan webhook). Guna
  sifat idempotent `UpdateDonationStatusByGatewayRef` (WHERE `status <>
  'succeeded'`) — retry/replay webhook Stripe kena `pgx.ErrNoRows`
  sebab row dah `succeeded`, jadi TAK trigger cabang emel semula
  (resit hantar tepat SEKALI, bukan setiap retry).
- [x] **Resit PDF + emel bertema (2026-08-09)** — `internal/receipt/receipt.go`
  (`go-pdf/fpdf`) jana PDF satu muka: header jenama, jumlah besar, jadual
  (no. rujukan, tarikh, nama, emel, no. ahli kalau berkaitan), footer nota.
  `internal/email/client.go` tambah `SendWithAttachments` (Resend terima
  attachment sbg base64 dlm body JSON, bukan multipart) — `Send` lama
  kekal, delegate ke variant baru dgn `attachments=nil`. HTML emel
  (`donationReceiptHTML`) guna inline style SAHAJA (bukan `<style>`
  block/class — byk email client buang tu), padanan warna jenama
  (`#2F6B4F`/`#FAF9F6`/`#1C1B19`/`#6B6B6B`, sama macam `AppColors` di
  `marc_flutter/lib/app/theme.dart`). Nama donor (user input) di-escape
  (`html.EscapeString`) sebelum masuk HTML — elak injection vector.
  Verified: `go test` smoke check PDF keluar fail valid (`file` confirm
  "PDF document, version 1.3"), toolkit build/vet/lint bersih.

**Insiden webhook (2026-08-08/09) — punca sebenar bukan secret**: siri
403/400 berpanjangan yang nampak macam isu `STRIPE_WEBHOOK_SECRET` (dan
punca delete/recreate endpoint berkali-kali) sebenarnya
**`webhook.ConstructEvent` (`stripe-go`) tolak event bila API version
account (`2025-10-29.clover`) tak padan API version SDK dipin
(`2025-08-27.basil`)** — signature SAH sepanjang masa, cuma version
check yang gagal, tapi error message generic "signature tidak sah"
buat ia nampak macam isu secret. Fix: `internal/payment/stripe.go` guna
`webhook.ConstructEventWithOptions{IgnoreAPIVersionMismatch: true}`
(selamat — handler cuma baca `event.Type`/`PaymentIntent.ID`, stabil
merentasi API version). `donations.go` webhook 400-path sekarang log
ralat SEBENAR drpd `VerifyWebhook`, bukan cuma balas 400 senyap — elak
kelas bug ni menyamar sbg isu secret lagi. Verified end-to-end dgn
donation sebenar (checkout → confirm via Stripe API → webhook 200 →
row DB `succeeded` → resit emel hantar).

**Verified**: `go build`/`go vet`/`golangci-lint run` bersih; migration
`up` jalan bersih lawan Postgres lokal; payment sebenar end-to-end kini
**verified** lawan staging (lihat insiden di atas) — checkout, webhook
signature verify, status update, DAN resit emel semua confirmed jalan
betul dgn PaymentIntent sebenar (bukan andaian/mock).

### Draf data model — ToyyibPay yuran (belum migration, tunggu soalan bawah)
```
profiles          + dues_status ('unpaid'|'paid'), dues_paid_until (nullable,
                  kalau yuran berulang — lihat soalan belum putus)
dues_payments     id, user_id, amount, toyyibpay_bill_code, status, paid_at, created_at
```

### Draf API endpoints — ToyyibPay yuran + SociaBuzz (belum implement)
```
POST /dues/checkout           -- create ToyyibPay bill, pulang payment URL
POST /webhooks/toyyibpay      -- callback ToyyibPay (unauthenticated, verify signature)
GET  /dues/status              -- status yuran ahli semasa (untuk gate + UI profil)

POST /webhooks/sociabuzz       -- callback SociaBuzz (kalau gateway ni ada webhook rasmi)
```
(`POST /donations/checkout` dah wujud — bila SociaBuzz siap, endpoint ni
kena diubah untuk route ikut threshold RM500, bukan Stripe terus)

### Belum putus / perlu clarify sebelum sambung
- [ ] Yuran ToyyibPay: **sekali bayar** ke **berulang** (tahunan/bulanan)?
  Kalau berulang — macam mana renewal reminder + grace period sebelum
  lock akses balik (elak ahli terus ter-lock tanpa notis)?
- [ ] Yuran ToyyibPay: bila exactly gate ni start berkuat kuasa untuk
  ahli baru — terus lepas `status=approved`, atau ada tempoh percubaan
  free dulu?
- [ ] Donation page: letak dalam app MARC (tab/menu baru) atau landing
  page luar di `hafizbahtiar.com` (pattern sama macam verify-email
  Stage 8, cross-repo dgn `portfolio-astro`)?
- [ ] Credential provisioning: `STRIPE_SECRET_KEY`/`STRIPE_PUBLISHABLE_KEY`/
  `STRIPE_WEBHOOK_SECRET` (test mode dulu) kena diisi `.env` sebelum
  boleh test end-to-end sebenar. ToyyibPay + SociaBuzz punya credential
  pun sama — perlu provisioning dulu bila sampai giliran.
- [ ] SociaBuzz: belum research — ada API/webhook rasmi untuk verify
  payment status secara programatik, atau perlu reconcile manual?
- [ ] Threshold RM500 routing (`/donations/checkout` pilih Stripe vs
  SociaBuzz ikut amount) — belum implement, tunggu SociaBuzz siap dulu.
  Buat masa ni endpoint Stripe SAHAJA, tiada pengecualian amount <RM500.

**Status**: Stripe donation checkout + webhook backend **done**, belum
verified lawan Stripe sebenar (tunggu credential). ToyyibPay + SociaBuzz
+ Flutter UI semua **belum start**.

---

## Stage 13 — Jejak audit (audit_logs) ✅ backend siap

Satu jadual generik `audit_logs` untuk semua entiti yang boleh diubah.
Migration `20260809140000_create_audit_logs.sql`, package `internal/audit`.

Prinsip:
- **Delta sahaja** untuk `update` — cuma field yang berubah disimpan dalam
  `old_values`/`new_values` (jsonb), `changed_fields` untuk tapisan murah.
  `create` → old null; `delete` → snapshot penuh dalam old.
- **Dalam transaksi yang sama** dengan mutasi (`queries.WithTx(tx)`).
  Catatan gagal = keseluruhan permintaan gagal. Jejak best-effort yang
  boleh hilang senyap bukan jejak.
- **Append-only di peringkat DB** — trigger tolak UPDATE. DELETE dibenarkan
  supaya pruning simpanan mungkin.
- Actor snapshot `member_id` + `role_key` sebagai teks: role berubah, kita
  nak tahu kuasa yang dia ADA masa tindakan itu.

Sudah dipasang: post update/delete, comment update/delete, tukar role ahli.
Baca: `GET /audit-logs` (management sahaja) — tapis ikut
`entity_type`/`entity_id`/`action`/`actor_id`, pagination keyset `before_id`.

Belum:
- [ ] Polisi simpanan + jadual pruning (`DeleteAuditLogsBefore` dah ada tapi
      tak dipanggil dari mana-mana — sengaja, tunggu polisi diputuskan).
      `ip_address`/`user_agent` ialah data peribadi (PDPA).
- [ ] UI Flutter untuk baca jejak (endpoint dah sedia)
- [ ] Audit untuk approve/reject ahli — nilai tinggi, belum dipasang
- [ ] Audit untuk `create` post/comment (volum tinggi, faedah rendah sebab
      entiti sendiri dah simpan author + created_at) — putuskan dulu

## Belum putus / perlu bincang lagi
- R2 API token permission scope (403 AccessDenied bila upload sebenar,
  walaupun presign + checksum dah betul) — perlu kau semak Cloudflare
  dashboard, bukan sesuatu code boleh fix
- `R2_PUBLIC_URL` belum diisi
