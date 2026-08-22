# Architecture

Rujukan HIDUP — ia menerangkan kod seperti ia SEKARANG, termasuk
kelemahan yang diketahui. Blok ⚠️ menandakan tempat yang kodnya tidak
sepadan dengan niatnya; setiap satu memaut ke item `TODO.md` yang
menjejakinya.

Kerja belum siap: [`TODO.md`](./TODO.md).
Schema & migration: [`DATABASE.md`](./DATABASE.md).
Spec, plan, laporan audit: [`docs/`](./docs/).

## Struktur direktori

```
cmd/api/main.go              — entrypoint: load env → migrate → connect DB →
                               connect Redis (pilihan) → start 5 kerja latar →
                               start Gin
internal/
  config/                    — load & validate env vars (termasuk polisi simpanan)
  db/
    db.go                    — pgxpool + goose migration runner (embedded)
    migrations/              — fail migration goose (single-file Up/Down)
    sqlc/                    — kod generated sqlc (JANGAN edit terus)
  auth/                      — password hashing, JWT, opaque token generation
  authz/                     — RBAC helper (IsManagement, IsAtLeastRole) +
                               pattern ownership
  audit/                     — jejak "siapa ubah apa" (Diff + Record)
  email/                     — Resend client (termasuk lampiran base64)
  onesignal/                 — OneSignal REST API client
  push/                      — device_tokens + onesignal (NotifyUser)
  redisclient/               — sambungan Redis kongsi (PILIHAN — no-op bila
                               REDIS_URL kosong) + URLCache teragih
  phone/                     — normalisasi nombor telefon, satu fungsi setiap
                               negara (NormalizeMY sahaja buat masa ni)
  disposableemail/           — senarai domain emel pelupusan terbenam
                               (domains.txt) + allowlist akaun tester
  payment/                   — interface Gateway + StripeGateway +
                               ToyyibPayGateway
  paymentlog/                — log PERISTIWA bayaran merentas 3 modul
                               (append-only, asas diagnosis + reconcile)
  paymentreconcile/          — semak semula bayaran 'pending' TERUS pada
                               gateway, betulkan DB (background)
  receipt/                   — jana PDF resit donation + yuran (go-pdf/fpdf)
  certificate/               — kelayakan sijil + jana PDF sijil (go-pdf/fpdf
                               + QR). TULEN: tiada DB, tiada rangkaian,
                               tiada filesystem
  storage/                   — Cloudflare R2: presign, verify, put, delete,
                               SignedURL + cache kestabilan URL
  reaper/                    — pembersih objek R2 yatim (background)
  retention/                 — polisi simpanan data (background)
  activitysweep/             — batal pendaftaran aktiviti berbayar yang
                               ditinggalkan, bebaskan slot (background)
  activitylifecycle/         — peringatan H-1 + auto-complete aktiviti
                               tamat (background)
  http/
    router.go                — wiring semua route + lapisan middleware
    handlers/                — satu fail per domain
    middleware/              — RequireAuth, OptionalAuth, RequireApprovedStatus,
                               RequireVerifiedEmail, BlockTesterWrites,
                               RateLimit, CORS, MaxBodySize, RequestLogger
queries/                     — SQL sumber untuk sqlc (input, bukan output)
```

## Lapisan akses

Route dikumpul ikut apa yang dituntut, bukan ikut domain:

```
r            → awam (healthz, login, register, donations/checkout, webhooks,
                     halaman return ToyyibPay, pengesahan sijil awam)
protected    → RequireAuth                     (/me, sejarah+resit bayaran
                                                sendiri, checkout yuran
                                                pendaftaran, deletion-request)
approved     → + RequireApprovedStatus         (members, roles, audit-logs,
                                                device-tokens, baca aktiviti,
                                                sijil sendiri, /admin/*)
verified     → + RequireVerifiedEmail          (posts, comments, uploads,
                                                notifications, tulis aktiviti,
                                                daftar aktiviti, kehadiran,
                                                terbit/tarik sijil)
```

`/me` sengaja berhenti di `protected`: user berstatus pending/rejected MESTI
boleh baca status sendiri, kalau tidak app tak dapat papar skrin yang betul
dan mereka terperangkap dalam ralat generik.

Atas sebab yang **sama**, tiga kumpulan route bayaran turut duduk di
`protected` dan bukan `approved`: `/me/payments`, ketiga-tiga laluan resit,
dan `POST /registration-payments/checkout`. Ahli `pending` yang belum
diluluskan mesti boleh **bayar** dan **lihat bukti bayaran sendiri** semasa
menunggu — kalau tidak mereka tak pernah sampai ke gate yang menjadikan
mereka `approved`.

