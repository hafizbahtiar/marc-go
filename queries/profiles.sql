-- name: CreateProfile :one
insert into profiles (user_id, member_id, role_id, phone)
values ($1, $2, $3, $4)
returning *;

-- name: GetProfileByUserID :one
select
  p.*,
  u.email as email,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category,
  r.rank as role_rank
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
where p.user_id = $1;

-- name: UpdateProfile :one
update profiles
set
  display_name = coalesce(sqlc.narg('display_name')::text, display_name),
  phone = coalesce(sqlc.narg('phone')::text, phone)
where user_id = $1
returning *;

-- name: UpdateProfileRole :one
update profiles
set role_id = $2
where user_id = $1
returning *;

-- name: MarkEmailVerified :exec
update profiles set email_verified = true where user_id = $1;

-- name: GetRoleCategoryByUserID :one
select r.category
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
--   max_rank             — siling hierarki keterlihatan; lihat
--                          `visibleRankCeiling` di handlers/profile.go
--   status               — penapis pilihan (cth 'pending' utk barisan
--                          kelulusan management)
--   include_all_statuses — management sahaja. Ahli biasa cuma nampak ahli
--                          berstatus 'approved' (+ baris dia sendiri,
--                          apa pun statusnya)
select
  p.*,
  u.email as email,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category,
  r.rank as role_rank,
  -- Status bayaran yuran pendaftaran TERKINI (utamakan 'succeeded' —
  -- padanan `GetLatestRegistrationPaymentStatus`, sebab sama: checkout
  -- berulang boleh cipta >1 baris). String KOSONG = ahli tak pernah
  -- cuba bayar (coalesce, BUKAN NULL — sqlc infer tak konsisten
  -- nullability keputusan LEFT JOIN LATERAL, string kosong lebih
  -- selamat drpd risiko crash scan NULL->string). Ditambah 2026-08-15
  -- supaya management NAMPAK siapa dah bayar SEBELUM tekan Luluskan,
  -- bukan dapat ralat lepas fakta (gate `ApproveMember` sedia ada sejak
  -- awal, cuma tak kelihatan di sini).
  coalesce(latest_payment.status, '') as registration_payment_status
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
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

-- name: ListManagementUserIDs :many
select p.user_id
from profiles p
join roles r on r.id = p.role_id
where r.category = $1;

-- name: UpdateProfileAvatar :one
update profiles set avatar_r2_key = sqlc.narg('avatar_r2_key')::text
where user_id = $1
returning *;

-- name: ListApprovedUserIDs :many
-- Penerima siaran seluruh kelab (cth aktiviti baharu diterbitkan).
select user_id from profiles where status = 'approved';
