# Database

Struktur kod: [`ARCHITECTURE.md`](./ARCHITECTURE.md).
Kerja belum siap: [`TODO.md`](./TODO.md).
Spec, plan, laporan audit: [`docs/`](./docs/).

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
| `roles` | `tester`(5)/`ahli`(10)/`supervisor`(50)/`manager`(60)/`admin`(80)/`superadmin`(100); lajur `rank` yang memacu keterlihatan & hierarki edit |
| `profiles` | member_id (`MARC{YYYY}/{MM}/{0000}`), display_name, phone, `avatar_r2_key`, role_id, email_verified, status (`pending`/`approved`/`rejected`), approved_by/at |
| `sequences` | counter generic — jana `member_id` DAN `serial` sijil secara atomic |
| `refresh_tokens` | SHA-256 hash + family_id. Single-use via `consumed_at` |
| `email_verification_tokens` | hash token pengesahan (TTL 1 jam) |
| `device_tokens` | OneSignal subscription id per user. Unik ikut `onesignal_id` |
| `blocked_email_domains` | domain emel disekat, tambahan MANUAL superadmin — pelengkap kpd senarai statik terbenam `internal/disposableemail/domains.txt` |
| `account_deletion_requests` | `user_id` unik, `status` (`pending`/`completed`). Keperluan Google Play Console. **Rekod permintaan sahaja** — tiada auto-purge, staff tindak manual |

Nota `roles`: `tester` sengaja `category = 'ahli'` (bukan kategori sendiri)
supaya SETIAP gate `authz.IsManagement` sedia ada terus terpakai tanpa ubah
apa-apa — akaun review Google Play/App Store perlu menguji aliran app
sebenar. Sekatannya (checkout bayaran sahaja) dikuatkuasakan oleh
`middleware.BlockTesterWrites` pada route checkout, BUKAN oleh rank/category.
Ranknya 5 (bawah `ahli`, bukan sama) supaya ia tak pernah tersilap disamakan
dengan ahli dalam perbandingan rank seperti `newRole.Rank >= caller.RoleRank`.

`admin`(80) ialah tier antara `manager` dan `superadmin`. Ia **tidak**
automatik mewarisi kuasa superadmin: mana-mana semakan yang secara eksplisit
menuntut rank superadmin (`authz.IsAtLeastRole(…, "superadmin")` — domain
emel disekat, data derma dalam `/admin/payments`) kekal di luar capaiannya.

Nota `refresh_tokens`: baris **TIDAK** dipadam bila dikonsum — `consumed_at`
ditetapkan sebaliknya. Baris yang tinggal itulah yang membolehkan pengesanan
reuse: hash yang sama dicuba SEKALI LAGI mendapat 0 baris daripada
`update … where consumed_at is null`, dan caller boleh membezakan "tak
pernah wujud" daripada "dah dikonsum" lalu membatalkan seluruh `family_id`.
Rotasi guna SATU statement `UPDATE … RETURNING` — reka bentuk
baca-dahulu-kemudian-tulis ada gap TOCTOU yang membenarkan dua permintaan
serentak KEDUA-DUANYA berjaya.

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
| `pending_uploads` | kunci R2 yang dah dipresign tapi belum dilekatkan pada post/avatar. Disapu pada 6 jam (karangan ditinggalkan) |
| `deleted_uploads` | gilir padam R2 dengan backoff. `deleted_at` ialah **batu nisan**, bukan padam baris — tanpanya penyapu yatim menggilir semula kunci yang sama selamanya |

⚠️ **`pending_uploads` ialah senarai padam, bukan senarai jejak.** Baris
dikeluarkan apabila kunci dilekatkan pada post (dalam transaksi cipta post)
atau pada profil (`applyAvatar`). Apa sahaja yang MASIH ada di sini selepas
6 jam akan dipadam daripada R2.