`approved` (bukan grup ketat baharu) juga jadi tempat `/admin/*` —
`/admin/blocked-email-domains`, `/admin/payments`,
`/admin/payments/reconcile`. Siling sebenar (management, atau superadmin
untuk domain emel dan data derma) dikuatkuasakan **dalam handler**, bukan
pada grup route — padanan corak `/audit-logs`. Sebabnya sama seperti
`authz` di bawah: kebanyakan semakan perlu tahu SIAPA target dahulu.

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

### Reset kata laluan

Token legap 32 bait, disimpan sebagai hash SHA-256 dalam
`password_reset_tokens`, TTL 1 jam. Ahli menaip kata laluan baharu pada
halaman `marc_astro` — tiada app-link https dikonfigur, jadi pautan emel
membuka pelayar, bukan app.

Tiga sifat yang saling bergantung, kesemuanya dalam satu transaksi:

- **Sekali-guna, atomik** — sama macam `/auth/refresh`, klaim token guna
  SATU statement `DELETE ... RETURNING` sebagai statement PERTAMA dalam
  transaksi. Sebab itulah dua permintaan serentak bawa token sama tak
  boleh kedua-duanya berjaya — hanya SATU dapat row terkunci, yang lain
  dapat 0 rows.
- **Permintaan baharu membunuh yang lama** — kalau tidak setiap
  permintaan menambah satu lagi kelayakan hidup pada akaun yang sama.
- **Setiap sesi dibatalkan** — orang reset selalunya kerana syak akaun
  dikompromi; membiarkan refresh token penyerang hidup mengalahkan
  tujuannya.

  Tepatnya: setiap **refresh token**, bukan setiap sesi. Access token ialah
  JWT tanpa keadaan, jadi yang sudah dikeluarkan kekal sah sehingga TTL 15
  minitnya tamat — tiada senarai hitam. Untuk ciri yang justifikasinya
  "akaun disyaki dikompromi", tetingkap itu patut diketahui dan bukan
  dianggap sifar. Sama untuk `POST /auth/logout-all`.

`request` pulang **204 sentiasa** (bukan-enumerasi) dan menghantar emel
dalam goroutine supaya masa respons tak membocorkan kewujudan akaun —
mitigasi separa; lihat komennya. Ia TIDAK menanda `email_verified`.

`PASSWORD_RESET_URL` kosong = ciri dimatikan (503), bukan fallback HTML Go.

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

**Lima** goroutine, semuanya selamat kalau proses terbunuh (kerja berasaskan
gilir/umur/guard-lajur, disambung semula pada boot). Semua ikut bentuk yang
sama — `New` / `Start(ctx)` / `RunOnce(ctx)` — dengan satu pusingan
dijalankan sebaik boot sebelum ticker bermula, supaya proses yang baru mati
tak perlu tunggu satu interval penuh.

Tiada kunci teragih pada mana-mana: setiap sapuan sama ada idempoten
(padam R2 idempoten ikut kunci) atau dilindungi guard lajur dalam UPDATE
itu sendiri (`reminder_sent_at is null`, `status <> 'cancelled'`,
`payment_status <> 'paid'`), jadi dua replika yang jalan serentak
menghasilkan keputusan yang sama tanpa penyelarasan.

- **`reaper`** (15 minit) — objek R2 bocor selamanya sebelum ni. Dua punca:
  post dipadam (soft delete, jadi `post_images` kekal) dan karangan post
  ditinggalkan (gambar naik ke R2 sebaik dipilih). Kunci digilir dalam
  `deleted_uploads`, dipadam dengan backoff. Batu nisan (`deleted_at`)
  dikekalkan supaya penyapu yatim tak menggilir semula kunci yang sama.
- **`retention`** (harian) — **empat** sapuan dengan tempoh BERBEZA. PII
  audit (ip/user_agent) diredaksi pada 90 hari; catatan audit dipadam pada
  365; batu nisan upload dibuang pada 30; `payment_logs` dipadam pada 90.
  Redaksi ≠ padam: "siapa naikkan pangkat siapa" berbaloi disimpan lama,
  "dari IP mana" tidak. Semua tempoh boleh diubah via env tanpa deploy
  semula — ia keputusan POLISI, bukan teknikal.
