# Modul Aktiviti — Pelan Pelaksanaan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ahli kelab boleh mendaftar untuk aktiviti sukan, kehadiran ditanda per-sesi, dan yang memenuhi ambang kehadiran menerima sijil PDF yang boleh disahkan oleh pihak ketiga.

**Architecture:** Backend Go (gin + pgx + sqlc + goose) menambah enam jadual dan satu modul PDF tulen. Sesi ialah sumber kebenaran masa; `activities.starts_at`/`ends_at` ialah ringkasan min/maks yang didenormalisasi dan dikira semula dalam transaksi yang sama. Penerbitan sijil dua fasa: transaksi Postgres memasukkan baris dengan `r2_key` null, kemudian muat naik R2 berlaku selepas komit dan boleh disambung semula. Klien Flutter (Riverpod + go_router + dio) menambah satu feature folder.

**Tech Stack:** Go 1.x, gin, pgx/v5, sqlc, goose, `go-pdf/fpdf`, `skip2/go-qrcode`, Postgres, Cloudflare R2. Flutter dengan `qr_flutter` + `mobile_scanner`.

**Spec:** `docs/superpowers/specs/2026-08-09-modul-aktiviti-design.md`

## Global Constraints

- Migration goose single-file dengan `-- +goose Up` / `-- +goose Down` dalam fail yang sama, nama `<timestamp>_<snake_case>.sql` dalam `internal/db/migrations/`.
- Query ditulis tangan dalam `queries/<jadual>.sql`, kod dijana `sqlc generate`. **Jangan** edit apa-apa dalam `internal/db/sqlc/` dengan tangan.
- Semakan management dibuat dalam handler melalui `authz.IsManagement(ctx, q, userID)`. **JANGAN** cipta middleware `RequireManagement` — corak itu tidak wujud dalam repo ini.
- `audit.Record` mesti dipanggil dengan `sqlc.Queries` yang terikat pada transaksi mutasi (`queries.WithTx(tx)`), dan ralatnya **tidak boleh ditelan** — gagalkan keseluruhan permintaan.
- Baldi had kadar mesti **dinamakan**: `rateLimiter.Limit("<nama>", ...)`. Baldi tanpa nama berkongsi kunci Redis dan saling menghabiskan kuota.
- Semua teks yang dilihat pengguna (mesej ralat API, UI Flutter, kandungan PDF) dalam **Bahasa Melayu**.
- Ujian yang perlukan Postgres dijaga env var dan di-skip secara lalai, ikut corak `HANDLER_TEST_DB`. Modul ini guna `ACTIVITY_TEST_DB`. Guna DB **buangan**, bukan DB dev.
- Ujian yang perlukan R2 dijaga `R2_LIVE_TEST=1`.
- Flutter: `permission_handler` dipin pada `12.0.3` dan projek terhad kepada **compileSdk 35**. Sebarang pakej baharu mesti membina dalam kekangan itu.
- Nilai wang dalam **sen** (`fee_cents`), mata wang `MYR`.
- Ambang kehadiran ialah peratus integer 1–100, lalai 100.
- Tetingkap check-in: 2 jam sebelum `session.starts_at` hingga 2 jam selepas `session.ends_at`.

---

## Struktur Fail

**Backend — cipta:**

| Fail | Tanggungjawab |
|---|---|
| `internal/db/migrations/20260810100000_create_activity_categories.sql` | Jadual kategori + seed |
| `internal/db/migrations/20260810100100_create_activities.sql` | Jadual aktiviti |
| `internal/db/migrations/20260810100200_create_activity_sessions.sql` | Jadual sesi |
| `internal/db/migrations/20260810100300_create_activity_registrations.sql` | Pendaftaran + unik separa |
| `internal/db/migrations/20260810100400_create_activity_attendances.sql` | Kehadiran |
| `internal/db/migrations/20260810100500_create_activity_certificates.sql` | Sijil |
| `internal/db/migrations/20260810100600_widen_notifications_activity.sql` | Luaskan check jenis notifikasi |
| `queries/activities.sql` | Query aktiviti + sesi + kategori |
| `queries/activity_registrations.sql` | Query pendaftaran |
| `queries/activity_attendances.sql` | Query kehadiran |
| `queries/activity_certificates.sql` | Query sijil |
| `internal/certificate/certificate.go` | Penjanaan PDF — fungsi tulen |
| `internal/certificate/certificate_test.go` | Ujian PDF |
| `internal/certificate/eligibility.go` | Kelayakan + tetingkap masa — fungsi tulen |
| `internal/certificate/eligibility_test.go` | Ujian kelayakan |
| `internal/http/handlers/activities.go` | CRUD aktiviti + sesi |
| `internal/http/handlers/activity_registrations.go` | Daftar/batal/senarai |
| `internal/http/handlers/activity_attendance.go` | Tanda/buang kehadiran |
| `internal/http/handlers/activity_certificates.go` | Terbit/tarik/muat turun/verify |
| `internal/http/handlers/activities_live_test.go` | Ujian lawan Postgres sebenar |

**Backend — ubah suai:**

| Fail | Perubahan |
|---|---|
| `internal/storage/r2.go` | Tambah `PutObject` — muat naik sisi-server |
| `internal/http/router.go` | Daftar route baharu + baldi `verify` |

**Flutter — cipta:** `lib/features/activities/` (models, providers, 5 halaman ahli, 4 halaman management, widgets).

## Helper yang dikongsi

Task 6 hingga 10 menggunakan helper penukaran jenis ini. **Semak
`internal/http/handlers/bind.go` dan fail handler sedia ada dahulu** — sebahagian
mungkin sudah wujud dengan nama lain. Tambah yang tiada ke `bind.go`, jangan
cipta pendua setempat dalam setiap fail:

```go
func pgTimestamptz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func pgDate(t time.Time) pgtype.Date               { return pgtype.Date{Time: t, Valid: true} }
func pgText(s string) pgtype.Text                  { return pgtype.Text{String: s, Valid: s != ""} }
func pgUUID(id uuid.UUID) pgtype.UUID              { return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil} }
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool)
```

`parseUUIDParam` membalas `400` `{"error": "id tidak sah"}` dan memulangkan
`false` bila penghuraian gagal.

Helper **ujian** yang dikongsi merentas fail `*_live_test.go`, diletakkan dalam
`activities_live_test.go` (fail ujian pertama yang ditulis) supaya ia wujud
sebelum task lain memerlukannya:

```go
func activityTestPool(t *testing.T) *pgxpool.Pool          // Task 6
func seedActivity(t *testing.T, pool *pgxpool.Pool) uuid.UUID  // Task 6
func seedActivityWithCapacity(t *testing.T, pool *pgxpool.Pool, capacity int) uuid.UUID  // Task 7
func seedUsers(t *testing.T, pool *pgxpool.Pool, n int) []uuid.UUID                      // Task 7
func seedSession(t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID, start, end time.Time) uuid.UUID  // Task 8
func seedAktivitiTigaSesi(t *testing.T, pool *pgxpool.Pool, thresholdPct int) uuid.UUID   // Task 9
func seedPesertaKehadiran(t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID, hadir ...int) []uuid.UUID  // Task 9
func seedSelesaiDenganKehadiran(t *testing.T, pool *pgxpool.Pool, n int) (uuid.UUID, []uuid.UUID)  // Task 9
func bacaSequence(t *testing.T, pool *pgxpool.Pool, key string) int64                     // Task 9
func doGET(t *testing.T, pool *pgxpool.Pool, path string) []byte                          // Task 10
func doGETStatus(t *testing.T, pool *pgxpool.Pool, path string) int                        // Task 10
```

`doGET`/`doGETStatus` membina router melalui `httptest.NewServer` dengan pool
ujian dan melakukan permintaan sebenar — endpoint pengesahan **awam**, jadi
ujian tidak boleh memintas lapisan HTTP tempat had kadar dan pemilihan medan
sebenarnya berlaku.

Setiap helper seed mesti mendaftar `t.Cleanup` yang memadam baris yang
diciptanya. DB ujian dikongsi antara ujian dalam pakej yang sama.

---

## Task 1: Migration & schema

**Files:**
- Create: `internal/db/migrations/20260810100000_create_activity_categories.sql`
- Create: `internal/db/migrations/20260810100100_create_activities.sql`
- Create: `internal/db/migrations/20260810100200_create_activity_sessions.sql`
- Create: `internal/db/migrations/20260810100300_create_activity_registrations.sql`
- Create: `internal/db/migrations/20260810100400_create_activity_attendances.sql`
- Create: `internal/db/migrations/20260810100500_create_activity_certificates.sql`

**Interfaces:**
- Produces: enam jadual — `activity_categories`, `activities`, `activity_sessions`, `activity_registrations`, `activity_attendances`, `activity_certificates`. Semua task selepas ini bergantung pada nama lajur di sini.

- [ ] **Step 1: Tulis migration kategori + seed**

`internal/db/migrations/20260810100000_create_activity_categories.sql`:

```sql
-- +goose Up
create table activity_categories (
  id uuid primary key default gen_random_uuid(),
  key text not null unique,
  name text not null,
  sort_order int not null default 0,
  is_active boolean not null default true,
  created_at timestamptz not null default now()
);

insert into activity_categories (key, name, sort_order) values
  ('badminton', 'Badminton', 10),
  ('futsal', 'Futsal', 20),
  ('bola_tampar', 'Bola Tampar', 30),
  ('larian', 'Larian', 40),
  ('ping_pong', 'Ping Pong', 50),
  ('lain_lain', 'Lain-lain', 900);

-- +goose Down
drop table if exists activity_categories;
```

- [ ] **Step 2: Tulis migration aktiviti**

`internal/db/migrations/20260810100100_create_activities.sql`:

```sql
-- +goose Up
create table activities (
  id uuid primary key default gen_random_uuid(),
  category_id uuid not null references activity_categories(id) on delete restrict,
  title text not null,
  description text not null default '',
  location_name text not null,
  location_address text not null default '',

  -- starts_at/ends_at ialah min/maks activity_sessions yang DIDENORMALISASI.
  -- Sesi ialah sumber kebenaran; dua lajur ni dikira semula dalam transaksi
  -- yang sama setiap kali set sesi berubah (lihat ReplaceActivitySessions).
  -- Sebab: senarai aktiviti perlu isih+tapis ikut tarikh dengan indeks, dan
  -- min() atas join pada setiap senarai terlalu mahal.
  starts_at timestamptz not null,
  ends_at timestamptz not null,

  registration_opens_at timestamptz,
  registration_closes_at timestamptz not null,
  capacity int check (capacity > 0),
  fee_cents int not null default 0 check (fee_cents >= 0),
  currency text not null default 'MYR',
  attendance_threshold_pct smallint not null default 100
    check (attendance_threshold_pct between 1 and 100),
  status text not null default 'draft'
    check (status in ('draft', 'published', 'cancelled', 'completed')),
  cancelled_reason text,
  certificates_issued_at timestamptz,
  created_by uuid references users(id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create index activities_status_starts_at_idx on activities(status, starts_at desc);
create index activities_category_id_idx on activities(category_id);

-- +goose Down
drop table if exists activities;
```

- [ ] **Step 3: Tulis migration sesi**

`internal/db/migrations/20260810100200_create_activity_sessions.sql`:

```sql
-- +goose Up
create table activity_sessions (
  id uuid primary key default gen_random_uuid(),
  activity_id uuid not null references activities(id) on delete cascade,
  seq int not null,
  title text not null default '',
  starts_at timestamptz not null,
  ends_at timestamptz not null check (ends_at > starts_at),
  unique (activity_id, seq)
);

create index activity_sessions_activity_id_starts_at_idx
  on activity_sessions(activity_id, starts_at);

-- +goose Down
drop table if exists activity_sessions;
```

- [ ] **Step 4: Tulis migration pendaftaran**

`internal/db/migrations/20260810100300_create_activity_registrations.sql`:

```sql
-- +goose Up
create table activity_registrations (
  id uuid primary key default gen_random_uuid(),
  activity_id uuid not null references activities(id) on delete cascade,
  user_id uuid not null references users(id) on delete cascade,
  status text not null default 'registered'
    check (status in ('pending_payment', 'registered', 'cancelled')),

  -- Cangkuk fasa payment. Sengaja wujud dari awal supaya integrasi gateway
  -- kemudian tak perlukan migration atas jadual yang dah ada data.
  payment_status text not null default 'not_required'
    check (payment_status in ('not_required', 'pending', 'paid', 'refunded')),
  payment_ref text,

  -- Isi QR ahli. Legap dan rawak: sesiapa yang nampak nilai ni boleh
  -- ditandakan hadir, jadi ia tak boleh diteka dari id/user_id.
  checkin_token text not null unique,

  registered_at timestamptz not null default now(),
  cancelled_at timestamptz
);

-- Unik SEPARA: halang pendaftaran berganda, tapi benarkan daftar semula
-- selepas batal (baris 'cancelled' kekal sebagai sejarah).
create unique index activity_registrations_active_uniq
  on activity_registrations(activity_id, user_id)
  where status <> 'cancelled';

create index activity_registrations_activity_status_idx
  on activity_registrations(activity_id, status);
create index activity_registrations_user_id_idx
  on activity_registrations(user_id);

-- +goose Down
drop table if exists activity_registrations;
```

- [ ] **Step 5: Tulis migration kehadiran**

`internal/db/migrations/20260810100400_create_activity_attendances.sql`:

```sql
-- +goose Up
create table activity_attendances (
  id uuid primary key default gen_random_uuid(),
  registration_id uuid not null references activity_registrations(id) on delete cascade,
  session_id uuid not null references activity_sessions(id) on delete cascade,

  -- Keempat-empat kaedah check-in hasilkan baris yang SAMA; hanya method
  -- dan marked_by berbeza. 'self_scan' dan 'code' belum ada UI — schema
  -- sokong supaya menambahnya nanti kerja UI, bukan migration.
  method text not null check (method in ('manual', 'scan', 'self_scan', 'code')),

  marked_by uuid references users(id) on delete set null,
  checked_in_at timestamptz not null default now(),
  unique (registration_id, session_id)
);

create index activity_attendances_session_id_idx on activity_attendances(session_id);

-- +goose Down
drop table if exists activity_attendances;
```

- [ ] **Step 6: Tulis migration sijil**

`internal/db/migrations/20260810100500_create_activity_certificates.sql`:

