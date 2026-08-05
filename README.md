# MARC Backend

Backend Go untuk app kelab MARC (gantian Supabase — lihat [`TODO.md`](./TODO.md)
untuk sejarah & status stage-by-stage penuh, [`ARCHITECTURE.md`](./ARCHITECTURE.md)
untuk struktur kod, [`DATABASE.md`](./DATABASE.md) untuk kerja migration/schema).

## Tech stack

- **Go 1.26** + **Gin** — HTTP framework
- **Postgres** — dev guna Postgres lokal (Homebrew), prod di Railway
- **goose** — migration (single-file `-- +goose Up`/`Down`, embedded + auto-run on startup)
- **sqlc** — generate Go type-safe daripada raw SQL (`queries/*.sql` → `internal/db/sqlc`)
- **pgx/v5** — Postgres driver
- **JWT (access) + opaque token (refresh, rotated)** — auth custom, bukan Supabase Auth
- **Resend** — email verification
- **OneSignal REST API** — push notification (client sudah siap, belum ada trigger dipakai)

## Quickstart (dev)

```bash
# 1. Postgres lokal (Homebrew) — sekali sahaja
brew services start postgresql@18   # atau versi lain yang kau install
createdb marc

# 2. .env
cp .env.example .env
# isi DATABASE_URL (guna username OS kau), JWT_SECRET (openssl rand -base64 48)
# ONESIGNAL_*, RESEND_*, EMAIL_FROM — optional, no-op senyap kalau kosong

# 3. Run — migration auto-apply on startup (goose embedded)
go run ./cmd/api
```

Server naik di `:8080` (atau `PORT` dalam `.env`). Health check:

```bash
curl http://localhost:8080/healthz
```

## Endpoints

| Method | Path | Auth | Nota |
|---|---|---|---|
| GET | `/healthz` | - | |
| POST | `/auth/register` | - | create user + profile (member_id auto-generate), issue token pair |
| POST | `/auth/login` | - | issue token pair |
| POST | `/auth/refresh` | - | rotate refresh token (single-use, atomic) |
| POST | `/auth/logout` | - | revoke refresh token |
| POST | `/auth/verify-email/request` | ✓ | hantar email verification (Resend) |
| POST | `/auth/verify-email/confirm` | - | confirm via JSON body `{token}` (app call) |
| GET | `/auth/verify-email/confirm?token=` | - | confirm via link (klik dari email, render HTML) |
| GET | `/me` | ✓ | profil user semasa |
| PATCH | `/me` | ✓ | update display_name/phone |
| GET | `/members` | ✓ | senarai ahli (ahli → diri sendiri; management → semua) |
| POST | `/device-tokens` | ✓ | upsert OneSignal subscription id |
| DELETE | `/device-tokens/:id` | ✓ | buang device token (scoped ke pemilik) |

`✓` = perlu header `Authorization: Bearer <access_token>`.

## Testing

```bash
go build ./...
go vet ./...
gofmt -l .        # kosong = clean
go test ./...
```

Unit test wujud untuk `internal/onesignal`, `internal/push`, dan `internal/email`
(guna `httptest`, tiada call keluar sebenar). Handler/integration di-test manual
lawan Postgres sebenar semasa pembangunan — lihat `TODO.md` untuk rekod verifikasi
setiap stage.

## Struktur

Lihat [`ARCHITECTURE.md`](./ARCHITECTURE.md).

## Deployment

Railway (projek `marc`, environment `staging` + `production`). `config.Load()`
baca `DATABASE_URL` terus daripada env Railway punya Postgres plugin — tiada
perubahan kod diperlukan antara dev/staging/prod.