- **`activitysweep`** (15 minit) — batalkan pendaftaran aktiviti berbayar
  yang ditinggalkan, bebaskan slot kapasiti yang tersilap dipegang. DUA
  cutoff yang sengaja jauh berbeza: 45 minit untuk yang tak pernah cuba
  checkout (`payment_ref is null` — tiada bil wujud, jadi tiada webhook
  akan datang), 24 JAM untuk yang bilnya dah dicipta (FPX/bank boleh ambil
  berjam-jam; batal awal bermakna ahli bayar ke bil yang slotnya dah
  hilang).
- **`activitylifecycle`** (1 jam) — peringatan H-1 (sekali sahaja, guard
  `reminder_sent_at`) dan auto-complete aktiviti yang `ends_at` dah lepas.
  `reminder_sent_at` ditanda **SEBELUM** push dihantar, bukan selepas:
  satu aktiviti terlepas sebahagian penerima lebih selamat daripada N
  replika yang masing-masing cuba semula dan membanjiri ahli.
- **`paymentreconcile`** (30 minit) — semak bayaran `pending` lapuk
  (>15 minit) TERUS pada gateway dan betulkan DB automatik. Wujud sebab
  webhook ToyyibPay pernah gagal senyap beberapa kali: DB boleh tersasar
  daripada kebenaran gateway kalau webhook tak pernah tiba. **Gateway
  ialah sumber kebenaran.** Turut boleh dicetus manual melalui
  `POST /admin/payments/reconcile` — instance yang SAMA, logik yang sama,
  cuma on-demand.

> ⚠️ `paymentreconcile` mempunyai had umur **bawah** sahaja — baris yang
> ditinggalkan selamanya kekal dalam senarai semakan, jadi bebanannya
> membesar secara monotonik. Lihat `TODO.md` **L30**.

## Payment (`internal/payment`)

Interface `Gateway` (Name/Enabled/CreatePayment/VerifyWebhook/CheckStatus)
dengan `CreateResult` sebagai union: `ClientSecret` XOR `RedirectURL`.
Stripe guna yang pertama, gateway hosted-redirect (ToyyibPay) guna yang
kedua. Registry `map[string]payment.Gateway` di `main.go` — tambah gateway =
satu baris, handler tak berubah. Bentuk yang sama dicerminkan di Flutter
(`DonationCheckoutHandler`).

`CheckStatus` ialah tambahan yang membolehkan `paymentreconcile`: ia tanya
status SEBENAR satu bayaran terus pada gateway, bukan daripada webhook atau
DB tempatan.

### TIGA modul bayaran yang berasingan — jangan keliru

| Modul | Gateway | Jadual | Handler |
|---|---|---|---|
| Derma (sokongan pembangun) | `stripe` | `donations` | `donations.go` |
| Yuran pendaftaran ahli (SEKALI bayar) | `toyyibpay` | `registration_payments` | `registration_payment.go` |
| Yuran AKTIVITI (`activities.fee_cents`) | `toyyibpay-activity` | `activity_registrations` (lajur `payment_ref`/`payment_status`/`fee_cents_paid`) | `activity_registration_payment.go` |

Yuran aktiviti **tidak** ada jadual bayaran sendiri: lajurnya duduk terus
atas `activity_registrations` sebab kelayakan sijil
(`queries/activity_certificates.sql`) membaca `payment_status` terus.

**Kenapa DUA instance ToyyibPay dengan kredential yang SAMA:**
`NewToyyibPayGateway` membakar `callbackURL`/`returnURL` **tetap** semasa
dibina. Bil yang dicipta guna instance `"toyyibpay"` akan sentiasa callback
ke `/registration-payments/webhook/toyyibpay`, tak kira route mana yang
mencipta bil itu. Instance kedua ialah satu-satunya cara ToyyibPay
benar-benar memanggil `/activity-registrations/webhook/toyyibpay`. Nota:
kedua-duanya pulang `Name() == "toyyibpay"` — pemilihan instance untuk
reconcile aktiviti dibuat melalui kunci peta literal, bukan `Name()`.

### Gotcha setiap gateway

**Stripe** — `webhook.ConstructEvent` menguatkuasakan keserasian versi API
**berasingan** daripada tandatangan, dan ralatnya tak dapat dibezakan
daripada rahsia yang salah. Guna `ConstructEventWithOptions` dengan
`IgnoreAPIVersionMismatch: true` — selamat di sini sebab handler cuma baca
`event.Type` dan `PaymentIntent.ID`. Guard berasingan menolak
`STRIPE_WEBHOOK_SECRET` kosong: stripe-go TIDAK menolaknya sendiri, jadi
tanpa guard itu tandatangan dikira HMAC dengan kunci KOSONG dan sesiapa
boleh tandakan derma sebagai berjaya.

