# Database

Postgres. Migration diuruskan **goose** (single-file, `-- +goose Up`/`-- +goose Down`
dalam fail sama, naming timestamp). Query layer **sqlc** (raw SQL → Go type-safe
generated code).

Kenapa dua tool berasingan (bukan satu ORM): goose uruskan **schema** (DDL —
`create table`, dsb), sqlc generate **kod query** (`select`/`insert`/`update`)
daripada SQL yang kau tulis sendiri. Tiada overlap, memang lazim dipakai bersama
dalam ekosistem Go.

## Setup sekali sahaja

### 1. Postgres

Dev guna Postgres lokal (bukan Docker — projek ni sengaja tak pakai Docker,
prod terus di Railway):

```bash
brew services start postgresql@18   # ganti ikut versi kau install
createdb marc
```

### 2. goose CLI

Migration auto-apply bila run app (`go run ./cmd/api`, embedded via `go:embed`)
— **tak perlu goose CLI untuk run app**. CLI cuma perlu untuk kerja manual
(rollback, tengok status, cipta migration baru):

```bash
go install github.com/pressly/goose/v3/cmd/goose@v3.27.3
```

`goose` install ke `$(go env GOPATH)/bin` (biasanya `~/go/bin`) — pastikan tu
ada dalam `PATH` kau. Kalau tak nak edit shell profile, panggil terus:

```bash
$(go env GOPATH)/bin/goose ...
```

Command di bawah semua andaikan `goose` dah ada dalam `PATH`.

### 3. `.env`

```bash
cp .env.example .env
# isi DATABASE_URL — contoh:
# postgres://<username-OS-kau>@localhost:5432/marc?sslmode=disable
```

## Command harian

Semua command goose kena `-dir internal/db/migrations` (lokasi fail migration)
dan connection string Postgres. Untuk senang, export dulu:

```bash
export DATABASE_URL="postgres://$(whoami)@localhost:5432/marc?sslmode=disable"
alias gm="goose -dir internal/db/migrations postgres \"$DATABASE_URL\""
```

(Alias ni sesi shell semasa je — letak dalam `.zshrc`/`.bashrc` kalau nak kekal.)

### Status — tengok migration mana dah jalan

```bash
gm status
```

### Up — jalankan semua migration pending

```bash
gm up
```

(App sendiri buat ni automatik on startup — command ni untuk bila kau nak
apply migration TANPA start whole server, cth waktu CI/deploy script atau
debug manual.)

### Up satu langkah je

```bash
gm up-by-one
```

### Down — rollback SATU migration (yang paling last applied)

```bash
gm down
```

### Down-to — rollback sampai versi tertentu (0 = semua)

```bash
gm down-to 0                          # rollback semua, DB balik kosong
gm down-to 20260805223500             # rollback sampai (tak termasuk) migration ni
```

### Redo — down + up migration terakhir (senang untuk test up/down sekali gus)

```bash
gm redo
```

### Reset — down semua, then up semua (padanan `down-to 0` + `up`)

```bash
gm reset
```

### Create — jana fail migration baru

```bash
goose -dir internal/db/migrations create nama_migration_snake_case sql
```

Contoh:

```bash
goose -dir internal/db/migrations create add_notifications_table sql
```

Hasilkan fail `{timestamp}_add_notifications_table.sql` (timestamp UTC) dengan
template:

```sql
-- +goose Up
SELECT 'up SQL query';

-- +goose Down
SELECT 'down SQL query';
```

Ganti dengan DDL sebenar. **Kena ada dua-dua** `Up` dan `Down` — down tak
boleh kosong walaupun "susah nak reverse", sekurang-kurangnya `-- tiada
reverse mudah` sebagai komen supaya jelas sengaja, bukan tertinggal.

Lepas cipta migration baru, **regenerate sqlc kalau ada jadual/lajur baru**
(lihat bawah).

## sqlc — query layer

Fail SQL kau tulis sendiri dalam `queries/*.sql`, sqlc generate Go code ke
`internal/db/sqlc/`. **Jangan edit fail dalam `internal/db/sqlc/` terus** —
akan overwrite bila `sqlc generate` dijalankan semula (ada komen
"DO NOT EDIT" atas setiap fail generated).

### Generate (lepas edit `queries/*.sql` atau migration baru)

```bash
sqlc generate
```

(`sqlc` CLI kena install dulu — `brew install sqlc` kalau belum ada.)

### Tulis query baru

1. Tambah SQL dalam `queries/<nama_jadual>.sql`, format:
   ```sql
   -- name: GetSomethingByID :one
   select * from something where id = $1;
   ```
   `:one` / `:many` / `:exec` tentukan shape return value.
2. `sqlc generate`
3. Fail baru muncul dalam `internal/db/sqlc/<nama_jadual>.sql.go` — import
   `marc/internal/db/sqlc`, panggil terus (`sqlc.New(pool)` atau
   `queries.WithTx(tx)` untuk transaction).

Config penuh dalam `sqlc.yaml` — satu override penting: kolum bertype `uuid`
di-generate sebagai `github.com/google/uuid.UUID` (bukan default
`pgtype.UUID`), lebih senang dipakai (`.String()`, compare terus, dsb).

## Schema semasa

| Table | Tujuan |
|---|---|
| `users` | akaun (email, password_hash) — gantian `auth.users` Supabase |
| `roles` | `ahli`/`supervisor`/`manager`/`superadmin`, kategori `ahli`/`management` |
| `profiles` | member_id (`MARC{YYYY}/{MM}/{0000}`), display_name, phone, role_id, email_verified, status (`pending`/`approved`/`rejected`, Stage 11), approved_by, approved_at |
| `device_tokens` | OneSignal subscription id per user (push notification) |
| `sequences` | counter generic — dipakai jana `member_id` atomic |
| `refresh_tokens` | hash refresh token (single-use, dipadam lepas consume) |
| `email_verification_tokens` | hash token pengesahan email (TTL 1 jam) |

Migration penuh (order): `internal/db/migrations/2026...sql` — baca terus
kalau nak schema DDL tepat.

## Local dev — cycle biasa

```bash
# 1. Mula dari kosong
gm down-to 0

# 2. Naik semula, confirm semua migration jalan bersih
gm up
gm status

# 3. Run app (migration auto-apply lagi — no-op sebab dah up)
go run ./cmd/api
```

## Troubleshooting

- **`goose: no migrations to run`** — normal, bermaksud DB dah up-to-date.
- **`command not found: goose`** — CLI tak dalam PATH, guna
  `$(go env GOPATH)/bin/goose` terus atau tambah ke `PATH`.
- **Migration baru tak ter-apply bila `go run ./cmd/api`** — pastikan fail
  migration ada extension `.sql` dan format nama `{timestamp}_name.sql`
  (goose skip fail yang tak match pattern ni), dan **fail tu wujud SEBELUM
  build** — sebab `//go:embed all:migrations` dalam `internal/db/db.go`
  embed fail masa compile, bukan runtime.