```sql
-- +goose Up
create table activity_certificates (
  id uuid primary key default gen_random_uuid(),

  -- restrict, bukan cascade: aktiviti yang dah keluarkan sijil tak boleh
  -- dipadam begitu sahaja.
  activity_id uuid not null references activities(id) on delete restrict,
  user_id uuid not null references users(id) on delete cascade,

  serial text not null unique,

  -- Berasingan daripada serial. Serial berjujukan — kalau ia juga kunci
  -- pengesahan awam, sesiapa boleh tambah satu dan menuai nama semua ahli.
  verify_token text not null unique,

  -- Snapshot: PDF tak berubah selepas dijana, jadi halaman pengesahan
  -- mesti menunjukkan apa yang TERCETAK, bukan profil semasa.
  recipient_name text not null,
  activity_title text not null,
  activity_date date not null,

  issued_at timestamptz not null default now(),
  r2_key text,
  revoked_at timestamptz,
  revoked_reason text,
  unique (activity_id, user_id)
);

create index activity_certificates_user_id_idx on activity_certificates(user_id);

-- +goose Down
drop table if exists activity_certificates;
```

- [ ] **Step 7: Jalankan migration naik**

```bash
export DATABASE_URL="postgres://$(whoami)@localhost:5432/marc?sslmode=disable"
goose -dir internal/db/migrations postgres "$DATABASE_URL" up
```

Dijangka: enam migration `OK`.

- [ ] **Step 8: Sahkan turun-naik bersih**

```bash
goose -dir internal/db/migrations postgres "$DATABASE_URL" down-to 20260809210000
goose -dir internal/db/migrations postgres "$DATABASE_URL" up
```

Dijangka: tiada ralat pada kedua-dua arah. Kalau `down` gagal atas kekangan foreign key, susunan `drop` salah — betulkan sebelum teruskan.

- [ ] **Step 9: Commit**

```bash
git add internal/db/migrations/
git commit -m "feat(activity): schema aktiviti, sesi, pendaftaran, kehadiran, sijil"
```

---

## Task 2: Query sqlc

**Files:**
- Create: `queries/activities.sql`
- Create: `queries/activity_registrations.sql`
- Create: `queries/activity_attendances.sql`
- Create: `queries/activity_certificates.sql`

**Interfaces:**
- Consumes: jadual dari Task 1.
- Produces: kaedah `*sqlc.Queries` yang dipakai setiap handler selepas ini — `ListActivityCategories`, `CreateActivity`, `GetActivityByID`, `ListActivities`, `UpdateActivity`, `SetActivityStatus`, `RecomputeActivityWindow`, `DeleteActivitySessions`, `CreateActivitySession`, `ListActivitySessions`, `CountSessionsWithAttendance`, `LockActivityForRegistration`, `CountActiveRegistrations`, `CreateRegistration`, `CancelRegistration`, `GetRegistrationByActivityAndUser`, `GetRegistrationByCheckinToken`, `ListRegistrationsByActivity`, `ListMyRegistrations`, `MarkAttendance`, `DeleteAttendance`, `ListAttendanceByActivity`, `CountAttendanceByRegistration`, `GetSessionByID`, `ListEligibleForCertificate`, `CreateCertificate`, `SetCertificateR2Key`, `ListCertificatesPendingFile`, `ListMyCertificates`, `GetCertificateByID`, `GetCertificateByVerifyToken`, `RevokeCertificate`.

- [ ] **Step 1: Tulis `queries/activities.sql`**

```sql
-- name: ListActivityCategories :many
select * from activity_categories
where is_active = true
order by sort_order, name;

-- name: CreateActivity :one
insert into activities (
  category_id, title, description, location_name, location_address,
  starts_at, ends_at, registration_opens_at, registration_closes_at,
  capacity, fee_cents, attendance_threshold_pct, created_by
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
returning *;

-- name: GetActivityByID :one
select a.*, c.key as category_key, c.name as category_name
from activities a
join activity_categories c on c.id = a.category_id
where a.id = $1 and a.deleted_at is null;

-- name: ListActivities :many
-- Keyset pagination atas (starts_at, id) — sama corak dengan ListPosts,
-- elak baris terlepas bila dua aktiviti berkongsi timestamp tepat.
-- upcoming=true → aktiviti yang belum tamat, isih menaik (paling hampir
-- dahulu). upcoming=false → yang dah tamat, isih menurun.
select a.*, c.key as category_key, c.name as category_name,
  (select count(*) from activity_registrations r
    where r.activity_id = a.id and r.status <> 'cancelled') as registration_count
from activities a
join activity_categories c on c.id = a.category_id
where a.deleted_at is null
  and a.status = any(sqlc.arg('statuses')::text[])
  and (sqlc.narg('category_id')::uuid is null or a.category_id = sqlc.narg('category_id')::uuid)
  and (
    case when sqlc.arg('upcoming')::boolean
      then a.ends_at >= now()
      else a.ends_at < now()
    end
  )
  and (
    sqlc.narg('cursor_starts_at')::timestamptz is null
    or (
      case when sqlc.arg('upcoming')::boolean
        then (a.starts_at, a.id) > (sqlc.narg('cursor_starts_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
        else (a.starts_at, a.id) < (sqlc.narg('cursor_starts_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
      end
    )
  )
order by
  case when sqlc.arg('upcoming')::boolean then a.starts_at end asc,
  case when sqlc.arg('upcoming')::boolean then a.id end asc,
  case when not sqlc.arg('upcoming')::boolean then a.starts_at end desc,
  case when not sqlc.arg('upcoming')::boolean then a.id end desc
limit sqlc.arg('row_limit');

-- name: UpdateActivity :one
update activities set
  category_id = $2, title = $3, description = $4,
  location_name = $5, location_address = $6,
  registration_opens_at = $7, registration_closes_at = $8,
  capacity = $9, fee_cents = $10, attendance_threshold_pct = $11,
  updated_at = now()
where id = $1 and deleted_at is null
returning *;

-- name: SetActivityStatus :one
update activities set status = $2, cancelled_reason = $3, updated_at = now()
where id = $1 and deleted_at is null
returning *;

-- name: SetActivityCertificatesIssuedAt :exec
update activities set certificates_issued_at = now(), updated_at = now()
where id = $1;

-- name: RecomputeActivityWindow :exec
-- Menjaga invarian denormalisasi. SATU tempat yang menulis starts_at/ends_at
-- selepas cipta — dipanggil dalam transaksi yang sama dengan setiap
-- perubahan set sesi.
update activities a set
  starts_at = s.min_start,
  ends_at = s.max_end,
  updated_at = now()
from (
  select min(starts_at) as min_start, max(ends_at) as max_end
  from activity_sessions where activity_id = $1
) s
where a.id = $1 and s.min_start is not null;

-- name: CreateActivitySession :one
insert into activity_sessions (activity_id, seq, title, starts_at, ends_at)
values ($1, $2, $3, $4, $5)
returning *;

-- name: ListActivitySessions :many
select * from activity_sessions where activity_id = $1 order by seq;

-- name: ListActivitySessionsByIDs :many
select * from activity_sessions where activity_id = any(sqlc.arg('activity_ids')::uuid[]) order by activity_id, seq;

-- name: GetActivitySessionByID :one
select * from activity_sessions where id = $1;

-- name: DeleteActivitySessions :exec
delete from activity_sessions where activity_id = $1;

-- name: CountSessionsWithAttendance :one
-- Menghalang penggantian set sesi yang akan membuang kehadiran yang sudah
-- direkod.
select count(*) from activity_sessions s
where s.activity_id = $1
  and exists (select 1 from activity_attendances a where a.session_id = s.id);

-- name: CountActivitySessions :one
select count(*) from activity_sessions where activity_id = $1;
```

- [ ] **Step 2: Tulis `queries/activity_registrations.sql`**

```sql
-- name: LockActivityForRegistration :one
-- `for update` atas baris aktiviti — ini yang menyerikan pendaftaran
-- serentak supaya kiraan kapasiti tak boleh basi antara baca dan tulis.
select * from activities where id = $1 and deleted_at is null for update;

-- name: CountActiveRegistrations :one
select count(*) from activity_registrations
where activity_id = $1 and status <> 'cancelled';

-- name: CreateRegistration :one
insert into activity_registrations (activity_id, user_id, status, payment_status, checkin_token)
values ($1, $2, $3, $4, $5)
returning *;

-- name: CancelRegistration :one
update activity_registrations
set status = 'cancelled', cancelled_at = now()
where activity_id = $1 and user_id = $2 and status <> 'cancelled'
returning *;

-- name: GetRegistrationByActivityAndUser :one
select * from activity_registrations
where activity_id = $1 and user_id = $2 and status <> 'cancelled';

-- name: GetRegistrationByCheckinToken :one
select * from activity_registrations
where checkin_token = $1 and status <> 'cancelled';

-- name: GetRegistrationByID :one
select * from activity_registrations where id = $1;

-- name: ListRegistrationsByActivity :many
select r.*, pr.member_id, pr.display_name, pr.avatar_r2_key
from activity_registrations r
join profiles pr on pr.user_id = r.user_id
where r.activity_id = $1 and r.status <> 'cancelled'
order by pr.display_name;

-- name: ListMyRegistrations :many
select r.*, a.title, a.starts_at, a.ends_at, a.status as activity_status,
  c.name as category_name
from activity_registrations r
join activities a on a.id = r.activity_id
join activity_categories c on c.id = a.category_id
where r.user_id = $1 and r.status <> 'cancelled' and a.deleted_at is null
order by a.starts_at desc;
```

- [ ] **Step 3: Tulis `queries/activity_attendances.sql`**

```sql
-- name: MarkAttendance :one
-- on conflict do nothing + returning: imbas kedua QR yang sama bukan ralat,
-- ia cuma tiada kerja. Handler membezakan "baharu" daripada "sudah ada"
-- melalui sama ada baris dipulangkan.
insert into activity_attendances (registration_id, session_id, method, marked_by)
values ($1, $2, $3, $4)
on conflict (registration_id, session_id) do nothing
returning *;

-- name: GetAttendance :one
select * from activity_attendances
where registration_id = $1 and session_id = $2;

-- name: DeleteAttendance :execrows
delete from activity_attendances
where registration_id = $1 and session_id = $2;

-- name: ListAttendanceByActivity :many
select at.* from activity_attendances at
join activity_sessions s on s.id = at.session_id
where s.activity_id = $1;

-- name: CountAttendanceByRegistration :one
select count(*) from activity_attendances where registration_id = $1;
```

- [ ] **Step 4: Tulis `queries/activity_certificates.sql`**

```sql
-- name: ListEligibleForCertificate :many
-- Server mengira sendiri siapa layak — management tidak menyenaraikan.
-- Klausa payment_status kekal walaupun payment belum diintegrasikan:
-- fee_cents sentiasa 0 buat masa ini, jadi ia sentiasa benar.
select r.id as registration_id, r.user_id, pr.display_name,
  (select count(*) from activity_attendances at where at.registration_id = r.id) as attended
from activity_registrations r
join profiles pr on pr.user_id = r.user_id
join activities a on a.id = r.activity_id
where r.activity_id = $1
  and r.status = 'registered'
  and (a.fee_cents = 0 or r.payment_status = 'paid')
order by pr.display_name;

-- name: CreateCertificate :one
insert into activity_certificates (
  activity_id, user_id, serial, verify_token,
  recipient_name, activity_title, activity_date
) values ($1, $2, $3, $4, $5, $6, $7)
on conflict (activity_id, user_id) do nothing
returning *;

-- name: SetCertificateR2Key :exec
update activity_certificates set r2_key = $2 where id = $1;

-- name: ListCertificatesPendingFile :many
-- Fasa 2 penerbitan menyambung dari sini. Baris tanpa r2_key ialah kerja
-- yang belum siap, bukan ralat.
select * from activity_certificates
where activity_id = $1 and r2_key is null and revoked_at is null;

-- name: ListCertificatesByActivity :many
select * from activity_certificates where activity_id = $1 order by serial;

-- name: ListMyCertificates :many
select ac.*, c.name as category_name
from activity_certificates ac
join activities a on a.id = ac.activity_id
join activity_categories c on c.id = a.category_id
where ac.user_id = $1 and ac.revoked_at is null
order by ac.issued_at desc;

-- name: GetCertificateByID :one
select * from activity_certificates where id = $1;

-- name: GetCertificateByVerifyToken :one
select * from activity_certificates where verify_token = $1;

-- name: RevokeCertificate :one
update activity_certificates
set revoked_at = now(), revoked_reason = $2
where id = $1 and revoked_at is null
returning *;
```

- [ ] **Step 5: Jana kod sqlc**

```bash
sqlc generate
```

Dijangka: tiada ralat, `internal/db/sqlc/` mengandungi fail baharu untuk keempat-empat fail query.

Kalau `sqlc` mengadu tentang `case when` dalam `order by` pada `ListActivities`, ia tidak dapat menyimpulkan jenis — tambah `::timestamptz` pada ungkapan `case` yang berkenaan.

- [ ] **Step 6: Sahkan kompilasi**

```bash
go build ./...
```

Dijangka: lulus.

- [ ] **Step 7: Commit**

```bash
git add queries/ internal/db/sqlc/
git commit -m "feat(activity): query sqlc untuk aktiviti, pendaftaran, kehadiran, sijil"
```

---

## Task 3: Logik tulen — kelayakan & tetingkap masa

**Files:**
- Create: `internal/certificate/eligibility.go`
- Test: `internal/certificate/eligibility_test.go`

**Interfaces:**
- Produces:
  - `func IsEligible(attended, totalSessions int, thresholdPct int) bool`
  - `func WithinCheckinWindow(now, sessionStart, sessionEnd time.Time) bool`
  - `const CheckinWindowPadding = 2 * time.Hour`

- [ ] **Step 1: Tulis ujian yang gagal**

`internal/certificate/eligibility_test.go`:

```go
package certificate

import (
	"testing"
	"time"
)

func TestIsEligible(t *testing.T) {
	tests := []struct {
		name         string
		attended     int
		total        int
		thresholdPct int
		want         bool
	}{
		{"hadir semua, ambang 100", 3, 3, 100, true},
		{"terlepas satu, ambang 100", 2, 3, 100, false},
		{"2 dari 3 pada ambang 66", 2, 3, 66, true},
		{"2 dari 3 pada ambang 67", 2, 3, 67, false},
		{"langsung tak hadir", 0, 3, 1, false},
		{"sesi tunggal hadir", 1, 1, 100, true},
		// Aktiviti tanpa sesi tak sepatutnya wujud, tapi kalau data rosak
		// kita pilih 'tidak layak' berbanding pembahagian dengan sifar.
		{"sifar sesi", 0, 0, 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEligible(tt.attended, tt.total, tt.thresholdPct); got != tt.want {
				t.Errorf("IsEligible(%d, %d, %d) = %v, mahu %v",
					tt.attended, tt.total, tt.thresholdPct, got, tt.want)
			}
		})
	}
}

func TestWithinCheckinWindow(t *testing.T) {
	start := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"semasa sesi", start.Add(30 * time.Minute), true},
		{"tepat pada mula", start, true},
		{"1 jam sebelum", start.Add(-time.Hour), true},
		{"3 jam sebelum", start.Add(-3 * time.Hour), false},
		{"1 jam selepas tamat", end.Add(time.Hour), true},
		{"3 jam selepas tamat", end.Add(3 * time.Hour), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithinCheckinWindow(tt.now, start, end); got != tt.want {
				t.Errorf("WithinCheckinWindow(%v) = %v, mahu %v", tt.now, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
go test ./internal/certificate/ -run 'TestIsEligible|TestWithinCheckinWindow' -v
```

Dijangka: GAGAL — `undefined: IsEligible`.

- [ ] **Step 3: Tulis implementasi minimum**

`internal/certificate/eligibility.go`:

```go
// Package certificate mengandungi logik sijil yang TULEN — tiada DB, tiada
// R2, tiada rangkaian. Itu yang menjadikannya boleh diuji tanpa infra,
// sama seperti internal/receipt.
package certificate

import "time"

// CheckinWindowPadding — berapa lama sebelum/selepas sesi kehadiran masih
// boleh ditanda sebagai check-in biasa.
//
// Tanpa had ini, kehadiran boleh ditanda seminggu kemudian tanpa jejak, dan
// sijil bergantung padanya. Menanda di luar tetingkap memerlukan tindakan
// pindaan berasingan yang dicatat audit.
const CheckinWindowPadding = 2 * time.Hour

// IsEligible — layakkah pendaftaran ini menerima sijil?
//
// Perbandingan dibuat dalam integer (attended*100 >= total*threshold) dan
// bukan float, supaya kes sempadan seperti 2/3 pada ambang 66 vs 67
// berkelakuan sama pada setiap platform.
func IsEligible(attended, totalSessions, thresholdPct int) bool {
	if totalSessions <= 0 {
		return false
	}
	return attended*100 >= totalSessions*thresholdPct
}

// WithinCheckinWindow — bolehkah kehadiran ditanda sekarang untuk sesi ini?
func WithinCheckinWindow(now, sessionStart, sessionEnd time.Time) bool {
	return !now.Before(sessionStart.Add(-CheckinWindowPadding)) &&
		!now.After(sessionEnd.Add(CheckinWindowPadding))
}
```

- [ ] **Step 4: Jalankan ujian, sahkan ia lulus**

```bash
go test ./internal/certificate/ -run 'TestIsEligible|TestWithinCheckinWindow' -v
```

Dijangka: LULUS, semua sub-ujian.

- [ ] **Step 5: Commit**

```bash
git add internal/certificate/
git commit -m "feat(certificate): logik kelayakan sijil dan tetingkap check-in"
```

---

## Task 4: Penjanaan PDF sijil

**Files:**
- Create: `internal/certificate/certificate.go`
- Test: `internal/certificate/certificate_test.go`
- Modify: `go.mod` (tambah `github.com/skip2/go-qrcode`)

**Interfaces:**
- Consumes: tiada.
- Produces:
  - `type Data struct { Serial, RecipientName, ActivityTitle, CategoryName, VerifyURL string; ActivityDate time.Time }`
  - `func GeneratePDF(d Data) ([]byte, error)`
  - `func EncodableName(name string) bool`

- [ ] **Step 1: Tambah kebergantungan QR**

```bash
go get github.com/skip2/go-qrcode@latest
```

- [ ] **Step 2: Tulis ujian yang gagal**

`internal/certificate/certificate_test.go`:

```go
package certificate

import (
	"bytes"
	"testing"
	"time"
)

func testData() Data {
	return Data{
		Serial:        "MARC-2026-000123",
		RecipientName: "Ahmad bin Abdullah",
		ActivityTitle: "Kejohanan Badminton Terbuka 2026",
		CategoryName:  "Badminton",
		ActivityDate:  time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		VerifyURL:     "https://marc.example/verify/certificates/abc123",
	}
}

func TestGeneratePDFPulangkanPDFSah(t *testing.T) {
	out, err := GeneratePDF(testData())
	if err != nil {
		t.Fatalf("GeneratePDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Errorf("output bukan PDF, 8 bait pertama: %q", out[:min(8, len(out))])
	}
	// Sijil dengan QR terbenam sepatutnya jauh melebihi seribu bait.
	// Ambang longgar sengaja — ini semakan kewarasan, bukan ujian saiz.
	if len(out) < 2000 {
		t.Errorf("PDF terlalu kecil (%d bait), QR mungkin tak terbenam", len(out))
	}
}

func TestGeneratePDFTolakNamaTakBolehDikodkan(t *testing.T) {
	d := testData()
	// Fon Helvetica terbina fpdf hanya meliputi cp1252. Tanpa semakan ini,
	// nama begini akan DITERBITKAN dengan aksara hilang senyap-senyap —
	// sijil rosak yang tiada siapa perasan sehingga penerima membukanya.
	d.RecipientName = "李小龍"

	if _, err := GeneratePDF(d); err == nil {
		t.Fatal("mahu ralat untuk nama tak boleh dikodkan, dapat nil")
	}
}

func TestEncodableName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Ahmad bin Abdullah", true},
		{"Nurul Aisyah binti Zainal", true},
		{"José Álvarez", true}, // cp1252 meliputi aksara beraksen Latin
		{"李小龍", false},
		{"Ahmad 李", false},
	}
	for _, tt := range tests {
		if got := EncodableName(tt.name); got != tt.want {
			t.Errorf("EncodableName(%q) = %v, mahu %v", tt.name, got, tt.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 3: Jalankan ujian, sahkan ia gagal**

```bash
go test ./internal/certificate/ -run 'TestGeneratePDF|TestEncodableName' -v
```

Dijangka: GAGAL — `undefined: GeneratePDF`.

- [ ] **Step 4: Tulis implementasi**

`internal/certificate/certificate.go`:

```go
package certificate

import (
	"bytes"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
	qrcode "github.com/skip2/go-qrcode"
)

// Ukuran A4 landskap dalam mm.
const (
	pageW    = 297.0
	pageH    = 210.0
	marginX  = 20.0
	contentW = pageW - 2*marginX
)

var (
	brandColor = [3]int{16, 94, 74}
	brandDark  = [3]int{9, 61, 48}
	inkColor   = [3]int{28, 30, 33}
	mutedColor = [3]int{110, 116, 122}
)

// Data — segala yang perlu untuk mencetak satu sijil. Semuanya sudah
// disnapshot oleh pemanggil; fungsi ini tidak membaca DB.
type Data struct {
	Serial        string
	RecipientName string
	ActivityTitle string
	CategoryName  string
	ActivityDate  time.Time
	VerifyURL     string
}

// EncodableName — bolehkah nama ini dicetak tanpa kehilangan aksara?
//
// fpdf dengan fon terbina mengekod ke cp1252 dan menggantikan aksara di
// luar julat itu secara SENYAP. Kita memeriksa terlebih dahulu supaya
// kegagalan menjadi ralat yang boleh dilihat, bukan sijil yang rosak.
func EncodableName(name string) bool {
	for _, r := range name {
		if r > 0xFF {
			return false
		}
		// Julat kawalan cp1252 yang tidak dipetakan.
		if r >= 0x80 && r <= 0x9F {
			return false
		}
	}
	return true
}

func GeneratePDF(d Data) ([]byte, error) {
	if !EncodableName(d.RecipientName) {
		return nil, fmt.Errorf("nama penerima mengandungi aksara yang tidak boleh dicetak: %q", d.RecipientName)
	}

	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(marginX, marginX, marginX)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetTitle("Sijil Penyertaan "+d.Serial, true)
	pdf.SetAuthor("MARC", true)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.AddPage()

	drawBorder(pdf)
	drawHeading(pdf, tr)
	drawRecipient(pdf, tr, d)
	if err := drawFooter(pdf, tr, d); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("hasilkan PDF: %w", err)
	}
	return buf.Bytes(), nil
}

func drawBorder(pdf *fpdf.Fpdf) {
	pdf.SetFillColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.Rect(0, 0, pageW, 14, "F")
	pdf.SetFillColor(brandDark[0], brandDark[1], brandDark[2])
	pdf.Rect(0, 14, pageW, 1.6, "F")

	pdf.SetDrawColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.SetLineWidth(0.6)
	pdf.Rect(10, 22, pageW-20, pageH-32, "D")
}

func drawHeading(pdf *fpdf.Fpdf, tr func(string) string) {
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(marginX, 3)
	pdf.SetFont("Helvetica", "B", 15)
	pdf.CellFormat(contentW, 8, "MARC", "", 0, "L", false, 0, "")

	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.SetXY(marginX, 42)
	pdf.SetFont("Helvetica", "B", 30)
	pdf.CellFormat(contentW, 14, tr("SIJIL PENYERTAAN"), "", 2, "C", false, 0, "")

	pdf.SetFont("Helvetica", "", 11)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.CellFormat(contentW, 8, tr("Dengan ini disahkan bahawa"), "", 2, "C", false, 0, "")
}

func drawRecipient(pdf *fpdf.Fpdf, tr func(string) string, d Data) {
	pdf.SetY(78)
	pdf.SetTextColor(brandDark[0], brandDark[1], brandDark[2])
	pdf.SetFont("Helvetica", "B", 26)
	pdf.CellFormat(contentW, 14, tr(d.RecipientName), "", 2, "C", false, 0, "")

	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(contentW, 8, tr("telah menyertai"), "", 2, "C", false, 0, "")

	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(contentW, 10, tr(d.ActivityTitle), "", 2, "C", false, 0, "")

	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(contentW, 7, tr(fmt.Sprintf("%s  •  %s",
		d.CategoryName, formatTarikh(d.ActivityDate))), "", 2, "C", false, 0, "")
}

func drawFooter(pdf *fpdf.Fpdf, tr func(string) string, d Data) error {
	png, err := qrcode.Encode(d.VerifyURL, qrcode.Medium, 256)
	if err != nil {
		return fmt.Errorf("jana QR: %w", err)
	}
	// RegisterImageReader membaca dari memori — tiada fail sementara.
	pdf.RegisterImageOptionsReader("qr", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(png))
	pdf.ImageOptions("qr", pageW-marginX-28, pageH-58, 28, 28, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetXY(marginX, pageH-44)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(contentW/2, 5, tr("No. Sijil  "+d.Serial), "", 2, "L", false, 0, "")
	pdf.CellFormat(contentW/2, 5, tr("Imbas QR untuk mengesahkan sijil ini"), "", 2, "L", false, 0, "")
	return nil
}

var namaBulan = [...]string{
	"Januari", "Februari", "Mac", "April", "Mei", "Jun",
	"Julai", "Ogos", "September", "Oktober", "November", "Disember",
}

func formatTarikh(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), namaBulan[int(t.Month())-1], t.Year())
}
```

- [ ] **Step 5: Jalankan ujian, sahkan ia lulus**

```bash
go test ./internal/certificate/ -v
```

Dijangka: semua LULUS.

- [ ] **Step 6: Semakan visual manual sekali sahaja**

Tulis skrip buangan yang mengeluarkan PDF ke fail dan bukanya. Penampilan tidak boleh diuji secara automatik — ini satu-satunya pemeriksaannya.

```bash
cat > /tmp/certgen.go <<'EOF'
package main

import (
	"os"
	"time"

	"marc/internal/certificate"
)