**ToyyibPay** — tiada pengesahan kriptografi callback yang boleh
dipercayai. Jadi `VerifyWebhook` mengambil **`billcode` sahaja** daripada
body, lalu mengesahkan status dengan poll `getBillTransactions` guna
`userSecretKey` (kredential sisi pelayan — penghantar callback tak boleh
palsukan). Akibatnya `VerifyWebhook` ToyyibPay **membuat panggilan
rangkaian keluar** dan bukan tulen/tempatan seperti Stripe.

Body callbacknya sendiri dah salah diandaikan tiga kali semasa
pembangunan (`;` mentah menolak `url.ParseQuery` keseluruhan; `%` tak sah
buat perkara sama; `"No data found!"` ialah teks biasa, bukan `[]` JSON) —
sebab itu `extractBillCode` mencuba beberapa bentuk secara berurutan dan
mengabaikan ralat separa `ParseQuery`.

### Idempotensi bayaran

Setiap laluan status bergantung pada guard dalam UPDATE, bukan semakan
baca-dahulu:

- `registration_payments` / `donations`: `where … and status <> 'succeeded'`
  — `succeeded` ialah keadaan TERMINAL, jadi webhook retry/tak-ikut-turutan
  tak boleh menurunkannya ke `failed`. `failed → succeeded` masih boleh
  (percubaan semula atas ref yang sama).
- `activity_registrations`: `where payment_ref = $1 and payment_status <>
  'paid'`. **Sengaja TIADA** `and status <> 'cancelled'`: kalau sapuan dah
  membatalkan baris sebelum webhook lewat tiba, UPDATE tetap menandakan
  `paid` atas baris `cancelled` supaya keadaan ganjil itu **kelihatan**
  (handler log ERROR + `paymentlog` `StatusMismatch`) dan bukan hilang
  senyap. Ia memerlukan campur tangan manual.
- `err == nil` selepas UPDATE bermakna baris BENAR-BENAR beralih (bukan
  replay) — itu yang menjadikan resit derma dihantar TEPAT SEKALI.

### Susunan tulis checkout — baris DB SEBELUM bil gateway

Ketiga-tiga modul menulis baris bayarannya **sebelum** memanggil gateway:

```
1. INSERT baris 'pending' (tanpa gateway_ref)
2. CreatePayment / createBill
3. UPDATE isi gateway_ref
```

Susunan ni bukan gaya, ia invarian. Terbalik, kegagalan pada langkah
INSERT meninggalkan bil gateway yang SAH dan boleh dibayar tanpa
sebarang baris merujuknya — webhook mengena 0 baris dan menyenyapkannya
sebagai replay biasa, dan reconcile melelar baris DB jadi ia buta kepada
apa yang tak pernah wujud. Duit masuk, sifar rekod.

Dibalikkan, kegagalan yang setara jadi tak berbahaya: yang tinggal ialah
baris tanpa ref — kelihatan, boleh diaudit, dan **tiada bil untuk sesiapa
bayar**. Ia ditanda `'failed'` dan dilangkau reconcile.

Ini memerlukan `registration_payments.gateway_ref` nullable dengan indeks
unik SEPARA (lihat `DATABASE.md`); indeks penuh akan membuatkan ahli
kedua yang checkout berlanggar dengan yang pertama.

> ⚠️ Tetingkap baki: langkah 3 boleh gagal selepas bil dicipta,
> meninggalkan bil + baris yang tak berpaut. Boleh dipulihkan (baris
> membawa user_id/amaun/timestamp, jadi ia satu UPDATE) dan dilog sebagai
> ERROR + `paymentlog`, tapi bukan sifar. Lihat `TODO.md` **L29**.

## Log bayaran (`internal/paymentlog`)

Jadual `payment_logs` — log PERISTIWA (checkout/webhook/reconcile) merentas
ketiga-tiga modul. Berbeza daripada `internal/audit`, yang merekod delta
MEDAN pada entiti yang boleh disunting.

Tiga keputusan yang membezakannya daripada `audit`:

- **Best-effort, sengaja.** Kegagalan menulis log tak boleh menggagalkan
  laluan bayaran sebenar — lebih-lebih lagi webhook, yang MESTI pulang 200
  supaya gateway tak retry-storm. (`audit.Record` sebaliknya: MESTI berjaya
  atau seluruh permintaan gagal.)
