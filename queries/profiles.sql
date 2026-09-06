-- name: CreateProfile :one
insert into profiles (user_id, member_id, staff_id, role_id, phone)
values ($1, $2, $3, $4, $5)
returning *;

-- name: GetProfileByUserID :one
select
  p.*,
  u.email as email,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category,
  r.rank as role_rank,
  d.name as department_name
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
left join departments d on d.code = p.department_code
where p.user_id = $1;

-- name: UpdateProfile :one
update profiles
set
  display_name = coalesce(sqlc.narg('display_name')::text, display_name),
  phone = coalesce(sqlc.narg('phone')::text, phone),
  emergency_contact_name = coalesce(sqlc.narg('emergency_contact_name')::text, emergency_contact_name),
  emergency_contact_phone = coalesce(sqlc.narg('emergency_contact_phone')::text, emergency_contact_phone),
  health_notes = coalesce(sqlc.narg('health_notes')::text, health_notes),
  updated_at = now()
where user_id = $1
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: UpdateProfileActive :one
-- Status AKTIF/TAK AKTIF keahlian - berasingan drpd `status` (kelulusan).
-- Management sahaja (dikuatkuasakan handler), padanan pola UpdateProfileRole.
update profiles
set is_active = $2, updated_at = now()
where user_id = $1
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: UpdateProfileDepartment :one
-- Bahagian/jawatan ahli - management (manager ke atas) sahaja. Semantik
-- GANTI PENUH (bukan partial-coalesce macam UpdateProfile) - handler
-- hantar nilai akhir terus (Valid:false = kosongkan), sebab tindakan ni
-- satu borang "tetapkan bahagian+jawatan skrg", bukan patch berperingkat.
update profiles
set department_code = sqlc.narg('department_code')::text,
  position = sqlc.narg('position')::text,
  updated_at = now()
where user_id = sqlc.arg('user_id')
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: UpdateProfileRole :one
update profiles
set role_id = $2, updated_at = now()
where user_id = $1
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: MarkEmailVerified :exec
update profiles set email_verified = true where user_id = $1;

-- name: GetRoleCategoryByUserID :one
select r.category
from profiles p
join roles r on r.id = p.role_id
where p.user_id = $1;

-- name: GetRoleKeyByUserID :one
-- Utk semakan berasaskan role SPESIFIK (bukan kategori umum) - cth
-- middleware.BlockTesterWrites, yang perlu tahu role 'tester' tepat
-- (category 'ahli' sengaja sama dengan ahli biasa, jadi
-- GetRoleCategoryByUserID tak boleh bezakan dua-dua).
select r.key
from profiles p
join roles r on r.id = p.role_id
where p.user_id = $1;

-- name: GetEmailVerifiedByUserID :one
select email_verified from profiles where user_id = $1;

-- name: GetStatusByUserID :one
select status from profiles where user_id = $1;

-- name: ListVisibleProfiles :many
-- Senarai ahli yang boleh dilihat oleh SEORANG viewer tertentu. Tapisan
-- dibuat di peringkat SQL (bukan dalam Go) supaya baris yang viewer tak
-- layak tengok tak pernah pun keluar dari DB:
--   max_rank             - siling hierarki keterlihatan; lihat
--                          `visibleRankCeiling` di handlers/profile.go
--   status               - penapis pilihan (cth 'pending' utk barisan
--                          kelulusan management)
--   include_all_statuses - management sahaja. Ahli biasa cuma nampak ahli
--                          berstatus 'approved' (+ baris dia sendiri,
--                          apa pun statusnya)
select
  p.*,
  u.email as email,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category,
  r.rank as role_rank,
  -- Status bayaran yuran pendaftaran TERKINI (utamakan 'succeeded' -
  -- padanan `GetLatestRegistrationPaymentStatus`, sebab sama: checkout
  -- berulang boleh cipta >1 baris). String KOSONG = ahli tak pernah
  -- cuba bayar (coalesce, BUKAN NULL - sqlc infer tak konsisten
  -- nullability keputusan LEFT JOIN LATERAL, string kosong lebih
  -- selamat drpd risiko crash scan NULL->string). Ditambah 2026-08-15
  -- supaya management NAMPAK siapa dah bayar SEBELUM tekan Luluskan,
  -- bukan dapat ralat lepas fakta (gate `ApproveMember` sedia ada sejak
  -- awal, cuma tak kelihatan di sini).
  coalesce(latest_payment.status, '') as registration_payment_status,
  d.name as department_name
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
left join departments d on d.code = p.department_code
left join lateral (
  select rp.status
  from registration_payments rp
  where rp.user_id = p.user_id
  order by (rp.status = 'succeeded') desc, rp.created_at desc
  limit 1
) latest_payment on true
where r.rank <= sqlc.arg('max_rank')::int
  and (sqlc.narg('status')::text is null or p.status = sqlc.narg('status')::text)
  and (
    sqlc.arg('include_all_statuses')::boolean
    or p.status = 'approved'
    or p.user_id = sqlc.arg('viewer_id')
  )
order by p.member_id;

-- name: ApproveProfile :one
update profiles
set status = 'approved', approved_by = $2, approved_at = now()
where user_id = $1 and status <> 'approved'
returning *;

-- name: RejectProfile :one
update profiles
set status = 'rejected', approved_by = $2, approved_at = now()
where user_id = $1 and status <> 'rejected'
returning *;

-- name: VerifyStaffID :one
update profiles
set staff_id = coalesce(sqlc.narg('staff_id'), staff_id),
    staff_id_verified_at = now(),
    staff_id_verified_by = @verified_by,
    member_id = coalesce(member_id, @member_id),
    updated_at = now()
where user_id = @user_id
  and staff_id_verified_at is null
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: CorrectStaffID :one
update profiles
set staff_id = @staff_id, updated_at = now()
where user_id = @user_id
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: CorrectMemberID :one
update profiles
set member_id = @member_id, updated_at = now()
where user_id = @user_id
  and member_id is not null
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: ListManagementUserIDs :many
select p.user_id
from profiles p
join roles r on r.id = p.role_id
where r.category = $1;

-- name: UpdateProfileAvatar :one
update profiles set avatar_r2_key = sqlc.narg('avatar_r2_key')::text, updated_at = now()
where user_id = $1
  and (sqlc.narg('expected_updated_at')::timestamptz is null
       or updated_at = sqlc.narg('expected_updated_at')::timestamptz)
returning *;

-- name: ListApprovedUserIDs :many
-- Penerima siaran seluruh kelab (cth aktiviti baharu diterbitkan).
select user_id from profiles where status = 'approved';

-- name: GetUserIDByTelegramChatID :one
select user_id from profiles where telegram_chat_id = $1;

-- name: SetTelegramLink :exec
update profiles
set telegram_chat_id = $2, telegram_username = $3, telegram_linked_at = now()
where user_id = $1;

-- name: ClearTelegramLink :exec
update profiles
set telegram_chat_id = null, telegram_username = null, telegram_linked_at = null
where user_id = $1;
