-- name: CreatePost :one
insert into posts (author_id, type, content)
values ($1, $2, $3)
returning *;

-- name: GetPostByID :one
select
  p.*,
  u.email as author_email,
  pr.member_id as author_member_id,
  pr.display_name as author_display_name
from posts p
join users u on u.id = p.author_id
join profiles pr on pr.user_id = p.author_id
where p.id = $1 and p.deleted_at is null;

-- name: ListPosts :many
-- Cursor-based: pulang post yang created_at lebih lama daripada cursor
-- (null cursor = page pertama).
select
  p.*,
  u.email as author_email,
  pr.member_id as author_member_id,
  pr.display_name as author_display_name
from posts p
join users u on u.id = p.author_id
join profiles pr on pr.user_id = p.author_id
where p.deleted_at is null
  and (sqlc.narg('cursor_created_at')::timestamptz is null or p.created_at < sqlc.narg('cursor_created_at'))
order by p.created_at desc
limit sqlc.arg('row_limit');

-- name: UpdatePost :one
update posts
set content = $2, edited_at = now()
where id = $1 and deleted_at is null
returning *;

-- name: SoftDeletePost :exec
update posts set deleted_at = now() where id = $1;

-- name: GetPostAuthorID :one
select author_id from posts where id = $1 and deleted_at is null;

-- name: CreatePostImage :one
insert into post_images (post_id, r2_key, "position")
values ($1, $2, $3)
returning *;

-- name: ListPostImagesByPostIDs :many
select * from post_images where post_id = any(sqlc.arg('post_ids')::uuid[]) order by post_id, "position";
