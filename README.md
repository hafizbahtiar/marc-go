# MARC Backend

Backend Go untuk app komuniti MARC (gantian Supabase).
[`ARCHITECTURE.md`](./ARCHITECTURE.md) untuk struktur kod,
[`DATABASE.md`](./DATABASE.md) untuk schema & migration,
[`TODO.md`](./TODO.md) untuk kerja yang belum siap,
[`docs/`](./docs/) untuk spec, plan dan laporan audit.

Client Flutter: repo `marc_flutter` (sibling).

## Tech stack

- **Go 1.26** + **Gin** — HTTP framework
- **Postgres** — dev lokal (Homebrew), prod di Railway
- **goose** — migration (single-file `Up`/`Down`, embedded + auto-run on startup)
- **sqlc** — generate Go type-safe daripada raw SQL (`queries/*.sql` → `internal/db/sqlc`)
- **pgx/v5** — Postgres driver
- **JWT (access) + opaque token (refresh, rotated)** — auth custom
- **Cloudflare R2** — storan gambar + PDF (upload presigned terus dari client;
  PDF dijana server ditolak terus)
- **Redis** — *pilihan*: had kadar teragih + kestabilan URL R2 antara replika.
  Kosong = jatuh balik per-instance, bukan gagal
- **Stripe** — derma (kad + FPX) melalui interface `payment.Gateway`
- **ToyyibPay** — yuran pendaftaran ahli + yuran aktiviti (dua instance,
  kredential sama, callback berbeza)
- **go-pdf/fpdf** + **skip2/go-qrcode** — PDF resit & sijil, QR pengesahan
- **Resend** — emel pengesahan + resit derma (dengan lampiran PDF)
- **OneSignal** — push notification

## Kerja latar

Lima goroutine bermula pada boot (`cmd/api/main.go`), semuanya selamat
kalau proses terbunuh dan disambung semula pada boot berikutnya:

| Modul | Kadar | Tugas |
|---|---|---|
| `reaper` | 15 min | padam objek R2 yatim (post dipadam, karangan ditinggalkan) |
| `activitysweep` | 15 min | batal pendaftaran berbayar terbiar, bebaskan slot |
| `paymentreconcile` | 30 min | semak bayaran `pending` terus pada gateway, betulkan DB |
| `activitylifecycle` | 1 jam | peringatan H-1 + auto-complete aktiviti tamat |
| `retention` | 24 jam | redaksi PII audit, prune audit/payment_logs/batu nisan |

Butiran reka bentuk: [`ARCHITECTURE.md`](./ARCHITECTURE.md).

## Quickstart (dev)

```bash
# 1. Postgres lokal — sekali sahaja
brew services start postgresql@18
createdb marc

# 2. .env
cp .env.example .env
# WAJIB: DATABASE_URL, JWT_SECRET (openssl rand -base64 48)
# Selebihnya optional — no-op senyap kalau kosong (R2, Stripe, Resend,
# OneSignal). App tetap boot; ciri berkenaan pulang 503 yang jelas.

# 3. Run — migration auto-apply on startup
go run ./cmd/api
```

```bash
curl http://localhost:8080/healthz
```

## Endpoints

`✓` = perlu `Authorization: Bearer <access_token>`.
Lapisan akses bertingkat: **auth** → **approved** (status diluluskan) →
**verified** (emel disahkan).

### Auth

| Method | Path | Akses | Nota |
|---|---|---|---|
| POST | `/auth/register` | — | cipta user + profile (member_id auto), issue token pair |
| POST | `/auth/login` | — | rate limit 5/min per IP |
| POST | `/auth/refresh` | — | rotate refresh token (single-use, atomic) |
| POST | `/auth/logout` | — | revoke satu refresh token |
| POST | `/auth/logout-all` | ✓ | revoke semua sesi |
| POST | `/auth/password-reset/request` | — | sentiasa 204 (tiada enumerasi); 503 kalau `PASSWORD_RESET_URL` kosong |
| POST | `/auth/password-reset/confirm` | — | dari halaman Astro; tukar kata laluan + batal SEMUA sesi |
| POST | `/auth/verify-email/request` | ✓ | hantar emel pengesahan |
| POST | `/auth/verify-email/confirm` | — | confirm via JSON (dari app) |
| GET | `/auth/verify-email/confirm?token=` | — | confirm via klik link (render HTML) |