`ListStalePendingUploads` kini menyemak lekatan secara bebas — dua klausa
`not exists` terhadap `post_images` dan `profiles.avatar_r2_key` (indeks:
`post_images_r2_key_idx`). Sebelum 2026-08-22 ia menapis ikut UMUR sahaja
dan bergantung sepenuhnya pada baris dikeluarkan oleh kod Go, jadi satu
DELETE yang gagal bermakna gambar post yang masih dipaparkan dipadam enam
jam kemudian. Kedua-dua lapisan (predikat SQL + semakan ralat dalam
`posts.go`) dikekalkan dengan sengaja. Lihat `TODO.md` **L28**.

### Aktiviti

| Table | Tujuan |
|---|---|
| `activity_categories` | key/name/sort_order/is_active. Di-seed dalam migration (badminton, futsal, bola tampar, larian, ping pong, lain-lain) mengikut corak `seed_roles` |
| `activities` | tajuk, lokasi, tetingkap pendaftaran, capacity, `fee_cents`+`currency`, `attendance_threshold_pct`, status (`draft`/`published`/`cancelled`/`completed`), `certificates_issued_at`, `reminder_sent_at`, **deleted_at (soft delete)**. `category_id` ialah `on delete restrict` |
| `activity_sessions` | satu baris per sesi; `seq` unik dalam aktiviti, `check (ends_at > starts_at)`. **Sumber kebenaran** bagi tetingkap masa aktiviti |
| `activity_registrations` | status (`pending_payment`/`registered`/`cancelled`), `payment_status`+`payment_ref`+`fee_cents_paid` (**disambung** — ToyyibPay, lihat "Duit & jejak"), `checkin_token` unik. Indeks unik **separa** atas `(activity_id, user_id) where status <> 'cancelled'` — halang pendaftaran berganda tapi benarkan daftar semula selepas batal |
| `activity_attendances` | `(registration_id, session_id)` unik. `method` ∈ `manual`/`scan`/`self_scan`/`code` — keempat-empatnya hasilkan baris yang SAMA, hanya `method` + `marked_by` berbeza, jadi menambah kaedah tidak perlukan migration (`code` sahaja yang belum ada pelaksanaan klien) |
| `activity_certificates` | `serial` unik + `verify_token` unik **berasingan** (serial berjujukan; kalau ia juga kunci pengesahan awam, sesiapa boleh tambah satu dan menuai nama semua ahli). `recipient_name`/`activity_title`/`activity_date` ialah **snapshot** — PDF tak berubah selepas dijana, jadi halaman pengesahan mesti menunjukkan apa yang TERCETAK. `activity_id` `on delete restrict`. Unik `(activity_id, user_id)` |

Nota `self_scan` (**menggantikan nota lama yang kata ia perlukan
`checkin_token` BERPUTAR**): keperluan itu dielak SEPENUHNYA oleh reka
bentuk yang dipilih. `self_scan` tidak menggunakan `checkin_token` mahupun
`registration_id` langsung — identiti datang daripada JWT pemanggil
(`middleware.UserID`), dan handler MENOLAK permintaan `self_scan` yang
membawa mana-mana dua medan itu. QR yang diimbas ahli hanya mengekod
pasangan aktiviti+sesi (data awam venue, bukan kelayakan peribadi), jadi
tangkapan skrinnya tak berguna kepada sesiapa: ia cuma "sesi apa", dan
server tetap mengira SIAPA daripada token log masuk sebenar peng-imbas.