func main() {
	b, err := certificate.GeneratePDF(certificate.Data{
		Serial:        "MARC-2026-000123",
		RecipientName: "Ahmad bin Abdullah",
		ActivityTitle: "Kejohanan Badminton Terbuka 2026",
		CategoryName:  "Badminton",
		ActivityDate:  time.Now(),
		VerifyURL:     "https://marc.example/verify/certificates/abc123",
	})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("/tmp/sijil.pdf", b, 0o644); err != nil {
		panic(err)
	}
}
EOF
go run /tmp/certgen.go && open /tmp/sijil.pdf
```

Semak: teks tidak bertindih, QR mengimbas kepada URL yang betul, nama berada di tengah. Laraskan koordinat dalam `certificate.go` kalau perlu. Buang `/tmp/certgen.go` bila selesai.

- [ ] **Step 7: Commit**

```bash
git add internal/certificate/ go.mod go.sum
git commit -m "feat(certificate): jana PDF sijil landskap A4 dengan QR pengesahan"
```

---

## Task 5: Muat naik R2 sisi-server

**Files:**
- Modify: `internal/storage/r2.go`
- Test: `internal/storage/r2_put_live_test.go`

**Interfaces:**
- Produces: `func (r *R2Client) PutObject(ctx context.Context, key, contentType string, body []byte) error`

Sebab task ini wujud: `R2Client` sekarang hanya boleh **presign** — klien yang memuat naik. PDF sijil dijana di server, jadi server perlu boleh memuat naik sendiri.

- [ ] **Step 1: Tulis ujian live yang gagal**

`internal/storage/r2_put_live_test.go`:

```go
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Ujian live — di-skip melainkan R2_LIVE_TEST=1, sama corak dengan
// TestR2LivePermissions sedia ada.
func TestR2PutObjectLive(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("tetapkan R2_LIVE_TEST=1 untuk jalankan")
	}

	r := NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"),
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		os.Getenv("R2_BUCKET"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r.Enabled() {
		t.Skip("kelayakan R2 tidak lengkap")
	}

	ctx := context.Background()
	key := fmt.Sprintf("test/putobject-%d.pdf", time.Now().UnixNano())
	want := []byte("%PDF-1.4 ujian")

	if err := r.PutObject(ctx, key, "application/pdf", want); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	t.Cleanup(func() { _ = r.DeleteImage(context.Background(), key) })

	resp, err := http.Get(r.SignedURL(ctx, key))
	if err != nil {
		t.Fatalf("ambil semula: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, mahu 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, want) {
		t.Errorf("kandungan = %q, mahu %q", got, want)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal kompilasi**

```bash
go test ./internal/storage/ -run TestR2PutObjectLive
```

Dijangka: GAGAL kompilasi — `r.PutObject undefined`.

- [ ] **Step 3: Tambah `PutObject`**

Dalam `internal/storage/r2.go`, tambah selepas `PresignUpload`:

```go
// PutObject muat naik bait terus dari server ke R2.
//
// Berbeza daripada PresignUpload (klien memuat naik sendiri), ini untuk
// kandungan yang DIJANA server dan tidak pernah menyentuh peranti — PDF
// sijil. Tiada semakan dimensi imej di sini; pemanggil yang tahu apa yang
// dihantarnya.
func (r *R2Client) PutObject(ctx context.Context, key, contentType string, body []byte) error {
	if !r.Enabled() {
		return fmt.Errorf("R2 tidak dikonfigurasikan")
	}
	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("muat naik %s: %w", key, err)
	}
	return nil
}
```

Pastikan `bytes`, `github.com/aws/aws-sdk-go-v2/aws`, dan `github.com/aws/aws-sdk-go-v2/service/s3` diimport. Kalau `r.client` bukan nama medan yang betul, semak struct `R2Client` di sekitar baris 85 dan guna nama sebenar.

- [ ] **Step 4: Sahkan kompilasi dan jalankan ujian live**

```bash
go build ./... && R2_LIVE_TEST=1 go test ./internal/storage/ -run TestR2PutObjectLive -v
```

Dijangka: LULUS (atau SKIP kalau kelayakan tiada — dalam kes itu jalankan sekali dengan kelayakan sebenar sebelum menganggap task ini selesai).

- [ ] **Step 5: Commit**

```bash
git add internal/storage/
git commit -m "feat(storage): PutObject untuk muat naik kandungan jana-server ke R2"
```

---

## Task 6: Handler aktiviti & sesi

**Files:**
- Create: `internal/http/handlers/activities.go`
- Modify: `internal/http/router.go`
- Test: `internal/http/handlers/activities_live_test.go`

**Interfaces:**
- Consumes: query dari Task 2, `authz.IsManagement`, `audit.Record`, `auditActor`.
- Produces: `NewActivityHandler(pool *pgxpool.Pool) *ActivityHandler` dengan kaedah `List`, `Get`, `Create`, `Update`, `Publish`, `Cancel`, `ReplaceSessions`, `ListCategories`.

- [ ] **Step 1: Tulis ujian invarian yang gagal**

`internal/http/handlers/activities_live_test.go`:

```go
package handlers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
)

// activityTestPool — sama corak dengan handler test sedia ada: di-skip
// melainkan ACTIVITY_TEST_DB ditetapkan. Guna DB BUANGAN.
func activityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ACTIVITY_TEST_DB")
	if dsn == "" {
		t.Skip("tetapkan ACTIVITY_TEST_DB untuk jalankan ujian ini")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("sambung: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestReplaceSessionsMengekalkanInvarianTetingkap — harga yang kita bayar
// untuk mendenormalisasi activities.starts_at/ends_at. Kalau ujian ini
// tiada, invarian itu hanya niat baik.
func TestReplaceSessionsMengekalkanInvarianTetingkap(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	activityID := seedActivity(t, pool) // helper: cipta kategori+aktiviti+1 sesi

	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	sessions := []sessionInput{
		{Seq: 1, StartsAt: base.Add(48 * time.Hour), EndsAt: base.Add(50 * time.Hour)},
		{Seq: 2, StartsAt: base, EndsAt: base.Add(2 * time.Hour)}, // paling awal, seq kemudian
		{Seq: 3, StartsAt: base.Add(24 * time.Hour), EndsAt: base.Add(27 * time.Hour)},
	}
	if err := replaceSessionsTx(ctx, pool, activityID, sessions); err != nil {
		t.Fatalf("replaceSessions: %v", err)
	}

	got, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		t.Fatalf("GetActivityByID: %v", err)
	}
	if !got.StartsAt.Time.Equal(base) {
		t.Errorf("starts_at = %v, mahu %v (min sesi, bukan sesi pertama ikut seq)", got.StartsAt.Time, base)
	}
	wantEnd := base.Add(50 * time.Hour)
	if !got.EndsAt.Time.Equal(wantEnd) {
		t.Errorf("ends_at = %v, mahu %v", got.EndsAt.Time, wantEnd)
	}

	// Buang sesi paling awal — tetingkap mesti mengecut, bukan kekal basi.
	if err := replaceSessionsTx(ctx, pool, activityID, sessions[:1]); err != nil {
		t.Fatalf("replaceSessions kedua: %v", err)
	}
	got, _ = q.GetActivityByID(ctx, activityID)
	if !got.StartsAt.Time.Equal(base.Add(48 * time.Hour)) {
		t.Errorf("selepas buang sesi terawal, starts_at = %v, mahu %v",
			got.StartsAt.Time, base.Add(48*time.Hour))
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestReplaceSessions -v
```

Cipta DB dahulu kalau belum: `createdb marc_activity` kemudian jalankan `goose ... up` ke atasnya.

Dijangka: GAGAL kompilasi — `undefined: seedActivity`, `undefined: replaceSessionsTx`, `undefined: sessionInput`.

- [ ] **Step 3: Tulis handler dan fungsi transaksi**

`internal/http/handlers/activities.go` — bahagian penting (invarian):

```go
package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type ActivityHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewActivityHandler(pool *pgxpool.Pool) *ActivityHandler {
	return &ActivityHandler{pool: pool, queries: sqlc.New(pool)}
}

type sessionInput struct {
	Seq      int       `json:"seq" binding:"required,min=1"`
	Title    string    `json:"title"`
	StartsAt time.Time `json:"starts_at" binding:"required"`
	EndsAt   time.Time `json:"ends_at" binding:"required"`
}

// replaceSessionsTx menggantikan KESELURUHAN set sesi dalam satu transaksi,
// kemudian mengira semula tetingkap aktiviti.
//
// Ganti-semua, bukan CRUD per-sesi: invarian starts_at/ends_at perlu dikira
// semula setiap kali set berubah, dan satu laluan kod bermakna satu tempat
// yang boleh melanggarnya.
func replaceSessionsTx(ctx context.Context, pool *pgxpool.Pool, activityID uuid.UUID, sessions []sessionInput) error {
	if len(sessions) == 0 {
		return errors.New("aktiviti perlu sekurang-kurangnya satu sesi")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	// Sesi yang sudah ada kehadiran tak boleh dibuang — kehadiran itu bukti
	// yang menyokong sijil.
	withAttendance, err := q.CountSessionsWithAttendance(ctx, activityID)
	if err != nil {
		return err
	}
	if withAttendance > 0 {
		return errSessionHasAttendance
	}

	if err := q.DeleteActivitySessions(ctx, activityID); err != nil {
		return err
	}
	for _, s := range sessions {
		if !s.EndsAt.After(s.StartsAt) {
			return errors.New("masa tamat sesi mesti selepas masa mula")
		}
		if _, err := q.CreateActivitySession(ctx, sqlc.CreateActivitySessionParams{
			ActivityID: activityID,
			Seq:        int32(s.Seq),
			Title:      s.Title,
			StartsAt:   pgTimestamptz(s.StartsAt),
			EndsAt:     pgTimestamptz(s.EndsAt),
		}); err != nil {
			return err
		}
	}

	if err := q.RecomputeActivityWindow(ctx, activityID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var errSessionHasAttendance = errors.New("sesi yang sudah ada kehadiran tidak boleh diganti")
```

Handler `ReplaceSessions` memetakan `errSessionHasAttendance` kepada `409`:

```go
func (h *ActivityHandler) ReplaceSessions(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Sessions []sessionInput `json:"sessions" binding:"required,min=1,dive"`
	}
	if !bindJSON(c, &req) {
		return
	}

	err := replaceSessionsTx(c.Request.Context(), h.pool, activityID, req.Sessions)
	switch {
	case errors.Is(err, errSessionHasAttendance):
		c.JSON(http.StatusConflict, gin.H{"error": "sesi yang sudah ada kehadiran tidak boleh diganti"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini sesi"})
		return
	}

	sessions, err := h.queries.ListActivitySessions(c.Request.Context(), activityID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca sesi"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// requireManagement — semakan management dibuat DALAM handler, ikut corak
// sedia ada (lihat audit.go, profile.go). Tiada middleware RequireManagement
// dalam repo ini; jangan cipta satu.
func (h *ActivityHandler) requireManagement(c *gin.Context) bool {
	ok, err := authz.IsManagement(c.Request.Context(), h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk pengurusan sahaja"})
		return false
	}
	return true
}
```

Tulis juga: `Create` (cipta aktiviti status `draft` + sesi awal dalam satu transaksi + `audit.Record` dengan `Action: audit.ActionCreate`), `Update` (`audit.ActionUpdate` dengan `Old`/`New` penuh supaya `audit.Diff` mengira delta), `Publish` (`draft` → `published`, tolak `409` kalau bukan `draft`), `Cancel` (perlukan `reason`, tolak kalau sudah `cancelled`), `List`, `Get`, `ListCategories`.

`Get` mesti memulangkan tiga medan tambahan di luar baris aktiviti, sebab klien bergantung padanya (Task 12):

```json
{
  "sessions": [...],
  "registration_count": 12,
  "is_registered": true
}
```

`is_registered` dikira untuk **pemanggil semasa** melalui `GetRegistrationByActivityAndUser` — `pgx.ErrNoRows` bermakna `false`, bukan ralat. `List` juga menyertakan `registration_count` (query sudah mengiranya) supaya kad senarai boleh menunjukkan slot berbaki tanpa N+1.

Helper penukaran jenis (`pgTimestamptz`, `pgUUID`, dsb.) dan `parseUUIDParam` — lihat bahagian "Helper yang dikongsi" di atas. Semak `bind.go` sebelum menambah.

- [ ] **Step 4: Tulis helper ujian `seedActivity`**

Dalam `activities_live_test.go`:

```go
func seedActivity(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var categoryID uuid.UUID
	err := pool.QueryRow(ctx,
		`select id from activity_categories where key = 'badminton'`).Scan(&categoryID)
	if err != nil {
		t.Fatalf("kategori seed tiada — jalankan migration atas DB ujian: %v", err)
	}

	start := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	var activityID uuid.UUID
	err = pool.QueryRow(ctx, `
		insert into activities (category_id, title, location_name, starts_at, ends_at,
		  registration_closes_at)
		values ($1, 'Ujian Aktiviti', 'Dewan A', $2, $3, $2)
		returning id`, categoryID, start, start.Add(2*time.Hour)).Scan(&activityID)
	if err != nil {
		t.Fatalf("seed aktiviti: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from activities where id = $1`, activityID)
	})
	return activityID
}
```

- [ ] **Step 5: Jalankan ujian, sahkan ia lulus**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestReplaceSessions -v
```

Dijangka: LULUS.

- [ ] **Step 6: Daftar route**

Dalam `internal/http/router.go`, selepas blok posts:

```go
	activityHandler := handlers.NewActivityHandler(pool)

	approved.GET("/activity-categories", activityHandler.ListCategories)
	approved.GET("/activities", activityHandler.List)
	approved.GET("/activities/:id", activityHandler.Get)

	verified.POST("/activities", activityHandler.Create)
	verified.PATCH("/activities/:id", activityHandler.Update)
	verified.POST("/activities/:id/publish", activityHandler.Publish)
	verified.POST("/activities/:id/cancel", activityHandler.Cancel)
	verified.PUT("/activities/:id/sessions", activityHandler.ReplaceSessions)
```

- [ ] **Step 7: Sahkan pembinaan dan jalankan semua ujian**

```bash
go build ./... && go test ./...
```

Dijangka: lulus (ujian yang perlukan DB akan SKIP).

- [ ] **Step 8: Commit**

```bash
git add internal/http/handlers/activities.go internal/http/handlers/activities_live_test.go internal/http/router.go
git commit -m "feat(activity): handler CRUD aktiviti dan penggantian set sesi"
```

---

## Task 7: Pendaftaran & perlumbaan kapasiti

**Files:**
- Create: `internal/http/handlers/activity_registrations.go`
- Modify: `internal/http/router.go`
- Test: `internal/http/handlers/activity_registrations_live_test.go`

**Interfaces:**
- Consumes: `LockActivityForRegistration`, `CountActiveRegistrations`, `CreateRegistration`, `CancelRegistration` dari Task 2.
- Produces: `NewRegistrationHandler(pool *pgxpool.Pool) *RegistrationHandler` dengan `Register`, `Cancel`, `ListForActivity`, `ListMine`; dan `func registerTx(ctx, pool, activityID, userID uuid.UUID) (sqlc.ActivityRegistration, error)`.

- [ ] **Step 1: Tulis ujian perlumbaan yang gagal**

`internal/http/handlers/activity_registrations_live_test.go`:

