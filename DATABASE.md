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

### Identiti & akses

| Table | Tujuan |
|---|---|
| `users` | akaun (email lowercase-unique, password_hash) |
| `roles` | `ahli`(10)/`supervisor`(50)/`manager`(60)/`superadmin`(100); lajur `rank` yang memacu keterlihatan & hierarki edit |
| `profiles` | member_id (`MARC{YYYY}/{MM}/{0000}`), display_name, phone, role_id, email_verified, status (`pending`/`approved`/`rejected`), approved_by/at |
| `sequences` | counter generic — jana `member_id` secara atomic |
| `refresh_tokens` | SHA-256 hash + family_id (single-use, dipadam bila consume) |
| `email_verification_tokens` | hash token pengesahan (TTL 1 jam) |
| `device_tokens` | OneSignal subscription id per user |

### Kandungan

| Table | Tujuan |
|---|---|
| `posts` | type (`normal`/`announcement`), content, edited_at, **deleted_at (soft delete)** |
| `post_images` | r2_key + position (maks 4 setiap post) |
| `post_likes` | PK komposit (post_id, user_id) — toggle |
| `comments` | parent_comment_id (depth di-cap 2), edited_at, deleted_at |
| `comment_likes` | PK komposit (comment_id, user_id) |
| `notifications` | recipient/actor/type, read_at |

### Upload & storan

| Table | Tujuan |
|---|---|
| `pending_uploads` | kunci R2 yang dah dipresign tapi belum dilekatkan pada post. Disapu pada 6 jam (karangan ditinggalkan) |
| `deleted_uploads` | gilir padam R2 dengan backoff. `deleted_at` ialah **batu nisan**, bukan padam baris — tanpanya penyapu yatim menggilir semula kunci yang sama selamanya |

### Aktiviti

| Table | Tujuan |
|---|---|
| `activity_categories` | key/name/sort_order/is_active. Di-seed dalam migration (badminton, futsal, bola tampar, larian, ping pong, lain-lain) mengikut corak `seed_roles` |
| `activities` | tajuk, lokasi, tetingkap pendaftaran, capacity, `fee_cents`+`currency`, `attendance_threshold_pct`, status (`draft`/`published`/`cancelled`/`completed`), `certificates_issued_at`, **deleted_at (soft delete)**. `category_id` ialah `on delete restrict` |
| `activity_sessions` | satu baris per sesi; `seq` unik dalam aktiviti, `check (ends_at > starts_at)`. **Sumber kebenaran** bagi tetingkap masa aktiviti |
| `activity_registrations` | status (`pending_payment`/`registered`/`cancelled`), `payment_status`+`payment_ref` (cangkuk payment, belum disambung), `checkin_token` unik. Indeks unik **separa** atas `(activity_id, user_id) where status <> 'cancelled'` — halang pendaftaran berganda tapi benarkan daftar semula selepas batal |
| `activity_attendances` | `(registration_id, session_id)` unik. `method` ∈ `manual`/`scan`/`self_scan`/`code` — keempat-empatnya hasilkan baris yang SAMA, hanya `method` + `marked_by` berbeza, jadi menambah kaedah nanti tidak perlukan migration (hanya `manual` dan `scan` ada pelaksanaan). **Bukan bermakna ia murah**: `self_scan` perlukan `checkin_token` BERPUTAR di sisi pelayan — lihat `TODO.md`, bahagian Modul Aktiviti |
| `activity_certificates` | `serial` unik + `verify_token` unik **berasingan** (serial berjujukan; kalau ia juga kunci pengesahan awam, sesiapa boleh tambah satu dan menuai nama semua ahli). `recipient_name`/`activity_title`/`activity_date` ialah **snapshot** — PDF tak berubah selepas dijana, jadi halaman pengesahan mesti menunjukkan apa yang TERCETAK. `activity_id` `on delete restrict`. Unik `(activity_id, user_id)` |

Dua migration `notifications` datang bersama modul ni:

| Migration | Kesan |
|---|---|
| `20260810100600_widen_notifications_activity` | luaskan `notifications_type_check` dengan `activity_published`, `activity_cancelled`, `certificate_ready`. Down akan **gagal** kalau baris jenis baharu sudah wujud — sama seperti `20260807120100`, dijangka untuk rollback dev |
| `20260810100700_add_notifications_activity_links` | tambah `activity_id` + `certificate_id` (nullable, `on delete cascade`). Tanpanya notifikasi aktiviti ialah satu-satunya jenis yang tak boleh diketuk — setiap jenis lain deep-link melalui `post_id` |

#### Invarian: `activities.starts_at`/`ends_at` DIDENORMALISASI

**Sesi ialah sumber kebenaran.** Dua lajur itu ialah `min(starts_at)` dan
`max(ends_at)` bagi `activity_sessions` aktiviti berkenaan, dikira semula
oleh `RecomputeActivityWindow` **dalam transaksi yang sama** dengan
sebarang perubahan set sesi (`ReplaceActivitySessions`, dan juga laluan
cipta — ada DUA penulis kepada invarian ni). Sebab denormalisasi: senarai
aktiviti perlu isih dan tapis ikut tarikh menggunakan indeks, dan `min()`
atas join pada setiap senarai terlalu mahal.

**Perangkap:** `RecomputeActivityWindow` ada guard `min_start is not null`,
jadi set sesi **kosong** meninggalkan tetingkap lama sebagai nilai basi
**tanpa ralat** — ia gagal senyap. Itulah sebabnya handler menolak senarai
sesi kosong pada peringkat permintaan. Kalau kau menambah laluan tulis
sesi yang baharu, laluan itu mesti menolak set kosong juga; DB tidak akan
menangkapnya untuk kau.

Nota berkaitan: tiada `check (ends_at >= starts_at)` pada `activities`
(sengaja — lajurnya didenormalisasi dan dijaga app), walaupun
`activity_sessions` ADA check itu. Tiada juga check `currency = 'MYR'`.

### Duit & jejak

| Table | Tujuan |
|---|---|
| `donations` | amount_cents, gateway, gateway_ref, status. Unik `(gateway, gateway_ref)` supaya webhook retry tak cipta baris pendua. Constraint `donations_traceable`: user_id ATAU donor_email mesti ada |
| `audit_logs` | siapa ubah apa. Delta jsonb + changed_fields, snapshot pelaku (member_id/role sebagai teks). **Append-only dikuatkuasakan trigger** |

Nota `audit_logs`: trigger `audit_logs_no_update` tolak semua UPDATE KECUALI
satu bentuk — menetapkan `ip_address` dan `user_agent` kepada NULL sambil
setiap lajur lain kekal `is not distinct from` nilai lama. Itu membenarkan
redaksi PDPA tanpa membenarkan sejarah ditulis semula. DELETE dibiar terbuka
supaya pruning simpanan mungkin.

Migration penuh (ikut order): `internal/db/migrations/*.sql`.

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
- **Migration ada `create function` / plpgsql gagal parse** — bungkus blok
  tu dengan `-- +goose StatementBegin` / `-- +goose StatementEnd`. goose
  pecahkan fail ikut `;`, jadi tanpa penanda ni badan function terpotong
  di tengah.
- **Migration baru tak ter-apply bila `go run ./cmd/api`** — pastikan fail
  migration ada extension `.sql` dan format nama `{timestamp}_name.sql`
  (goose skip fail yang tak match pattern ni), dan **fail tu wujud SEBELUM
  build** — sebab `//go:embed all:migrations` dalam `internal/db/db.go`
  embed fail masa compile, bukan runtime.