- **`raw_payload` ialah `text`, bukan `jsonb`.** Callback ToyyibPay
  form-urlencoded, bukan JSON. `jsonb` menolak INSERT — dan kerana Record
  best-effort, penolakan itu SENYAP, jadi payload tak pernah tersimpan
  untuk TEPAT dua modul yang menjadi sebab ciri ini dibina.
- **Payload mentah direkod DAHULU**, sebelum sebarang parsing atau
  pengesahan. Baris paling bernilai untuk diagnosis ialah baris daripada
  permintaan yang gagal diparse.

`raw_payload` boleh membawa PII pembayar (billTo/billEmail/billPhone) dan
sengaja disimpan tanpa scrub — sebab itu ia **tidak pernah** didedahkan
melalui API (`paymentLogItem` tiada medan itu) dan retentionnya 90 hari.

## Storan (`internal/storage`)

Client upload **terus ke R2** guna presigned URL — backend tak pernah sentuh
bait gambar (elak jadi bottleneck bandwidth). Backend cuma jana URL, dan
selepas itu sahkan saiz (`HeadObject`) + magic number DAN dimensi
(`GetObject` julat) sebelum kunci diterima masuk post/avatar.

Pengecualian: `PutObject` untuk kandungan yang **dijana server** dan tak
pernah menyentuh peranti — PDF sijil dan PDF resit. Tiada semakan imej di
situ; pemanggil tahu apa yang dihantarnya.

Tiga gotcha, semuanya disahkan terhadap R2 sebenar:

- **`RequestChecksumCalculation` mesti `WhenRequired`.** Default
  aws-sdk-go-v2 menambah checksum CRC32 pada setiap request termasuk
  presigned URL, yang R2 tak serasi — tandatangan jadi tak sah dan PUT
  client dapat 403.
- **Julat baca verifikasi ialah `MaxImageSizeBytes` (5MB), bukan nilai
  tetap kecil.** 12 bait (asal) mahupun 64KB (pusingan 1) tidak mencukupi:
  penanda SOF0 JPEG yang membawa lebar/tinggi boleh ditolak keluar julat
  oleh segmen APPn yang sengaja dipadatkan, dan `verifyDimensions`
  **gagal-terbuka** apabila ia tak dapat mengukur — jadi had dimensi
  senyap tidak terpakai langsung. Lihat `TODO.md` **L16**.
- **`SignedURL` dicache, dan itu bukan pengoptimuman prestasi.**
  Menandatangani ialah HMAC tempatan, murah. Cache wujud untuk
  **kestabilan URL**: presigned URL mengandungi `X-Amz-Date`, jadi
  menandatangani semula setiap permintaan menghasilkan rentetan berbeza,
  dan cache imej pada peranti dikunci ikut URL — setiap tatalan feed akan
  memuat turun semula setiap gambar. TTL cache (1 jam) sengaja SEPARUH
  daripada tempoh sah (2 jam) supaya klien yang menerima URL pada saat
  akhir tetingkap masih dapat sekurang-kurangnya satu jam kesahan.
  Cachenya di Redis bila ada, supaya semua replika memulangkan URL yang
  SAMA — kalau tidak klien terlepas cache setiap kali ia mencapai
  instance berlainan.

Bucket adalah **persendirian**. `R2_PUBLIC_URL` tak lagi diperlukan untuk
memapar gambar; kalau ia masih diset, `main.go` log AMARAN — bucket
berkemungkinan masih terdedah secara awam, yang membatalkan seluruh tujuan
URL bertandatangan.

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

## Bajet masa (timeout)

Nilai ini tersebar merentas beberapa fail dan **tidak** konsisten antara
satu sama lain — dikumpulkan di sini supaya percanggahannya kelihatan:

| Tempat | Nilai | Fail |
|---|---|---|
| `http.Server` ReadHeader | 5s | `cmd/api/main.go` |
| `http.Server` Read | 15s | `cmd/api/main.go` |
| `http.Server` **Write** | **90s** | `cmd/api/main.go` |
| `http.Server` Idle | 60s | `cmd/api/main.go` |
| Muat naik PDF sijil (setiap fail) | 30s | `handlers/activity_certificates.go` |
| Muat naik PDF resit | 30s | `handlers/payments.go` |
| Poll ToyyibPay (createBill / getBillTransactions) | 15s | `payment/toyyibpay.go` |
| Resend / OneSignal | 10s | `email/`, `onesignal/` |
| Redis (arahan) | 200ms | `middleware/ratelimit.go`, `redisclient/` |
| Fan-out notifikasi latar | 2 minit | `handlers/activities.go` |