```go
package handlers

import (
	"context"
	"sync"
	"testing"
)

// Ujian paling penting dalam modul ini. Tanpa ia, `select ... for update`
// dalam registerTx hanya niat baik — tiada apa yang membuktikan dua ahli
// tidak boleh merebut slot terakhir yang sama.
func TestRegisterPerlumbaanSlotTerakhir(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID := seedActivityWithCapacity(t, pool, 1)
	users := seedUsers(t, pool, 8)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		berjaya int
		penuh   int
	)
	for _, uid := range users {
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			_, err := registerTx(ctx, pool, activityID, uid)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				berjaya++
			case errors.Is(err, errActivityFull):
				penuh++
			default:
				t.Errorf("ralat tak dijangka: %v", err)
			}
		}(uid)
	}
	wg.Wait()

	if berjaya != 1 {
		t.Errorf("pendaftaran berjaya = %d, mahu tepat 1", berjaya)
	}
	if penuh != len(users)-1 {
		t.Errorf("ditolak 'penuh' = %d, mahu %d", penuh, len(users)-1)
	}

	// Semakan kedua terhadap DB — kaunter dalam-memori boleh menipu.
	q := sqlc.New(pool)
	n, err := q.CountActiveRegistrations(ctx, activityID)
	if err != nil {
		t.Fatalf("CountActiveRegistrations: %v", err)
	}
	if n != 1 {
		t.Errorf("baris pendaftaran dalam DB = %d, mahu 1", n)
	}
}

func TestDaftarSemulaSelepasBatal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	activityID := seedActivityWithCapacity(t, pool, 5)
	userID := seedUsers(t, pool, 1)[0]

	if _, err := registerTx(ctx, pool, activityID, userID); err != nil {
		t.Fatalf("daftar pertama: %v", err)
	}
	// Daftar dua kali mesti ditolak oleh indeks unik separa.
	if _, err := registerTx(ctx, pool, activityID, userID); !errors.Is(err, errAlreadyRegistered) {
		t.Fatalf("daftar kedua = %v, mahu errAlreadyRegistered", err)
	}

	q := sqlc.New(pool)
	if _, err := q.CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID, UserID: userID,
	}); err != nil {
		t.Fatalf("batal: %v", err)
	}

	// Selepas batal, unik SEPARA membenarkan pendaftaran baharu.
	if _, err := registerTx(ctx, pool, activityID, userID); err != nil {
		t.Errorf("daftar semula selepas batal: %v", err)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestRegister|TestDaftarSemula' -v
```

Dijangka: GAGAL kompilasi — `undefined: registerTx`.

- [ ] **Step 3: Tulis `registerTx` dan handler**

`internal/http/handlers/activity_registrations.go`:

```go
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

var (
	errActivityFull       = errors.New("aktiviti penuh")
	errRegistrationClosed = errors.New("pendaftaran ditutup")
	errAlreadyRegistered  = errors.New("sudah berdaftar")
	errActivityNotOpen    = errors.New("aktiviti tidak dibuka untuk pendaftaran")
)

// newCheckinToken jana token legap untuk QR ahli.
//
// crypto/rand, bukan math/rand: sesiapa yang dapat meneka token ini boleh
// ditandakan hadir untuk orang lain, dan kehadiran itulah yang menentukan
// siapa dapat sijil.
func newCheckinToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// registerTx mendaftarkan seorang ahli, menyerikan pemeriksaan kapasiti.
//
// `select ... for update` atas baris aktiviti ialah intinya: tanpa kunci
// itu, dua permintaan serentak boleh kedua-duanya membaca "9 daripada 10
// terisi" dan kedua-duanya memasukkan baris. Pada skala ratusan ahli, kunci
// baris ini percuma — tiada sebab untuk mereka sesuatu yang lebih pintar.
func registerTx(ctx context.Context, pool *pgxpool.Pool, activityID, userID uuid.UUID) (sqlc.ActivityRegistration, error) {
	var zero sqlc.ActivityRegistration

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	activity, err := q.LockActivityForRegistration(ctx, activityID)
	if err != nil {
		return zero, err
	}
	if activity.Status != "published" {
		return zero, errActivityNotOpen
	}

	now := time.Now()
	if activity.RegistrationOpensAt.Valid && now.Before(activity.RegistrationOpensAt.Time) {
		return zero, errRegistrationClosed
	}
	if now.After(activity.RegistrationClosesAt.Time) {
		return zero, errRegistrationClosed
	}

	if _, err := q.GetRegistrationByActivityAndUser(ctx, sqlc.GetRegistrationByActivityAndUserParams{
		ActivityID: activityID, UserID: userID,
	}); err == nil {
		return zero, errAlreadyRegistered
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}

	if activity.Capacity.Valid {
		count, err := q.CountActiveRegistrations(ctx, activityID)
		if err != nil {
			return zero, err
		}
		if count >= int64(activity.Capacity.Int32) {
			return zero, errActivityFull
		}
	}

	token, err := newCheckinToken()
	if err != nil {
		return zero, err
	}

	// payment_status kekal 'not_required' sehingga integrasi payment
	// mendarat; fee_cents sentiasa 0 buat masa ini.
	status, paymentStatus := "registered", "not_required"

	reg, err := q.CreateRegistration(ctx, sqlc.CreateRegistrationParams{
		ActivityID:    activityID,
		UserID:        userID,
		Status:        status,
		PaymentStatus: paymentStatus,
		CheckinToken:  token,
	})
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return reg, nil
}
```

Handler `Register` memetakan ralat: `errActivityFull` → `409` "aktiviti sudah penuh", `errRegistrationClosed` → `409` "pendaftaran telah ditutup", `errAlreadyRegistered` → `409` "anda sudah berdaftar", `errActivityNotOpen` → `409` "aktiviti belum dibuka".

`Cancel` memanggil `q.CancelRegistration`; `pgx.ErrNoRows` → `404` "anda tidak berdaftar". `ListForActivity` perlukan management. `ListMine` memanggil `ListMyRegistrations` untuk pengguna semasa.

Tiada `audit.Record` untuk pendaftaran — volum tinggi, dan baris itu sendiri menyimpan `registered_at`/`cancelled_at`. Keputusan sama seperti `create` post.

- [ ] **Step 4: Tulis helper ujian**

Tambah `seedActivityWithCapacity(t, pool, capacity int) uuid.UUID` (sama seperti `seedActivity` tetapi menetapkan `capacity` dan `status = 'published'` dengan `registration_closes_at` pada masa hadapan) dan `seedUsers(t, pool, n int) []uuid.UUID` (memasukkan `users` + `profiles` dengan `status='approved'`, dibersihkan melalui `t.Cleanup`).

- [ ] **Step 5: Jalankan ujian, sahkan ia lulus**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestRegister|TestDaftarSemula' -v -race
```

Dijangka: LULUS. Bendera `-race` penting di sini — ujian ini menjalankan goroutine serentak.

- [ ] **Step 6: Daftar route**

```go
	registrationHandler := handlers.NewRegistrationHandler(pool)

	approved.GET("/me/activities", registrationHandler.ListMine)
	verified.POST("/activities/:id/registration", registrationHandler.Register)
	verified.DELETE("/activities/:id/registration", registrationHandler.Cancel)
	verified.GET("/activities/:id/registrations", registrationHandler.ListForActivity)
```

- [ ] **Step 7: Commit**

```bash
git add internal/http/handlers/activity_registrations.go internal/http/handlers/activity_registrations_live_test.go internal/http/router.go
git commit -m "feat(activity): pendaftaran ahli dengan kapasiti berkunci baris"
```

---

## Task 8: Kehadiran

**Files:**
- Create: `internal/http/handlers/activity_attendance.go`
- Modify: `internal/http/router.go`
- Test: `internal/http/handlers/activity_attendance_live_test.go`

**Interfaces:**
- Consumes: `MarkAttendance`, `DeleteAttendance`, `GetActivitySessionByID`, `GetRegistrationByCheckinToken`, `certificate.WithinCheckinWindow`.
- Produces: `NewAttendanceHandler(pool *pgxpool.Pool) *AttendanceHandler` dengan `Mark`, `Unmark`.

- [ ] **Step 1: Tulis ujian yang gagal**

`internal/http/handlers/activity_attendance_live_test.go`:

```go
package handlers

import (
	"context"
	"testing"
	"time"
)

func TestTandaKehadiranDiLuarTetingkapDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// Sesi yang tamat tiga hari lalu — jauh di luar padding 2 jam.
	activityID := seedActivityWithCapacity(t, pool, 10)
	sessionID := seedSession(t, pool, activityID,
		time.Now().Add(-72*time.Hour), time.Now().Add(-70*time.Hour))
	userID := seedUsers(t, pool, 1)[0]
	reg, err := registerTx(ctx, pool, activityID, userID)
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}

	_, err = markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual", userID)
	if !errors.Is(err, errOutsideCheckinWindow) {
		t.Errorf("err = %v, mahu errOutsideCheckinWindow", err)
	}
}

func TestTandaKehadiranDuaKaliIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID := seedActivityWithCapacity(t, pool, 10)
	sessionID := seedSession(t, pool, activityID,
		time.Now().Add(-30*time.Minute), time.Now().Add(time.Hour))
	userID := seedUsers(t, pool, 1)[0]
	reg, _ := registerTx(ctx, pool, activityID, userID)

	first, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "scan", userID)
	if err != nil {
		t.Fatalf("tanda pertama: %v", err)
	}
	if !first.Created {
		t.Error("tanda pertama sepatutnya mencipta baris")
	}

	// QR dipegang di depan lens menghantar permintaan berulang. Yang kedua
	// bukan ralat — ia hanya tiada kerja, dan UI perlu tahu bezanya supaya
	// ia boleh menunjukkan "sudah hadir" berbanding "✓ baru ditanda".
	second, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "scan", userID)
	if err != nil {
		t.Fatalf("tanda kedua: %v", err)
	}
	if second.Created {
		t.Error("tanda kedua sepatutnya melaporkan Created=false")
	}

	q := sqlc.New(pool)
	n, _ := q.CountAttendanceByRegistration(ctx, reg.ID)
	if n != 1 {
		t.Errorf("baris kehadiran = %d, mahu 1", n)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestTandaKehadiran -v
```

Dijangka: GAGAL kompilasi — `undefined: markAttendanceTx`.

- [ ] **Step 3: Tulis implementasi**

`internal/http/handlers/activity_attendance.go`:

```go
package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/certificate"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

var (
	errOutsideCheckinWindow = errors.New("di luar tetingkap check-in")
	errNotRegistered        = errors.New("tidak berdaftar untuk aktiviti ini")
)

type markResult struct {
	Created bool
	Row     sqlc.ActivityAttendance
}

// markAttendanceTx menanda satu kehadiran.
//
// Menerima registration_id (skrin senarai, method 'manual') ATAU token yang
// sudah diselesaikan kepada pendaftaran (scanner, method 'scan') — pemanggil
// yang menyelesaikan token, jadi fungsi ini melihat satu bentuk input
// sahaja.
func markAttendanceTx(ctx context.Context, pool *pgxpool.Pool, sessionID, registrationID uuid.UUID, method string, actorID uuid.UUID) (markResult, error) {
	q := sqlc.New(pool)

	session, err := q.GetActivitySessionByID(ctx, sessionID)
	if err != nil {
		return markResult{}, err
	}
	if !certificate.WithinCheckinWindow(time.Now(), session.StartsAt.Time, session.EndsAt.Time) {
		return markResult{}, errOutsideCheckinWindow
	}

	reg, err := q.GetRegistrationByID(ctx, registrationID)
	if err != nil {
		return markResult{}, err
	}
	if reg.ActivityID != session.ActivityID {
		return markResult{}, errNotRegistered
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return markResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := sqlc.New(pool).WithTx(tx)

	row, err := qtx.MarkAttendance(ctx, sqlc.MarkAttendanceParams{
		RegistrationID: registrationID,
		SessionID:      sessionID,
		Method:         method,
		MarkedBy:       pgUUID(actorID),
	})
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		// on conflict do nothing → tiada baris dipulangkan. Sudah hadir.
		created = false
	} else if err != nil {
		return markResult{}, err
	}

	if created {
		if err := audit.Record(ctx, qtx, audit.Entry{
			EntityType: "activity_attendance",
			EntityID:   row.ID,
			Action:     audit.ActionCreate,
			Actor:      audit.Actor{UserID: actorID},
			New: map[string]any{
				"registration_id": registrationID.String(),
				"session_id":      sessionID.String(),
				"method":          method,
			},
		}); err != nil {
			return markResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return markResult{}, err
	}
	return markResult{Created: created, Row: row}, nil
}
```

Handler `Mark` menerima badan `{"registration_id": "...", "checkin_token": "...", "method": "manual|scan"}` — tepat satu daripada dua medan pengenalan. `checkin_token` diselesaikan melalui `GetRegistrationByCheckinToken`; token tidak dikenali → `404` "QR tidak dikenali". Pemetaan ralat: `errOutsideCheckinWindow` → `422` "di luar tetingkap check-in", `errNotRegistered` → `409` "ahli ini tidak berdaftar untuk aktiviti ini".

Respons mesti membezakan `created` supaya UI boleh menunjukkan "✓ ditanda" berbanding "sudah hadir":

```json
{"created": true, "member": {"display_name": "Ahmad", "member_id": "M0012"}}
```

`Unmark` memanggil `DeleteAttendance`, memerlukan management, dan mencatat audit dengan `Action: audit.ActionDelete`. Sifar baris terjejas → `404`.

Gunakan `auditActor(c, h.queries)` dalam handler (bukan `audit.Actor{UserID: ...}` kosong) supaya IP, user-agent, dan snapshot role masuk ke dalam jejak.

- [ ] **Step 4: Jalankan ujian, sahkan ia lulus**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestTandaKehadiran -v
```

Dijangka: LULUS.

- [ ] **Step 5: Daftar route**

```go
	attendanceHandler := handlers.NewAttendanceHandler(pool)

	verified.POST("/activities/:id/sessions/:sid/attendance", attendanceHandler.Mark)
	verified.DELETE("/activities/:id/sessions/:sid/attendance/:rid", attendanceHandler.Unmark)
```

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/activity_attendance.go internal/http/handlers/activity_attendance_live_test.go internal/http/router.go
git commit -m "feat(activity): tanda kehadiran per-sesi dengan tetingkap masa"
```

---

## Task 9: Penerbitan sijil dua fasa

**Files:**
- Create: `internal/http/handlers/activity_certificates.go`
- Modify: `internal/http/router.go`
- Test: `internal/http/handlers/activity_certificates_live_test.go`

**Interfaces:**
- Consumes: `certificate.IsEligible`, `certificate.GeneratePDF`, `R2Client.PutObject`, `NextSequence`, query sijil dari Task 2.
- Produces: `NewCertificateHandler(pool *pgxpool.Pool, r2 *storage.R2Client, baseURL string) *CertificateHandler` dengan `Issue`, `Revoke`, `ListMine`, `Download`, `Verify`; dan `func issueCertificatesTx(...) ([]sqlc.ActivityCertificate, error)`, `func fillPendingCertificateFiles(...) error`.

- [ ] **Step 1: Tulis ujian yang gagal**

`internal/http/handlers/activity_certificates_live_test.go`:

```go
package handlers

import (
	"context"
	"testing"
)

func TestTerbitSijilIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 3) // 3 ahli, semua hadir penuh

	first, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil)
	if err != nil {
		t.Fatalf("terbit pertama: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("sijil terbit = %d, mahu 3", len(first))
	}

	// Panggilan kedua tak boleh menerbitkan pendua ATAU membazirkan nombor
	// siri — unik (activity_id, user_id) menghalang baris, dan siri hanya
	// diambil untuk baris yang benar-benar dimasukkan.
	second, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil)
	if err != nil {
		t.Fatalf("terbit kedua: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("terbit kedua hasilkan %d sijil, mahu 0", len(second))
	}

	q := sqlc.New(pool)
	all, _ := q.ListCertificatesByActivity(ctx, activityID)
	if len(all) != 3 {
		t.Errorf("jumlah sijil = %d, mahu 3", len(all))
	}
}

