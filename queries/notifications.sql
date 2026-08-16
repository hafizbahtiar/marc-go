-- name: CreateNotification :one
insert into notifications (recipient_id, actor_id, type, post_id, comment_id, activity_id, certificate_id)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: ListNotifications :many
select * from notifications
where recipient_id = $1
  and (
    sqlc.narg('cursor_created_at')::timestamptz is null
    or (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
order by created_at desc, id desc
limit sqlc.arg('row_limit');

-- name: MarkNotificationRead :exec
update notifications set read_at = now() where id = $1 and recipient_id = $2 and read_at is null;

-- name: MarkAllNotificationsRead :exec
update notifications set read_at = now() where recipient_id = $1 and read_at is null;
