-- name: IsUserCurrentlyBanned :one
select exists (
  select 1
  from profiles
  where user_id = $1
    and banned_at is not null
    and (ban_expires_at is null or ban_expires_at > now())
);

-- name: ListBannedProfiles :many
select
  p.user_id,
  p.member_id,
  p.display_name,
  u.email,
  r.key as role_key,
  p.banned_at,
  p.ban_expires_at,
  p.ban_reason,
  p.banned_by
from profiles p
join users u on u.id = p.user_id
join roles r on r.id = p.role_id
where p.banned_at is not null
  and (p.ban_expires_at is null or p.ban_expires_at > now())
order by p.ban_expires_at nulls first, p.banned_at desc;

-- name: BanProfile :one
update profiles
set banned_at = now(),
    ban_expires_at = sqlc.narg('ban_expires_at')::timestamptz,
    ban_reason = $2,
    banned_by = $3,
    updated_at = now()
where user_id = $1
  and banned_at is null
returning *;

-- name: UnbanProfile :one
update profiles
set banned_at = null,
    ban_expires_at = null,
    ban_reason = null,
    banned_by = null,
    updated_at = now()
where user_id = $1
  and banned_at is not null
returning *;