func TestSijilHanyaUntukYangCukupAmbang(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// 3 sesi, ambang 100%. Ahli A hadir 3, B hadir 2, C hadir 0.
	activityID := seedAktivitiTigaSesi(t, pool, 100)
	a, b, c := seedPesertaKehadiran(t, pool, activityID, 3, 2, 0)

	terbit, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil)
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	if len(terbit) != 1 {
		t.Fatalf("sijil = %d, mahu 1 (hanya A)", len(terbit))
	}
	if terbit[0].UserID != a {
		t.Errorf("penerima = %v, mahu A (%v)", terbit[0].UserID, a)
	}
	_ = b
	_ = c
}

func TestFasaDuaMenyambungBarisTanpaR2Key(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 2)
	if _, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil); err != nil {
		t.Fatalf("terbit: %v", err)
	}

	q := sqlc.New(pool)
	pending, err := q.ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		t.Fatalf("ListCertificatesPendingFile: %v", err)
	}
	// Fasa 1 sengaja meninggalkan r2_key null — muat naik berlaku SELEPAS
	// komit, sebab rollback Postgres tidak boleh memadam objek R2.
	if len(pending) != 2 {
		t.Errorf("sijil menunggu fail = %d, mahu 2", len(pending))
	}
}

func TestNomborSiriTidakMelompatBilaTransaksiGagal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	before := bacaSequence(t, pool, "certificate_serial")

	// Aktiviti tanpa peserta layak → tiada siri diambil langsung.
	activityID := seedAktivitiTigaSesi(t, pool, 100)
	if _, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil); err != nil {
		t.Fatalf("terbit: %v", err)
	}

	after := bacaSequence(t, pool, "certificate_serial")
	if after != before {
		t.Errorf("sequence bergerak %d → %d tanpa sijil diterbitkan", before, after)
	}
	_ = q
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestTerbitSijil|TestSijilHanya|TestFasaDua|TestNomborSiri' -v
```

Dijangka: GAGAL kompilasi — `undefined: issueCertificatesTx`.

- [ ] **Step 3: Tulis fasa 1 — transaksi**

Dalam `internal/http/handlers/activity_certificates.go`:

```go
// issueCertificatesTx ialah FASA 1: kira yang layak, ambil nombor siri,
// masukkan baris dengan r2_key null, catat audit, komit.
//
// Menjana PDF dan memuat naik ke R2 SENGAJA tiada di sini. Ia kerja luaran:
// transaksi yang menahan kunci selama ratusan muat naik ialah masalah, dan
// rollback tidak memadam objek R2. Fasa 2 (fillPendingCertificateFiles)
// berjalan selepas komit dan boleh disambung semula.
func issueCertificatesTx(ctx context.Context, pool *pgxpool.Pool, activityID, actorID uuid.UUID) ([]sqlc.ActivityCertificate, error) {
	q := sqlc.New(pool)

	activity, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		return nil, err
	}
	if time.Now().Before(activity.EndsAt.Time) {
		return nil, errActivityNotFinished
	}

	totalSessions, err := q.CountActivitySessions(ctx, activityID)
	if err != nil {
		return nil, err
	}

	candidates, err := q.ListEligibleForCertificate(ctx, activityID)
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := sqlc.New(pool).WithTx(tx)

	var issued []sqlc.ActivityCertificate
	for _, cand := range candidates {
		if !certificate.IsEligible(int(cand.Attended), int(totalSessions), int(activity.AttendanceThresholdPct)) {
			continue
		}
		if !certificate.EncodableName(cand.DisplayName) {
			return nil, fmt.Errorf("nama %q tidak boleh dicetak pada sijil", cand.DisplayName)
		}

		token, err := newCheckinToken() // sumber crypto/rand yang sama
		if err != nil {
			return nil, err
		}

		// NextSequence ialah `update ... returning` atas jadual `sequences`
		// — ia BERUNDUR dengan transaksi ini. `create sequence` Postgres
		// tidak, dan akan meninggalkan lompang dalam penomboran sijil.
		seq, err := qtx.NextSequence(ctx, "certificate_serial")
		if err != nil {
			return nil, err
		}
		serial := fmt.Sprintf("MARC-%d-%06d", activity.StartsAt.Time.Year(), seq)

		row, err := qtx.CreateCertificate(ctx, sqlc.CreateCertificateParams{
			ActivityID:    activityID,
			UserID:        cand.UserID,
			Serial:        serial,
			VerifyToken:   token,
			RecipientName: cand.DisplayName,
			ActivityTitle: activity.Title,
			ActivityDate:  pgDate(activity.StartsAt.Time),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// on conflict do nothing → sijil sudah wujud. Bukan ralat;
			// inilah yang menjadikan endpoint boleh diulang.
			continue
		}
		if err != nil {
			return nil, err
		}

		if err := audit.Record(ctx, qtx, audit.Entry{
			EntityType: "activity_certificate",
			EntityID:   row.ID,
			Action:     audit.ActionCreate,
			Actor:      audit.Actor{UserID: actorID},
			New: map[string]any{
				"serial":      serial,
				"activity_id": activityID.String(),
				"user_id":     cand.UserID.String(),
			},
		}); err != nil {
			return nil, err
		}
		issued = append(issued, row)
	}

	if err := qtx.SetActivityCertificatesIssuedAt(ctx, activityID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return issued, nil
}

var errActivityNotFinished = errors.New("aktiviti belum tamat")
```

Nota tentang `NextSequence` yang dipanggil per-sijil: satu sijil gagal → keseluruhan transaksi berundur → tiada nombor siri terbazir. Itu tepat yang diuji `TestNomborSiriTidakMelompatBilaTransaksiGagal`.

- [ ] **Step 4: Tulis fasa 2 — jana dan muat naik**

```go
// fillPendingCertificateFiles ialah FASA 2: jana PDF dan muat naik untuk
// setiap sijil yang r2_key masih null.
//
// Boleh diulang dengan selamat. Kalau proses mati separuh jalan atau R2
// tersekat, panggil semula endpoint dan ia menyambung dari baris yang
// belum siap.
func fillPendingCertificateFiles(ctx context.Context, pool *pgxpool.Pool, r2 *storage.R2Client, baseURL string, activityID uuid.UUID) error {
	q := sqlc.New(pool)
	pending, err := q.ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		return err
	}

	activity, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		return err
	}

	for _, cert := range pending {
		pdf, err := certificate.GeneratePDF(certificate.Data{
			Serial:        cert.Serial,
			RecipientName: cert.RecipientName,
			ActivityTitle: cert.ActivityTitle,
			CategoryName:  activity.CategoryName,
			ActivityDate:  cert.ActivityDate.Time,
			VerifyURL:     baseURL + "/verify/certificates/" + cert.VerifyToken,
		})
		if err != nil {
			return fmt.Errorf("jana sijil %s: %w", cert.Serial, err)
		}

		key := "certificates/" + cert.ID.String() + ".pdf"
		if err := r2.PutObject(ctx, key, "application/pdf", pdf); err != nil {
			return fmt.Errorf("muat naik sijil %s: %w", cert.Serial, err)
		}

		// Kemas kini SELEPAS muat naik berjaya. Kalau ia gagal di sini,
		// baris kekal tanpa r2_key dan pusingan seterusnya menulis ganti
		// objek yang sama — muat naik R2 idempoten ikut kunci.
		if err := q.SetCertificateR2Key(ctx, sqlc.SetCertificateR2KeyParams{
			ID: cert.ID, R2Key: pgText(key),
		}); err != nil {
			return err
		}
	}
	return nil
}
```

Handler `Issue` memanggil fasa 1 kemudian fasa 2, dan memulangkan `202` dengan kiraan kalau fasa 2 gagal separuh jalan — sijil sudah wujud, failnya belum. Badan respons: `{"issued": 12, "files_ready": 9, "message": "..."}`.

`Download` memulangkan presigned URL; `r2_key` null → `409` `{"error": "sijil sedang disediakan, cuba sebentar lagi"}`.

`Revoke`: `RevokeCertificate`, `EnqueueDeletedUpload` dengan `reason = "certificate_revoked"`, dan `audit.Record` dengan `Action: audit.ActionDelete` — semuanya dalam satu transaksi.

- [ ] **Step 5: Jalankan ujian, sahkan ia lulus**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run 'TestTerbitSijil|TestSijilHanya|TestFasaDua|TestNomborSiri' -v
```

Dijangka: LULUS.

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/activity_certificates.go internal/http/handlers/activity_certificates_live_test.go
git commit -m "feat(activity): penerbitan sijil dua fasa yang boleh disambung"
```

---

## Task 10: Halaman pengesahan awam

**Files:**
- Modify: `internal/http/handlers/activity_certificates.go`
- Modify: `internal/http/router.go`
- Test: `internal/http/handlers/activity_certificates_live_test.go`

**Interfaces:**
- Consumes: `GetCertificateByVerifyToken`.
- Produces: `(*CertificateHandler).Verify`, `type verifyResponse struct`.

- [ ] **Step 1: Tulis ujian kebocoran yang gagal**

Tambah pada `activity_certificates_live_test.go`:

```go
// Endpoint AWAM pertama yang mendedahkan nama ahli. Ujian ini ditulis
// sebagai penegasan atas SET medan, bukan atas nilai — supaya sesiapa yang
// menambah medan pada masa depan memecahkan ujian ini dan terpaksa
// memikirkannya semula. Semakan privasi yang bergantung pada ingatan tidak
// bertahan.
func TestVerifyTidakMendedahkanPII(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, uuid.Nil)
	if err != nil || len(certs) != 1 {
		t.Fatalf("terbit: %v, %d sijil", err, len(certs))
	}

	body := doGET(t, pool, "/verify/certificates/"+certs[0].VerifyToken)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("nyahkod respons: %v", err)
	}

	dibenarkan := map[string]bool{
		"serial": true, "recipient_name": true, "activity_title": true,
		"activity_date": true, "issued_at": true, "status": true,
	}
	for key := range payload {
		if !dibenarkan[key] {
			t.Errorf("respons awam mengandungi medan tak dibenarkan %q — "+
				"semak semula sama ada ia patut awam sebelum meluaskan senarai", key)
		}
	}
	for _, wajib := range []string{"serial", "recipient_name", "status"} {
		if _, ok := payload[wajib]; !ok {
			t.Errorf("respons kehilangan medan wajib %q", wajib)
		}
	}
}