Nota `payment_status` pada `activity_registrations`: nilainya ialah
`not_required` (aktiviti percuma), `pending`, `paid`, `refunded` — **tiada
`failed`**, berbeza daripada `registration_payments.status`. Jadi kegagalan
bayaran yang dilaporkan gateway TIDAK ditulis ke lajur ini; baris kekal
`pending` dan dibersihkan oleh `internal/activitysweep`. Peristiwa gagal
itu tetap direkod dalam `payment_logs`, yang tiada kekangan sedemikian.

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
| `registration_payments` | yuran pendaftaran ahli SEKALI bayar (ToyyibPay). amount_cents, `gateway_ref` (billcode, **nullable**), status (`pending`/`succeeded`/`failed`). Unik **separa** `(gateway, gateway_ref) where gateway_ref is not null`. Boleh ada >1 baris per ahli (percubaan berulang) |
| `payment_logs` | log PERISTIWA bayaran merentas KETIGA-TIGA modul. `bigserial` (bukan uuid) untuk keyset pagination murah. `raw_payload` ialah **`text`**, bukan `jsonb` |
| `audit_logs` | siapa ubah apa. Delta jsonb + changed_fields, snapshot pelaku (member_id/role sebagai teks). **Append-only dikuatkuasakan trigger** |

Nota `registration_payments.gateway_ref` **nullable** (L29, 2026-08-22):
baris ditulis **sebelum** bil gateway dicipta, jadi ref belum wujud pada
saat INSERT. Susunan itu ialah keseluruhan pembaikan — susunan terbalik
(createBill dahulu) bermakna INSERT yang gagal meninggalkan bil ToyyibPay
SAH yang boleh dibayar tanpa sebarang baris merujuknya, dan webhook
mahupun reconcile tak dapat melihatnya.

Indeks unik mesti **separa** untuk menyokongnya: di bawah indeks penuh,
ahli KEDUA yang checkout akan berlanggar dengan yang pertama sebaik
kedua-duanya membawa ref kosong. NULL tidak pernah berlanggar dengan NULL
dalam indeks unik Postgres, tetapi predikatnya ditulis eksplisit supaya
niat itu boleh dibaca daripada skema.

Baris ber-ref-NULL bermakna createBill GAGAL — ia ditanda `'failed'` oleh
handler dan dilangkau oleh reconcile (`gateway_ref is not null`), kerana
tiada bil untuk ditanya pada gateway.

**Tiga jadual bayaran, satu jadual log.** Amaun disimpan pada baris bayaran
itu sendiri (`donations.amount_cents`, `registration_payments.amount_cents`,
`activity_registrations.fee_cents_paid`) dan **bukan** dibaca hidup daripada
`activities.fee_cents` — resit dijana semula setiap muat turun dan menulis
ganti kunci R2 yang STABIL, jadi tanpa snapshot itu yuran yang ditukar
selepas ahli bayar akan senyap mengubah resit yang sedia wujud.
`fee_cents_paid` nullable: baris lama sebelum migrationnya jatuh balik ke
`coalesce(fee_cents_paid, activities.fee_cents)`.

Nota `payment_logs`: `raw_payload` `text` dan bukan `jsonb` **kerana satu
bug sebenar** — callback ToyyibPay ialah form-urlencoded, bukan JSON, dan
`jsonb` menolak INSERT. Oleh sebab `paymentlog.Record` best-effort dengan
sengaja (log tak boleh menggagalkan laluan bayaran), penolakan itu SENYAP,
jadi payload tak pernah tersimpan untuk TEPAT dua modul yang menjadi sebab
jadual ini dibina. Tiada CHECK pada `event`/`status` (berbeza daripada
`module`, yang set tetapnya bermakna untuk retention/tapisan): ini jadual
observability, bukan state machine — kekangan ketat cuma akan menolak baris
log MASA HADAPAN daripada gateway yang belum wujud.

Kedua-dua `payment_logs.user_id` dan `audit_logs.actor_id` ialah `on delete
set null`: memadam akaun tidak memusnahkan jejak kewangan atau jejak audit.
`payment_logs.related_id` sengaja TIADA foreign key — tiga jadual berlainan
berkongsi lajur itu, dan baris log mesti kekal walaupun baris asalnya tiada.

Retention (`internal/retention`): `payment_logs` 90 hari, PII `audit_logs`
diredaksi 90 hari, catatan `audit_logs` dipadam 365 hari, batu nisan upload
30 hari.

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
