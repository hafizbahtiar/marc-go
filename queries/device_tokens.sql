-- name: UpsertDeviceToken :execrows
insert into device_tokens (user_id, onesignal_id, platform, updated_at)
values ($1, $2, $3, now())
on conflict (onesignal_id) do update set
  user_id = excluded.user_id,
  platform = excluded.platform,
  updated_at = now()
where device_tokens.user_id = excluded.user_id;

-- name: DeleteDeviceToken :exec
delete from device_tokens where id = $1 and user_id = $2;

-- name: DeleteDeviceTokenByOnesignalID :exec
delete from device_tokens where onesignal_id = $1 and user_id = $2;

-- name: ListDeviceTokensByUser :many
select * from device_tokens where user_id = $1;