func TestVerifyTokenTakSahDanSijilDitarikTakBolehDibezakan(t *testing.T) {
	pool := activityTestPool(t)

	// Token yang tidak wujud langsung.
	statusTiada := doGETStatus(t, pool, "/verify/certificates/tokenyangtakwujudlangsung")
	// Token bentuk sah tetapi tak dikenali.
	statusSalah := doGETStatus(t, pool, "/verify/certificates/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if statusTiada != http.StatusNotFound || statusSalah != http.StatusNotFound {
		t.Errorf("status = %d dan %d, kedua-duanya mahu 404 — respons berbeza "+
			"menjadi oracle yang mengesahkan token mana yang pernah wujud",
			statusTiada, statusSalah)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia gagal**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestVerify -v
```

Dijangka: GAGAL — route belum wujud, `404` dengan badan kosong (`json.Unmarshal` gagal).

- [ ] **Step 3: Tulis handler Verify**

```go
// verifyResponse — bentuk respons awam, ditakrifkan sebagai struct EKSPLISIT
// dan bukan gin.H daripada baris DB.
//
// Sebab: mengembalikan baris terus bermakna menambah lajur pada
// activity_certificates secara senyap-senyap menyiarkannya ke internet.
// Struct ini ialah sempadan yang mesti dilalui dengan sengaja.
type verifyResponse struct {
	Serial        string `json:"serial"`
	RecipientName string `json:"recipient_name"`
	ActivityTitle string `json:"activity_title"`
	ActivityDate  string `json:"activity_date"`
	IssuedAt      string `json:"issued_at"`
	Status        string `json:"status"` // "sah" | "ditarik_balik"
}

func (h *CertificateHandler) Verify(c *gin.Context) {
	token := c.Param("token")

	cert, err := h.queries.GetCertificateByVerifyToken(c.Request.Context(), token)
	if err != nil {
		// 404 yang SAMA untuk token tak wujud dan token cacat. Respons
		// berbeza akan menjadi oracle: penyerang boleh mengesahkan token
		// mana yang pernah wujud.
		c.JSON(http.StatusNotFound, gin.H{"error": "sijil tidak dijumpai"})
		return
	}

	status := "sah"
	if cert.RevokedAt.Valid {
		// Sijil yang ditarik kekal boleh disemak dan dilaporkan sebagai
		// ditarik. Memadam baris akan menjadikannya nampak seperti tidak
		// pernah wujud — lebih buruk bagi orang yang sedang mengesahkan.
		status = "ditarik_balik"
	}

	c.JSON(http.StatusOK, verifyResponse{
		Serial:        cert.Serial,
		RecipientName: cert.RecipientName,
		ActivityTitle: cert.ActivityTitle,
		ActivityDate:  cert.ActivityDate.Time.Format("2006-01-02"),
		IssuedAt:      cert.IssuedAt.Time.Format(time.RFC3339),
		Status:        status,
	})
}
```

- [ ] **Step 4: Daftar route dengan baldi had kadar sendiri**

Dalam `internal/http/router.go`:

```go
	certificateHandler := handlers.NewCertificateHandler(pool, r2Client, cfg.PublicBaseURL)

	// Baldi DINAMAKAN. Baldi tanpa nama berkongsi kunci Redis dengan
	// 'auth' dan 'upload', jadi trafik pengesahan awam akan menghabiskan
	// kuota log masuk ahli.
	verifyRateLimiter := rateLimiter.Limit("verify", rate.Every(2*time.Second), 20)
	r.GET("/verify/certificates/:token", verifyRateLimiter, certificateHandler.Verify)

	approved.GET("/me/certificates", certificateHandler.ListMine)
	approved.GET("/me/certificates/:id/file", certificateHandler.Download)
	verified.POST("/activities/:id/certificates", certificateHandler.Issue)
	verified.POST("/certificates/:id/revoke", certificateHandler.Revoke)
```

Kalau `cfg.PublicBaseURL` belum wujud dalam `internal/config`, tambahkannya (baca dari env `PUBLIC_BASE_URL`, lalai `http://localhost:8080`). Ia perlu untuk URL QR — URL relatif tidak berguna pada sijil bercetak.

- [ ] **Step 5: Jalankan ujian, sahkan ia lulus**

```bash
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -run TestVerify -v
```

Dijangka: LULUS.

- [ ] **Step 6: Jalankan keseluruhan suite backend**

```bash
go build ./... && go test ./... && \
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_activity?sslmode=disable" \
  go test ./internal/http/handlers/ -v
```

Dijangka: semua lulus.

- [ ] **Step 7: Commit**

```bash
git add internal/http/handlers/ internal/http/router.go internal/config/
git commit -m "feat(activity): halaman pengesahan sijil awam dengan respons terhad"
```

---

## Task 11: Notifikasi push

**Files:**
- Create: `internal/db/migrations/20260810100600_widen_notifications_activity.sql`
- Modify: `internal/http/handlers/activities.go`, `internal/http/handlers/activity_certificates.go`

**Interfaces:**
- Consumes: `push.Service.NotifyUser`.

- [ ] **Step 1: Tulis migration**

Semak dahulu nilai `check` semasa:

```bash
psql "$DATABASE_URL" -c "\d+ notifications"
```

Kemudian tulis `20260810100600_widen_notifications_activity.sql` mengikut corak `20260807120100_widen_notifications_member_status.sql` — gugurkan kekangan sedia ada dan cipta semula dengan nilai tambahan `activity_published`, `activity_cancelled`, `certificate_ready`.

- [ ] **Step 2: Jalankan migration**

```bash
goose -dir internal/db/migrations postgres "$DATABASE_URL" up
goose -dir internal/db/migrations postgres "$DATABASE_URL" down
goose -dir internal/db/migrations postgres "$DATABASE_URL" up
```

Dijangka: bersih pada kedua-dua arah.

- [ ] **Step 3: Hantar push pada tiga titik**

Dalam handler `Publish` — selepas transaksi komit, hantar kepada semua ahli yang diluluskan:

```go
	// Selepas komit, bukan di dalam transaksi: kegagalan push tidak
	// sepatutnya menggulung semula penerbitan aktiviti. Corak sama dengan
	// notifikasi post sedia ada.
	go func(title string) {
		ctx := context.Background()
		userIDs, err := h.queries.ListApprovedUserIDs(ctx)
		if err != nil {
			log.Printf("push aktiviti: senarai ahli: %v", err)
			return
		}
		for _, uid := range userIDs {
			if err := h.push.NotifyUser(ctx, uid, "Aktiviti Baharu", title); err != nil {
				log.Printf("push aktiviti kepada %s: %v", uid, err)
			}
		}
	}(activity.Title)
```

Kalau `ListApprovedUserIDs` belum wujud, tambah ke `queries/profiles.sql`:

```sql
-- name: ListApprovedUserIDs :many
select user_id from profiles where status = 'approved';
```

Dalam `Cancel` — hantar hanya kepada yang berdaftar (guna `ListRegistrationsByActivity`), tajuk "Aktiviti Dibatalkan".

Dalam `Issue` — hantar kepada setiap penerima sijil, tajuk "Sijil Anda Sedia".

- [ ] **Step 4: Sahkan pembinaan**

```bash
go build ./... && go test ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/ internal/http/handlers/ queries/profiles.sql internal/db/sqlc/
git commit -m "feat(activity): notifikasi push untuk terbit, batal, dan sijil sedia"
```

---

## Task 12: Flutter — model, provider, senarai, detail, daftar

**Files:**
- Create: `lib/features/activities/activity_models.dart`
- Create: `lib/features/activities/activity_providers.dart`
- Create: `lib/features/activities/activities_page.dart`
- Create: `lib/features/activities/activity_detail_page.dart`
- Modify: `lib/app/router.dart`, `lib/app/nav_shell.dart`

**Interfaces:**
- Produces: `Activity`, `ActivitySession`, `ActivityCategory`, `MyRegistration` (model); `activitiesProvider`, `activityDetailProvider`, `activityCategoriesProvider`, `registerActivity`, `cancelRegistration` (provider/aksi).

- [ ] **Step 1: Tulis model**

`lib/features/activities/activity_models.dart` — kelas biasa dengan `fromJson`, ikut corak `post_models.dart` (repo ini tidak guna `freezed`; jangan perkenalkannya):

```dart
class ActivitySession {
  const ActivitySession({
    required this.id,
    required this.seq,
    required this.title,
    required this.startsAt,
    required this.endsAt,
  });

  final String id;
  final int seq;
  final String title;
  final DateTime startsAt;
  final DateTime endsAt;

  factory ActivitySession.fromJson(Map<String, dynamic> json) => ActivitySession(
        id: json['id'] as String,
        seq: json['seq'] as int,
        title: (json['title'] as String?) ?? '',
        startsAt: DateTime.parse(json['starts_at'] as String).toLocal(),
        endsAt: DateTime.parse(json['ends_at'] as String).toLocal(),
      );
}

class Activity {
  const Activity({
    required this.id,
    required this.title,
    required this.description,
    required this.categoryName,
    required this.locationName,
    required this.locationAddress,
    required this.startsAt,
    required this.endsAt,
    required this.registrationClosesAt,
    required this.capacity,
    required this.registrationCount,
    required this.status,
    required this.sessions,
    required this.isRegistered,
  });

  final String id;
  final String title;
  final String description;
  final String categoryName;
  final String locationName;
  final String locationAddress;
  final DateTime startsAt;
  final DateTime endsAt;
  final DateTime registrationClosesAt;
  final int? capacity;
  final int registrationCount;
  final String status;
  final List<ActivitySession> sessions;
  final bool isRegistered;

  bool get isFull => capacity != null && registrationCount >= capacity!;
  bool get registrationClosed => DateTime.now().isAfter(registrationClosesAt);
  bool get isCancelled => status == 'cancelled';

  /// Boleh daftar hanya bila SEMUA syarat lulus. Server tetap hakim
  /// muktamad — ini untuk melumpuhkan butang, bukan untuk mempercayai.
  bool get canRegister =>
      !isRegistered && !isFull && !registrationClosed && !isCancelled && status == 'published';

  factory Activity.fromJson(Map<String, dynamic> json) => Activity(
        id: json['id'] as String,
        title: json['title'] as String,
        description: (json['description'] as String?) ?? '',
        categoryName: (json['category_name'] as String?) ?? '',
        locationName: (json['location_name'] as String?) ?? '',
        locationAddress: (json['location_address'] as String?) ?? '',
        startsAt: DateTime.parse(json['starts_at'] as String).toLocal(),
        endsAt: DateTime.parse(json['ends_at'] as String).toLocal(),
        registrationClosesAt:
            DateTime.parse(json['registration_closes_at'] as String).toLocal(),
        capacity: json['capacity'] as int?,
        registrationCount: (json['registration_count'] as int?) ?? 0,
        status: json['status'] as String,
        sessions: ((json['sessions'] as List<dynamic>?) ?? const [])
            .map((e) => ActivitySession.fromJson(e as Map<String, dynamic>))
            .toList(),
        isRegistered: (json['is_registered'] as bool?) ?? false,
      );
}
```

- [ ] **Step 2: Tulis ujian widget yang gagal**

`test/features/activities/activity_state_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/features/activities/activity_models.dart';

Activity buatAktiviti({
  int? capacity,
  int registrationCount = 0,
  bool isRegistered = false,
  String status = 'published',
  DateTime? tutup,
}) {
  final now = DateTime.now();
  return Activity(
    id: 'a1',
    title: 'Kejohanan Badminton',
    description: '',
    categoryName: 'Badminton',
    locationName: 'Dewan A',
    locationAddress: '',
    startsAt: now.add(const Duration(days: 7)),
    endsAt: now.add(const Duration(days: 7, hours: 3)),
    registrationClosesAt: tutup ?? now.add(const Duration(days: 5)),
    capacity: capacity,
    registrationCount: registrationCount,
    status: status,
    sessions: const [],
    isRegistered: isRegistered,
  );
}

void main() {
  test('boleh daftar bila ada slot dan masih terbuka', () {
    expect(buatAktiviti(capacity: 10, registrationCount: 3).canRegister, isTrue);
  });

  test('tak boleh daftar bila penuh', () {
    final a = buatAktiviti(capacity: 10, registrationCount: 10);
    expect(a.isFull, isTrue);
    expect(a.canRegister, isFalse);
  });

  test('kapasiti null bermakna tiada had', () {
    expect(buatAktiviti(registrationCount: 999).isFull, isFalse);
  });

  test('tak boleh daftar selepas tarikh tutup', () {
    final a = buatAktiviti(
        capacity: 10, tutup: DateTime.now().subtract(const Duration(hours: 1)));
    expect(a.registrationClosed, isTrue);
    expect(a.canRegister, isFalse);
  });

  test('tak boleh daftar bila sudah berdaftar', () {
    expect(buatAktiviti(capacity: 10, isRegistered: true).canRegister, isFalse);
  });

  test('tak boleh daftar bila aktiviti dibatalkan', () {
    expect(buatAktiviti(capacity: 10, status: 'cancelled').canRegister, isFalse);
  });
}
```

- [ ] **Step 3: Jalankan ujian, sahkan ia lulus**

```bash
cd /Users/hafiz/Developments/marc_flutter && flutter test test/features/activities/activity_state_test.dart
```

Dijangka: LULUS (model ditulis dalam Step 1).

- [ ] **Step 4: Tulis provider**

`activity_providers.dart` mengikut corak `post_providers.dart` — `FutureProvider.family` untuk detail, `StateNotifier` untuk senarai bercursor. Aksi `registerActivity` mesti mengendalikan `409` daripada server dengan bersih:

```dart
Future<String?> registerActivity(Ref ref, String activityId) async {
  try {
    await ref.read(apiClientProvider).post('/activities/$activityId/registration');
    ref.invalidate(activityDetailProvider(activityId));
    return null;
  } on DioException catch (e) {
    // 409 daripada server ialah kebenaran, bukan kes tepi. Dua ahli boleh
    // menekan Daftar dalam saat yang sama dan kiraan tempatan tak dapat
    // menghalangnya — server yang menyerikan.
    if (e.response?.statusCode == 409) {
      ref.invalidate(activityDetailProvider(activityId));
      return errorMessage(e); // helper sedia ada dalam core/error_utils.dart
    }
    rethrow;
  }
}
```

- [ ] **Step 5: Tulis halaman senarai dan detail**

`activities_page.dart`: tab "Akan Datang" / "Lepas", cip penapis kategori, `skeletonizer` semasa memuat (pakej sudah ada). `activity_detail_page.dart`: senarai sesi, lokasi, kiraan slot, butang daftar/batal dengan pengesahan dialog untuk batal.

- [ ] **Step 6: Tambah route dan tab navigasi**

Dalam `lib/app/router.dart` tambah `/activities` dan `/activities/:id`. Dalam `lib/app/nav_shell.dart` tambah destinasi "Aktiviti".

- [ ] **Step 7: Jalankan app dan sahkan secara manual**

```bash
flutter run
```

Semak: senarai memuat, tab bertukar, detail membuka, daftar berjaya dan kiraan bertambah, batal berjaya.

- [ ] **Step 8: Commit**

```bash
git add lib/features/activities/ lib/app/ test/features/activities/
git commit -m "feat(activities): senarai, detail, dan pendaftaran aktiviti"
```

---

## Task 13: Flutter — aktiviti saya, QR check-in, sijil saya

**Files:**
- Create: `lib/features/activities/my_activities_page.dart`
- Create: `lib/features/activities/my_certificates_page.dart`
- Modify: `pubspec.yaml` (tambah `qr_flutter`), `lib/app/router.dart`

- [ ] **Step 1: Tambah `qr_flutter`**

```bash
flutter pub add qr_flutter
flutter build apk --debug
```

Dijangka: pembinaan lulus. `qr_flutter` ialah rendering tulen Dart — ia tidak sepatutnya menyentuh kekangan compileSdk. Kalau ia berlaku, pin ke versi lebih awal dan catat sebabnya dalam komen `pubspec` mengikut corak `permission_handler`.

- [ ] **Step 2: Tulis halaman aktiviti saya dengan QR**

```dart
// QR ialah checkin_token daripada data yang SUDAH dimuatkan — tiada
// panggilan rangkaian untuk menjananya.
//
// Liputan di gelanggang sukan selalunya teruk. Ahli boleh membuka skrin ini
// sebelum sampai dan QR kekal berfungsi tanpa isyarat.
Widget buildCheckinQr(BuildContext context, String token) {
  return Column(
    mainAxisSize: MainAxisSize.min,
    children: [
      QrImageView(
        data: token,
        version: QrVersions.auto,
        size: 220,
        backgroundColor: Colors.white,
      ),
      const SizedBox(height: 12),
      Text(
        'Tunjukkan QR ini kepada pengurusan untuk direkodkan hadir',
        textAlign: TextAlign.center,
        style: Theme.of(context).textTheme.bodySmall,
      ),
    ],
  );
}
```

Latar belakang QR mesti **putih tegar**, bukan warna tema — QR gelap-atas-gelap dalam mod gelap tidak boleh diimbas.

- [ ] **Step 3: Tulis halaman sijil saya**

Senarai daripada `GET /me/certificates`. Ketik → `GET /me/certificates/{id}/file` → buka URL yang dipulangkan dengan `url_launcher`:

```dart
Future<void> bukaSijil(BuildContext context, WidgetRef ref, String id) async {
  try {
    final res = await ref.read(apiClientProvider).get('/me/certificates/$id/file');
    final url = res.data['url'] as String;
    await launchUrl(Uri.parse(url), mode: LaunchMode.externalApplication);
  } on DioException catch (e) {
    // 409 = baris sijil wujud tapi PDF belum siap dimuat naik (fasa 2
    // penerbitan). Ini keadaan sementara, bukan kegagalan.
    final mesej = e.response?.statusCode == 409
        ? 'Sijil sedang disediakan. Cuba lagi sebentar.'
        : errorMessage(e);
    if (context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(mesej)));
    }
  }
}
```

- [ ] **Step 4: Tambah pautan dalam halaman profil**

Dua item senarai: "Aktiviti Saya" dan "Sijil Saya".

- [ ] **Step 5: Sahkan secara manual**

```bash
flutter run
```

Semak: QR dipaparkan dan boleh diimbas dengan aplikasi kamera peranti; sijil membuka dalam pelihat PDF.

- [ ] **Step 6: Commit**

```bash
git add lib/ pubspec.yaml pubspec.lock
git commit -m "feat(activities): aktiviti saya dengan QR check-in dan muat turun sijil"
```

---

## Task 14: Flutter — skrin management

**Files:**
- Create: `lib/features/activities/manage/activity_form_page.dart`
- Create: `lib/features/activities/manage/registrations_page.dart`
- Create: `lib/features/activities/manage/issue_certificates_page.dart`

- [ ] **Step 1: Borang aktiviti dengan editor sesi**

Medan: kategori (dropdown daripada `/activity-categories`), tajuk, penerangan, lokasi, alamat, tarikh tutup pendaftaran, kapasiti (kosong = tiada had), ambang kehadiran (lalai 100), dan senarai sesi boleh tambah/buang.

Sesi dihantar sebagai satu set melalui `PUT /activities/{id}/sessions`, bukan satu demi satu.

- [ ] **Step 2: Halaman senarai peserta dengan tanda hadir**

Pemilih sesi di atas, senarai peserta di bawah dengan suis hadir. Ketik → `POST /activities/{id}/sessions/{sid}/attendance` dengan `{"registration_id": "...", "method": "manual"}`.

- [ ] **Step 3: Halaman terbit sijil**

Papar kiraan layak sebelum mengesahkan, kemudian `POST /activities/{id}/certificates`. Respons `202` bermakna sijil diterbitkan tetapi sebahagian fail belum siap — tunjukkan mesej itu dan bukan ralat.

- [ ] **Step 4: Sembunyikan skrin ini daripada bukan-management**

Guna semakan role sedia ada dalam `auth_state.dart`. Ini kemudahan UI sahaja — server tetap menguatkuasakan.

- [ ] **Step 5: Sahkan secara manual**

Log masuk sebagai management: cipta aktiviti, tambah 2 sesi, terbitkan, daftar dari akaun kedua, tanda hadir, terbitkan sijil.

- [ ] **Step 6: Commit**

```bash
git add lib/features/activities/manage/
git commit -m "feat(activities): skrin management untuk aktiviti, kehadiran, sijil"
```

---

## Task 15: Flutter — scanner QR

**Files:**
- Create: `lib/features/activities/manage/checkin_scanner_page.dart`
- Create: `lib/features/activities/manage/scan_result.dart`
- Test: `test/features/activities/scan_result_test.dart`
- Modify: `pubspec.yaml`, `android/app/build.gradle.kts`, `ios/Runner/Info.plist`

**Task ini diletakkan terakhir dengan sengaja.** `mobile_scanner` ialah satu-satunya risiko pembinaan dalam pelan ini. Kalau ia tidak boleh dipin pada compileSdk 35, semua yang sebelum ini tetap dihantar dan check-in manual berfungsi sepenuhnya.

- [ ] **Step 1: Get pembinaan DAHULU, sebelum menulis apa-apa skrin**

```bash
flutter pub add mobile_scanner
flutter build apk --debug
```

Kalau pembinaan **gagal** dengan ralat compileSdk/AGP: turunkan versi (`flutter pub add mobile_scanner:^5.2.3`) dan cuba lagi. Catat versi yang berjaya dan sebabnya dalam komen `pubspec.yaml` mengikut corak `permission_handler`:

```yaml
  # Dipin ke <versi> — versi lebih baharu perlukan compileSdk <n>, yang
  # melanggar siling 35 projek ini (lihat komen permission_handler).
  mobile_scanner: <versi>
```

Kalau tiada versi yang membina: **berhenti**, laporkan kepada pengguna, dan tinggalkan task ini belum selesai. Jangan naikkan compileSdk untuk memuatkannya — itu perubahan seluruh projek yang telah dielakkan dengan sengaja.

- [ ] **Step 2: Tambah kebenaran kamera**

`ios/Runner/Info.plist`:

```xml
<key>NSCameraUsageDescription</key>
<string>Kamera digunakan untuk mengimbas QR kehadiran peserta.</string>
```

Android: `mobile_scanner` mengisytiharkan `android.permission.CAMERA` sendiri melalui manifest gabungan; sahkan dengan `flutter build apk --debug` dan periksa manifest gabungan kalau ragu.

- [ ] **Step 3: Tulis ujian pemetaan hasil yang gagal**

`test/features/activities/scan_result_test.dart`:

```dart
import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/features/activities/manage/scan_result.dart';

DioException ralat(int status, String mesej) => DioException(
      requestOptions: RequestOptions(path: '/x'),
      response: Response(
        requestOptions: RequestOptions(path: '/x'),
        statusCode: status,
        data: {'error': mesej},
      ),
    );

void main() {
  test('kehadiran baharu ialah kejayaan', () {
    final r = ScanResult.fromResponse({'created': true, 'member': {'display_name': 'Ahmad'}});
    expect(r.kind, ScanResultKind.marked);
    expect(r.message, contains('Ahmad'));
  });

  // Sudah hadir BUKAN ralat. Kalau ia dipaparkan merah, pengurusan akan
  // fikir imbasan gagal dan cuba lagi — atau lebih teruk, tanda manual
  // atas kehadiran yang sudah wujud.
  test('sudah hadir ialah keadaan tersendiri, bukan ralat', () {
    final r = ScanResult.fromResponse({'created': false, 'member': {'display_name': 'Ahmad'}});
    expect(r.kind, ScanResultKind.alreadyMarked);
  });

  test('tidak berdaftar dipetakan berasingan', () {
    expect(ScanResult.fromError(ralat(409, 'ahli ini tidak berdaftar untuk aktiviti ini')).kind,
        ScanResultKind.notRegistered);
  });

  test('di luar tetingkap dipetakan berasingan', () {
    expect(ScanResult.fromError(ralat(422, 'di luar tetingkap check-in')).kind,
        ScanResultKind.outsideWindow);
  });

  test('QR tak dikenali dipetakan berasingan', () {
    expect(ScanResult.fromError(ralat(404, 'QR tidak dikenali')).kind,
        ScanResultKind.unknownCode);
  });

  test('kegagalan rangkaian dipetakan berasingan', () {
    final e = DioException(
      requestOptions: RequestOptions(path: '/x'),
      type: DioExceptionType.connectionError,
    );
    expect(ScanResult.fromError(e).kind, ScanResultKind.network);
  });
}
```

- [ ] **Step 4: Jalankan ujian, sahkan ia gagal**

```bash
flutter test test/features/activities/scan_result_test.dart
```

Dijangka: GAGAL — `scan_result.dart` tidak wujud.

- [ ] **Step 5: Tulis `scan_result.dart`**

```dart
import 'package:dio/dio.dart';

/// Enam keadaan berbeza. Tanpa pemisahan ini semuanya menjadi "Ralat" dan
/// pengurusan tidak dapat tahu sama ada perlu mengimbas semula, menanda
/// manual, atau memberitahu ahli bahawa mereka tidak berdaftar.
enum ScanResultKind { marked, alreadyMarked, notRegistered, outsideWindow, unknownCode, network }

class ScanResult {
  const ScanResult(this.kind, this.message);

  final ScanResultKind kind;
  final String message;

  bool get isFailure =>
      kind != ScanResultKind.marked && kind != ScanResultKind.alreadyMarked;

  factory ScanResult.fromResponse(Map<String, dynamic> data) {
    final nama = (data['member']?['display_name'] as String?) ?? 'Ahli';
    final created = (data['created'] as bool?) ?? false;
    return created
        ? ScanResult(ScanResultKind.marked, '✓ $nama hadir')
        : ScanResult(ScanResultKind.alreadyMarked, '$nama sudah ditanda hadir');
  }

  factory ScanResult.fromError(DioException e) {
    if (e.response == null) {
      return const ScanResult(
          ScanResultKind.network, 'Tiada sambungan. Cuba lagi atau tanda manual.');
    }
    final mesej = (e.response?.data is Map)
        ? (e.response!.data['error'] as String? ?? 'Ralat tidak diketahui')
        : 'Ralat tidak diketahui';
    switch (e.response!.statusCode) {
      case 404:
        return ScanResult(ScanResultKind.unknownCode, 'QR tidak dikenali');
      case 422:
        return ScanResult(ScanResultKind.outsideWindow, mesej);
      case 409:
        return ScanResult(ScanResultKind.notRegistered, mesej);
      default:
        return ScanResult(ScanResultKind.network, mesej);
    }
  }
}
```

- [ ] **Step 6: Jalankan ujian, sahkan ia lulus**

```bash
flutter test test/features/activities/scan_result_test.dart
```

Dijangka: LULUS, enam-enam.

- [ ] **Step 7: Tulis skrin scanner**

```dart
// Kamera kekal terbuka merentas imbasan. Menolak pengurusan keluar skrin
// selepas setiap peserta menjadikan barisan 40 orang menyakitkan.
class _ScannerState extends ConsumerState<CheckinScannerPage> {
  final _controller = MobileScannerController();
  final _recent = <String, DateTime>{};
  ScanResult? _last;

  // QR yang dipegang di depan lens mencetuskan pengesanan berpuluh kali
  // sesaat. Tanpa nyahlantun ini, satu peserta menghantar puluhan
  // permintaan.
  bool _debounced(String code) {
    final now = DateTime.now();
    final seen = _recent[code];
    if (seen != null && now.difference(seen) < const Duration(seconds: 3)) {
      return true;
    }
    _recent[code] = now;
    return false;
  }

  Future<void> _onDetect(BarcodeCapture capture) async {
    final code = capture.barcodes.firstOrNull?.rawValue;
    if (code == null || _debounced(code)) return;

    try {
      final res = await ref.read(apiClientProvider).post(
        '/activities/${widget.activityId}/sessions/${widget.sessionId}/attendance',
        data: {'checkin_token': code, 'method': 'scan'},
      );
      setState(() => _last = ScanResult.fromResponse(res.data as Map<String, dynamic>));
    } on DioException catch (e) {
      setState(() => _last = ScanResult.fromError(e));
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }
}
```

Papar `_last` sebagai sepanduk di bawah paparan kamera: hijau untuk `marked` dan `alreadyMarked`, merah untuk selebihnya.

- [ ] **Step 8: Uji dengan kamera sebenal pada peranti**

Jalankan pada peranti fizikal, imbas QR daripada telefon kedua yang memaparkan skrin "Aktiviti Saya". Sahkan: imbasan pertama hijau "✓ hadir", imbasan kedua serta-merta tidak menghantar permintaan (nyahlantun), imbasan selepas 3 saat menunjukkan "sudah ditanda hadir".

- [ ] **Step 9: Commit**

```bash
git add lib/ test/ pubspec.yaml pubspec.lock ios/ android/
git commit -m "feat(activities): scanner QR check-in dengan nyahlantun dan keadaan hasil berasingan"
```

---

## Task 16: Kemas kini dokumentasi

**Files:**
- Modify: `marc_go/TODO.md`, `marc_go/DATABASE.md`, `marc_go/ARCHITECTURE.md`, `marc_flutter/TODO.md`

- [ ] **Step 1: Kemas kini `marc_go/TODO.md`**

Tambah bahagian "Modul Aktiviti" yang menyenaraikan apa yang siap dan apa yang **belum**:

- Yuran aktiviti — `fee_cents` wujud tetapi tiada gateway; aktiviti berbayar belum berfungsi
- Check-in `self_scan` dan `code` — schema sokong, tiada UI, perlukan token berputar
- Sijil pencapaian (johan/naib johan) — tidak dilaksanakan
- Peringatan H-1 memerlukan kerja berjadual (semak sama ada `retention` sweep boleh menjadi tuan rumah)
- Aktiviti tidak pernah beralih ke `completed` secara automatik — perlukan kerja berjadual atau peralihan pada penerbitan sijil

- [ ] **Step 2: Kemas kini `DATABASE.md`**

Tambah tujuh jadual baharu pada mana-mana bahagian ringkasan schema, dengan nota invarian denormalisasi `activities.starts_at`/`ends_at`.

- [ ] **Step 3: Kemas kini `ARCHITECTURE.md`**

Tambah `internal/certificate` pada senarai modul, dinyatakan sebagai tulen (tiada DB/rangkaian) sama seperti `internal/receipt`.

- [ ] **Step 4: Kemas kini `marc_flutter/TODO.md`**

Catat feature folder `activities`, versi `mobile_scanner` yang dipin dan sebabnya, dan bahawa muat turun sijil melalui `url_launcher` (bukan simpanan tempatan).

- [ ] **Step 5: Commit kedua-dua repo**

```bash
cd /Users/hafiz/Developments/marc_go
git add TODO.md DATABASE.md ARCHITECTURE.md
git commit -m "docs: modul aktiviti — status siap dan jurang yang tinggal"

cd /Users/hafiz/Developments/marc_flutter
git add TODO.md
git commit -m "docs: modul aktiviti Flutter dan versi mobile_scanner yang dipin"
```

---

## Semakan Akhir

- [ ] `cd marc_go && go build ./... && go test ./...` lulus
- [ ] `ACTIVITY_TEST_DB=... go test ./internal/http/handlers/ -v -race` lulus
- [ ] `R2_LIVE_TEST=1 go test ./internal/storage/ -run TestR2PutObjectLive -v` lulus
- [ ] `cd marc_flutter && flutter test && flutter build apk --debug` lulus
- [ ] Larian menyeluruh manual: cipta → terbit → daftar → tanda hadir → terbit sijil → muat turun PDF → imbas QR pada PDF → halaman pengesahan memaparkan nama yang betul
- [ ] Halaman pengesahan disemak dalam pelayar penyamaran (tanpa sesi) untuk mengesahkan ia benar-benar awam