### Profil & ahli

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/me` | ✓ | sengaja TIDAK perlu approved — user pending kena boleh baca status sendiri; bawa `telegram_linked`/`telegram_username` |
| PATCH | `/me` | ✓ | display_name / phone |
| POST | `/me/telegram-link/token` | ✓ | jana deep-link binding Telegram; 503 kalau `TELEGRAM_BOT_TOKEN` kosong |
| DELETE | `/me/telegram-link` | ✓ | nyahikat akaun Telegram; idempoten |
| POST | `/webhooks/telegram` | — | dipanggil Telegram sahaja; route tak berdaftar bila ciri dimatikan |
| GET | `/members` | approved | keterlihatan ikut `roles.rank`; emel ahli lain management sahaja |
| GET | `/members?status=pending` | approved | barisan kelulusan — management sahaja |
| POST | `/members/:id/approve` | approved | management; diaudit |
| POST | `/members/:id/reject` | approved | management; diaudit + revoke sesi target |
| PATCH | `/members/:id/role` | approved | hierarki rank; diaudit |
| GET | `/roles` | approved | ditapis kepada role yang caller boleh assign |
| GET | `/audit-logs` | approved | management sahaja; keyset pagination `before_id` |
| POST | `/me/deletion-request` | ✓ | idempoten; rekod permintaan sahaja (tiada auto-purge) |
| POST | `/device-tokens` | approved | daftar OneSignal subscription id |
| DELETE | `/device-tokens/:id` | approved | |
| DELETE | `/device-tokens/by-onesignal/:onesignalId` | approved | dipakai waktu logout |

### Aktiviti

Baca cukup `approved`; menulis perlu `verified`. Semakan "management sahaja"
dibuat **dalam handler**, bukan pada grup route.

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/activity-categories` | approved | `?all=true` = termasuk tidak aktif, manager ke atas |
| POST/PATCH | `/activity-categories[/:id]` | verified | manager ke atas |
| GET | `/activities` | approved | keyset cursor; `?status=draft` management sahaja |
| GET | `/activities/:id` | approved | draf pulang 404 kepada bukan-management |
| POST | `/activities` | verified | management; cipta + sesi sekali gus |
| PATCH | `/activities/:id` | verified | management; PATCH separa sebenar (`optional[T]`) |
| POST | `/activities/:id/publish` | verified | management; draf → published, fan-out push |
| POST | `/activities/:id/cancel` | verified | management; notify pendaftar sahaja |
| PUT | `/activities/:id/sessions` | verified | ganti SELURUH set; ditolak kalau ada kehadiran |
| GET | `/me/activities` | approved | pendaftaran aktif sendiri |
| POST/DELETE | `/activities/:id/registration` | verified | daftar / batal |
| GET | `/activities/:id/registrations` | verified | management; termasuk `attended_session_ids` |
| POST | `/activities/:id/sessions/:sid/attendance` | verified | `manual`/`scan` = management; `self_scan` = ahli sendiri |
| DELETE | `/activities/:id/sessions/:sid/attendance/:rid` | verified | management; sentiasa diaudit |

### Sijil

| Method | Path | Akses | Nota |
|---|---|---|---|
| POST | `/activities/:id/certificates` | verified | management; 2 fasa, boleh diulang untuk sambung |
| POST | `/certificates/:id/revoke` | verified | management; baris kekal, fail digilir padam |
| GET | `/me/certificates` | approved | |
| GET | `/me/certificates/:id/file` | approved | pulang URL bertandatangan, bukan bait PDF |
| GET | `/verify/certificates/:token` | **awam** | QR sijil bercetak; respons medan-awam sahaja |

### Posts & comments

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/posts` | verified | feed cursor-based |
| POST | `/posts` | verified | content + `r2_keys[]` (maks 4 gambar) |
| GET | `/posts/:id` | verified | |
| PATCH | `/posts/:id` | verified | pemilik; diaudit |
| DELETE | `/posts/:id` | verified | pemilik/management; soft delete + gilir padam R2 |
| POST/DELETE | `/posts/:id/like` | verified | |
| GET | `/posts/:id/comments` | verified | flat + `parent_comment_id`, client bina tree |
| POST | `/posts/:id/comments` | verified | depth di-cap 2 |
| PATCH | `/comments/:id` | verified | pemilik; diaudit |
| DELETE | `/comments/:id` | verified | pemilik/management; diaudit |
| POST/DELETE | `/comments/:id/like` | verified | |
| POST | `/uploads/presign` | verified | rate limit; pulang `{upload_url, r2_key}` |

### Notifikasi

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/notifications` | verified | |
| POST | `/notifications/:id/read` | verified | |
| POST | `/notifications/read-all` | verified | |