`WriteTimeout` **mesti** lebih panjang daripada operasi terpanjang yang
dihoskan handler. Ia pernah 15s — lebih PENDEK daripada dua muat naik R2
sisi-pelayan yang berhad 30s, dan penerbitan sijil menjalankan muat naik
itu **berjujukan** untuk setiap penerima, jadi aktiviti 50–200 orang
dijamin putus sambungan. Go tidak membatalkan `Request.Context()` atas
write deadline, jadi handler tetap habis dan datanya betul — cuma
RESPONSnya hilang, dan pengurus nampak "gagal" pada operasi yang
sebenarnya berjaya.

> ⚠️ 90s ialah **tampung**, bukan penyelesaian. Ia mengalihkan siling,
> tidak menghapuskan pertumbuhan linear penerbitan sijil mengikut bilangan
> penerima. Pembaikan sebenar: jadikan fasa 2 kerja latar dan pulangkan
> 202 serta-merta — endpoint itu sudah idempoten dan boleh disambung,
> separuh reka bentuknya sudah wujud. Lihat `TODO.md` **L31**.

Fan-out notifikasi sengaja **tidak** guna ctx permintaan: ia dibatalkan
sebaik respons ditulis, yang akan memotong fan-out di tengah jalan. Ia guna
`context.Background()` dengan hadnya sendiri, dan dijalankan dalam SATU
goroutine per peristiwa (gelung berjujukan di dalam) — bukan satu goroutine
per penerima, yang akan jadi fan-out tak berhad ke OneSignal.

## Config / env

`internal/config/config.go`. Wajib: `DATABASE_URL`, `JWT_SECRET`. Selebihnya
optional, dan "optional" bermaksud satu daripada tiga tingkah laku yang
BERBEZA — bukan satu:

| Kosong | Kesan | Contoh |
|---|---|---|
| No-op senyap | ciri hilang, tiada ralat di mana-mana | OneSignal, Resend |
| 503 yang jelas | endpoint pulang ralat yang boleh dibaca | R2, Stripe, ToyyibPay, `PASSWORD_RESET_URL` |
| Jatuh balik setempat | ciri berfungsi, cuma per-instance | Redis (had kadar + cache URL) |
| Jatuh balik ke Go | halaman HTML Go sendiri, bukan Astro | `EMAIL_VERIFY_URL`, `*_RETURN_URL`, `CERTIFICATE_VERIFY_URL` |

⚠️ `PASSWORD_RESET_URL` ialah satu-satunya var `*_URL` yang **tidak** jatuh
balik ke Go — ia duduk pada baris 503. Borang kata laluan bukan sesuatu
yang patut muncul daripada halaman sandaran yang tiada siapa reka, jadi
ciri itu dimatikan sepenuhnya dan bukan didegradasi.

App **sentiasa** boot. Redis pula disahkan boleh dicapai semasa boot supaya
salah konfigurasi muncul dalam log serta-merta dan bukan pada permintaan
pengguna pertama — tapi kegagalan Ping **tidak** menggagalkan boot: tiada
data dalam app ini yang hidup HANYA dalam Redis, jadi kehilangannya
bermakna hilang penyelarasan antara instance, bukan hilang data.

Nilai tak sah (bukan kosong) dilog dan default digunakan — polisi simpanan
yang salah taip tak patut menghalang app daripada boot, tapi ia juga tak
patut senyap.

Polisi simpanan boleh diubah via env tanpa deploy semula, sebab ia
keputusan POLISI bukan teknikal.

⚠️ `CERTIFICATE_VERIFY_URL` hanya menjejaskan sijil yang dijana **selepas**
nilainya ditukar. QR pada sijil yang sudah dicetak tak boleh dibetulkan —
itu sebabnya `VerifyCertificateRoute` ialah pemalar dieksport dan bukan
literal yang diulang.

## Kenapa sqlc + goose (bukan ORM)

Keputusan awal: user biasa dengan Drizzle (node). sqlc paling dekat dengan
rasa itu di Go — tulis SQL raw, generate types, bukan query builder magic.
goose dipilih atas `golang-migrate` sebab format satu-fail + naming timestamp.

## Deployment

Railway, projek `marc`, environment `staging` + `production`. Tiada Docker.
Migration auto-apply on boot melalui goose embedded.
