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
-- attended_session_ids menjawab "sesi mana pendaftaran ini sudah hadir?" —
-- skrin kehadiran pengurusan menyemai suisnya daripada medan ini. Satu
-- boolean per peserta tidak mencukupi: kehadiran ialah per-sesi.
--
-- Agregat dikira SEKALI dalam subkueri berkumpulan, bukan satu kueri per
-- pendaftaran; senarai ini dibaca dengan seluruh peserta sekali gus, jadi
-- N+1 di sini bermakna satu perjalanan DB bagi setiap ahli.
--
-- coalesce(..., '{}') penting: left join memberi NULL untuk pendaftaran
-- tanpa kehadiran, dan NULL bersiri sebagai `null` dalam JSON. Klien yang
-- memanggil .map atasnya terhempas — [] ialah kontrak.
select r.*, pr.member_id, pr.display_name, pr.avatar_r2_key,
  coalesce(att.session_ids, '{}')::uuid[] as attended_session_ids
from activity_registrations r
join profiles pr on pr.user_id = r.user_id
left join (
  select at.registration_id, array_agg(at.session_id order by s.seq) as session_ids
  from activity_attendances at
  join activity_sessions s on s.id = at.session_id
  where s.activity_id = $1
  group by at.registration_id
) att on att.registration_id = r.id
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
