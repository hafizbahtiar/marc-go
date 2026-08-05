-- name: CreateProfile :one
insert into profiles (user_id, member_id, role_id)
values ($1, $2, $3)
returning *;

-- name: GetProfileByUserID :one
select
  p.*,
  u.email as email,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
where p.user_id = $1;

-- name: UpdateProfile :one
update profiles
set display_name = $2, phone = $3
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

-- name: ListProfiles :many
select
  p.*,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category
from profiles p
join roles r on r.id = p.role_id
order by p.member_id;
