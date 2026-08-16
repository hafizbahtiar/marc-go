# Architecture

## Struktur direktori

```
cmd/api/main.go              — entrypoint: load env → migrate → connect DB →
                               start reaper + retention → start Gin
internal/
  config/                    — load & validate env vars (termasuk polisi simpanan)
  db/
    db.go                    — pgxpool + goose migration runner (embedded)
    migrations/              — fail migration goose (single-file Up/Down)
    sqlc/                    — kod generated sqlc (JANGAN edit terus)
  auth/                      — password hashing, JWT, opaque token generation
  authz/                     — RBAC helper (IsManagement) + pattern ownership
  audit/                     — jejak "siapa ubah apa" (Diff + Record)
  email/                     — Resend client (termasuk lampiran base64)
  onesignal/                 — OneSignal REST API client
  push/                      — device_tokens + onesignal (NotifyUser)
  payment/                   — interface Gateway + StripeGateway
  receipt/                   — jana PDF resit donation (go-pdf/fpdf)
  certificate/               — kelayakan sijil + jana PDF sijil (go-pdf/fpdf
                               + QR). TULEN: tiada DB, tiada rangkaian,
                               tiada filesystem
  storage/                   — Cloudflare R2: presign, verify, delete
  reaper/                    — pembersih objek R2 yatim (background)
  retention/                 — polisi simpanan data (background)
  http/
    router.go                — wiring semua route + lapisan middleware
    handlers/                — satu fail per domain
    middleware/              — RequireAuth, OptionalAuth, RequireApprovedStatus,
                               RequireVerifiedEmail, RateLimit, RequestLogger
queries/                     — SQL sumber untuk sqlc (input, bukan output)
```

## Lapisan akses

Route dikumpul ikut apa yang dituntut, bukan ikut domain:

```
r            → awam (healthz, login, register, donations/checkout, webhooks)
protected    → RequireAuth                     (/me — sengaja setakat ni sahaja)
approved     → + RequireApprovedStatus         (members, roles, audit-logs, device-tokens)
verified     → + RequireVerifiedEmail          (posts, comments, uploads, notifications)
```

`/me` sengaja berhenti di `protected`: user berstatus pending/rejected MESTI
boleh baca status sendiri, kalau tidak app tak dapat papar skrin yang betul
dan mereka terperangkap dalam ralat generik.

## Alir request (contoh: `GET /me`)

```
Client  │ Authorization: Bearer <access_token>
        ▼
Gin router (router.go)
        ▼
middleware.RequireAuth      — verify JWT (HS256), set user id ke gin.Context
        ▼
handlers.ProfileHandler.Me
        ▼
sqlc.Queries.GetProfileByUserID(ctx, userID)   — di-scope guna userID dari
        │                                        JWT, BUKAN dari URL/body
        ▼
pgxpool → Postgres → JSON
```

## Auth: JWT access + opaque refresh (rotated)

- **Access token**: JWT (HS256), TTL 15 minit, `sub` = user id. Stateless.
- **Refresh token**: random opaque 32 bait, disimpan sebagai **SHA-256 hash**
  (kalau DB bocor, token asal tak boleh dipakai).
- **Rotation, single-use, atomic**: `POST /auth/refresh` guna SATU statement
  `DELETE ... RETURNING`, bukan `SELECT` diikuti `DELETE`. Sengaja: kalau dua
  request refresh serentak bawa token sama, row-level lock Postgres jamin
  hanya SATU dapat row; yang lain dapat 0 rows → 401 bersih. Reka bentuk
  get-then-delete ada gap TOCTOU yang membenarkan KEDUA-DUANYA berjaya.

## Authorization — gantian Postgres RLS

Tiada RLS lagi (lihat `TODO.md` Stage 9). Semua enforcement app-level:

1. **Role check** (`authz.IsManagement`) dipanggil inline dalam handler,
   bukan middleware peringkat-route — kebanyakan check perlu tahu SIAPA
   target dulu (cth `setMemberStatus`), bukan sekadar "route ni
   management-only".
2. **Ownership** — tiada helper generic. Handler WAJIB scope query guna
   `middleware.UserID(c)`, **tak pernah** terima "resource siapa" daripada
   URL/body.
3. **Keterlihatan ahli** ikut hierarki `roles.rank` — lihat
   `visibleRankCeiling`. Peraturan: nampak sehingga satu tingkat di atas
   rank sendiri, dan rank tertinggi (superadmin) tak pernah didedahkan.
   Ditapis dalam SQL, jadi baris yang tak layak tak pernah keluar dari DB.
   Emel ahli LAIN cuma didedahkan kepada management.

## Jejak audit (`internal/audit`)

Satu jadual generik untuk semua entiti. Tiga keputusan yang saling bergantung:

- **Delta sahaja** untuk `update` — hanya field yang berubah disimpan.
  `delete` simpan snapshot penuh (kandungan tak dapat dibaca semula selepas
  itu).
- **Dalam transaksi yang sama** dengan mutasi (`queries.WithTx(tx)`). Jejak
  best-effort yang boleh gagal senyap bukan jejak: kes paling penting untuk
  direkod ialah kes paling mungkin gagal ditulis.
- **Append-only dikuatkuasakan DB.** Trigger tolak semua UPDATE kecuali satu
  bentuk: meredaksi `ip_address`/`user_agent` ke NULL (keperluan PDPA —
  lihat `retention`). DELETE dibenarkan untuk pruning.

Dipasang pada: post update/delete, comment update/delete, tukar role,
approve/reject ahli.

## Kerja latar