### Bayaran

Tiga modul **berasingan** — jangan keliru. Butiran: [`ARCHITECTURE.md`](./ARCHITECTURE.md).

| Method | Path | Akses | Nota |
|---|---|---|---|
| POST | `/donations/checkout` | **awam** | OptionalAuth — guest boleh derma (emel wajib) |
| POST | `/webhooks/:gateway` | **awam** | verify tandatangan; `:gateway` = `stripe` |
| POST | `/registration-payments/checkout` | ✓ | yuran ahli SEKALI bayar; `protected` supaya ahli `pending` boleh bayar |
| POST | `/registration-payments/webhook/toyyibpay` | **awam** | ambil `billcode`, sahkan via poll `getBillTransactions` |
| GET | `/registration-payments/return/toyyibpay` | **awam** | halaman landing; 302 ke Astro kalau dikonfigur |
| POST | `/activities/:id/registration/checkout` | verified | yuran AKTIVITI; mesti dah berdaftar dahulu |
| POST | `/activity-registrations/webhook/toyyibpay` | **awam** | instance gateway KEDUA (callback berbeza) |
| GET | `/activity-registrations/return/toyyibpay` | **awam** | |
| GET | `/me/payments` | ✓ | sejarah sendiri — yuran pendaftaran + yuran aktiviti + derma (derma tanpa nama dikecualikan) |
| GET | `/me/payments/registration/:id/receipt` | ✓ | jana PDF + pulang URL bertandatangan |
| GET | `/me/payments/activity/:id/receipt` | ✓ | `:id` = id **pendaftaran**, bukan id aktiviti |
| GET | `/me/payments/donation/:id/receipt` | ✓ | |

Route checkout melalui `BlockTesterWrites` — akaun `tester` (review Google
Play/App Store) berkelakuan macam ahli biasa untuk SEMUA tindakan lain,
cuma bayaran sebenar yang disekat.

### Admin

Diletak pada grup `approved`; siling sebenar dikuatkuasakan dalam handler.

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/admin/payments` | approved | management; modul `donation` **superadmin sahaja** |
| POST | `/admin/payments/reconcile` | approved | management; cetus satu pusingan `paymentreconcile` |
| GET/POST | `/admin/blocked-email-domains` | approved | **superadmin sahaja** |
| DELETE | `/admin/blocked-email-domains/:domain` | approved | **superadmin sahaja** |

## Testing

```bash
go build ./... && go vet ./... && gofmt -l .   # kosong = clean
go test ./...
golangci-lint run
```

Ujian lawan infra sebenar (R2, Postgres) di-skip secara lalai — lihat
[`TODO.md`](./TODO.md) untuk cara jalankannya.

CI menjalankan perkhidmatan **Postgres 18 + Redis 8** sebenar, jadi ujian
bersandar-DB benar-benar berjalan pada setiap PR — **202 PASS / 9 SKIP**,
dan kesembilan-sembilan SKIP itu ujian R2 (perlukan kredential Cloudflare).
`-race` dihidupkan.

Satu langkah **tripwire** menggagalkan job kalau mana-mana ujian melapor
SKIP atas sebab env var DB hilang — supaya menamakan semula satu env var
tak boleh senyap mengembalikan CI kepada keadaan lama (dulu: 89 PASS /
122 SKIP).

Setempat, ujian bersandar-DB dilangkau melainkan env var diset — arahan
penuh dalam [`TODO.md`](./TODO.md) bahagian Ujian.

## Deployment

Railway (projek `marc`, environment `staging` live). `config.Load()` baca
`DATABASE_URL` terus daripada Postgres plugin Railway — tiada perubahan kod
antara dev/staging/prod. Migration auto-apply on boot.

```bash
railway logs                        # log staging
railway variables --json            # semak env (nilai penuh, tak dipotong)
railway variables --set "KEY=value" # auto-redeploy
```
