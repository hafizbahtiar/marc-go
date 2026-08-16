# MARC Backend

Backend Go untuk app komuniti MARC (gantian Supabase).
[`ARCHITECTURE.md`](./ARCHITECTURE.md) untuk struktur kod,
[`DATABASE.md`](./DATABASE.md) untuk schema & migration,
[`TODO.md`](./TODO.md) untuk kerja yang belum siap.

Client Flutter: repo `marc_flutter` (sibling).

## Tech stack

- **Go 1.26** + **Gin** — HTTP framework
- **Postgres** — dev lokal (Homebrew), prod di Railway
- **goose** — migration (single-file `Up`/`Down`, embedded + auto-run on startup)
- **sqlc** — generate Go type-safe daripada raw SQL (`queries/*.sql` → `internal/db/sqlc`)
- **pgx/v5** — Postgres driver
- **JWT (access) + opaque token (refresh, rotated)** — auth custom
- **Cloudflare R2** — storan gambar (upload presigned terus dari client)
- **Stripe** — donation (kad + FPX) melalui interface `payment.Gateway`
- **Resend** — emel pengesahan + resit donation (dengan lampiran PDF)
- **OneSignal** — push notification

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
| POST | `/auth/verify-email/request` | ✓ | hantar emel pengesahan |
| POST | `/auth/verify-email/confirm` | — | confirm via JSON (dari app) |
| GET | `/auth/verify-email/confirm?token=` | — | confirm via klik link (render HTML) |

### Profil & ahli

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/me` | ✓ | sengaja TIDAK perlu approved — user pending kena boleh baca status sendiri |
| PATCH | `/me` | ✓ | display_name / phone |
| GET | `/members` | approved | keterlihatan ikut `roles.rank`; emel ahli lain management sahaja |
| GET | `/members?status=pending` | approved | barisan kelulusan — management sahaja |
| POST | `/members/:id/approve` | approved | management; diaudit |
| POST | `/members/:id/reject` | approved | management; diaudit + revoke sesi target |
| PATCH | `/members/:id/role` | approved | hierarki rank; diaudit |
| GET | `/roles` | approved | ditapis kepada role yang caller boleh assign |
| GET | `/audit-logs` | approved | management sahaja; keyset pagination `before_id` |

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

### Notifikasi & donation

| Method | Path | Akses | Nota |
|---|---|---|---|
| GET | `/notifications` | verified | |
| POST | `/notifications/:id/read` | verified | |
| POST | `/notifications/read-all` | verified | |
| POST | `/donations/checkout` | **awam** | OptionalAuth — guest boleh derma (emel wajib) |
| POST | `/webhooks/:gateway` | **awam** | verify tandatangan; `:gateway` = `stripe` |

## Testing

```bash
go build ./... && go vet ./... && gofmt -l .   # kosong = clean
go test ./...
golangci-lint run
```

Ujian lawan infra sebenar (R2, Postgres) di-skip secara lalai — lihat
[`TODO.md`](./TODO.md) untuk cara jalankannya.

## Deployment

Railway (projek `marc`, environment `staging` live). `config.Load()` baca
`DATABASE_URL` terus daripada Postgres plugin Railway — tiada perubahan kod
antara dev/staging/prod. Migration auto-apply on boot.

```bash
railway logs                        # log staging
railway variables --json            # semak env (nilai penuh, tak dipotong)
railway variables --set "KEY=value" # auto-redeploy
```