Dua goroutine, kedua-duanya selamat kalau proses terbunuh (kerja berasaskan
gilir/umur, disambung semula pada boot):

- **`reaper`** (15 minit) — objek R2 bocor selamanya sebelum ni. Dua punca:
  post dipadam (soft delete, jadi `post_images` kekal) dan karangan post
  ditinggalkan (gambar naik ke R2 sebaik dipilih). Kunci digilir dalam
  `deleted_uploads`, dipadam dengan backoff. Batu nisan (`deleted_at`)
  dikekalkan supaya penyapu yatim tak menggilir semula kunci yang sama.
- **`retention`** (harian) — tiga sapuan dengan tempoh BERBEZA. PII audit
  (ip/user_agent) diredaksi pada 90 hari; catatan audit dipadam pada 365;
  batu nisan upload dibuang pada 30. Redaksi ≠ padam: "siapa naikkan pangkat
  siapa" berbaloi disimpan lama, "dari IP mana" tidak.

## Payment (`internal/payment`)

Interface `Gateway` (Name/Enabled/CreatePayment/VerifyWebhook) dengan
`CreateResult` sebagai union: `ClientSecret` XOR `RedirectURL`. Stripe guna
yang pertama, gateway hosted-redirect (ToyyibPay/SociaBuzz) akan guna yang
kedua. Registry `map[string]payment.Gateway` di `main.go` — tambah gateway =
satu baris, handler tak berubah. Bentuk yang sama dicerminkan di Flutter
(`DonationCheckoutHandler`).

Gotcha webhook: `webhook.ConstructEvent` menguatkuasakan keserasian versi API
**berasingan** daripada tandatangan, dan ralatnya tak dapat dibezakan
daripada rahsia yang salah. Guna `ConstructEventWithOptions` dengan
`IgnoreAPIVersionMismatch: true` — selamat di sini sebab handler cuma baca
`event.Type` dan `PaymentIntent.ID`.

## Storan (`internal/storage`)

Client upload **terus ke R2** guna presigned URL — backend tak pernah sentuh
bait gambar (elak jadi bottleneck bandwidth). Backend cuma jana URL, dan
selepas itu sahkan format fail melalui `GetObject` julat 12 bait.

Gotcha: `RequestChecksumCalculation` mesti `WhenRequired`. Default
aws-sdk-go-v2 menambah checksum CRC32 pada setiap request termasuk presigned
URL, yang R2 tak serasi — tandatangan jadi tak sah dan PUT client dapat 403.

## Sijil (`internal/certificate`)

Modul **tulen**: tiada DB, tiada rangkaian, tiada filesystem — sama bentuk
dengan `internal/receipt`. Ia terima nilai dan pulangkan `[]byte`
(atau `bool` untuk kelayakan). Itulah sebabnya seluruh logik sijil
— pengiraan kelayakan DAN penjanaan PDF — diuji tanpa Postgres, tanpa R2,
dan tanpa env — ujiannya benar-benar berjalan dalam CI, tidak seperti
ujian live handler dan storan yang langkau di sana (lihat `TODO.md`,
bahagian Ujian).

Dua bahagian:

- `eligibility.go` — `IsEligible` (kehadiran vs `attendance_threshold_pct`)
  dan `CheckinWindowPadding`. Perbandingan ambang dibuat dalam **integer**
  (`attended*100 >= total*threshold`), bukan float, supaya kes sempadan
  seperti 2/3 pada ambang 66 lwn 67 berkelakuan sama pada setiap platform.
- `certificate.go` — susun atur A4 landskap (`go-pdf/fpdf`) + QR
  pengesahan (`skip2/go-qrcode`).

Gotcha `fpdf`: penterjemah cp1252 menggantikan rune yang tak dipeta dengan
`.` **secara senyap** — nama ahli boleh dicetak rosak tanpa sebarang
ralat. Jadi keempat-empat medan teks (serial, nama penerima, tajuk
aktiviti, kategori) disemak dahulu terhadap penterjemah SEBENAR (round-trip,
bukan cutoff `r > 0xFF`, yang akan menolak `’ – € •` yang sah), dan medan
yang gagal menamakan dirinya dalam ralat. `VerifyURL` dikecualikan dengan
sengaja: ia hanya masuk ke `qrcode.Encode`, tak pernah ke penterjemah.
Teks lebar-berubah dipotong dengan `clip()` yang disalin daripada
`receipt.go` — tanpanya teks panjang melimpah keluar bingkai.

Penerbitan sijil sendiri (baca DB, jana, naik R2, tulis `r2_key`) hidup
dalam `handlers/activity_certificates.go`, dua fasa dan boleh disambung
semula. Sengaja BUKAN di sini — sebaik modul ni menyentuh DB, ia hilang
sifat yang membuatnya berguna.

## Config / env

`internal/config/config.go`. Wajib: `DATABASE_URL`, `JWT_SECRET`. Selebihnya
optional dengan no-op senyap (OneSignal, Resend) atau 503 yang jelas (R2,
Stripe) — app sentiasa boot. Polisi simpanan boleh diubah via env tanpa
deploy semula, sebab ia keputusan POLISI bukan teknikal.

## Kenapa sqlc + goose (bukan ORM)

Keputusan awal: user biasa dengan Drizzle (node). sqlc paling dekat dengan
rasa itu di Go — tulis SQL raw, generate types, bukan query builder magic.
goose dipilih atas `golang-migrate` sebab format satu-fail + naming timestamp.

## Deployment

Railway, projek `marc`, environment `staging` + `production`. Tiada Docker.
Migration auto-apply on boot melalui goose embedded.
