-- name: CreateComment :one
insert into comments (post_id, parent_comment_id, author_id, content)
values ($1, $2, $3, $4)
returning *;

-- name: GetCommentByID :one
select * from comments where id = $1 and deleted_at is null;

-- name: ListCommentsByPostID :many
-- Flat list, semua comment (top-level + reply) untuk satu post. Client
-- bina tree guna parent_comment_id.
select
  c.*,
  u.email as author_email,
  pr.member_id as author_member_id,
  pr.display_name as author_display_name
from comments c
join users u on u.id = c.author_id
join profiles pr on pr.user_id = c.author_id
where c.post_id = $1 and c.deleted_at is null
order by c.created_at asc;

-- name: UpdateComment :one
update comments
set content = $2, edited_at = now()
where id = $1 and deleted_at is null
returning *;

-- name: SoftDeleteComment :exec
update comments set deleted_at = now() where id = $1;

-- name: GetCommentAuthorID :one
select author_id from comments where id = $1 and deleted_at is null;

-- name: CountCommentsByPostIDs :many
select post_id, count(*) as comment_count
from comments
where post_id = any(sqlc.arg('post_ids')::uuid[]) and deleted_at is null
group by post_id;
