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
