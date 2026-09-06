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
  pr.display_name as author_display_name,
  pr.avatar_r2_key as author_avatar_r2_key
from comments c
join users u on u.id = c.author_id
join profiles pr on pr.user_id = c.author_id
where c.post_id = $1 and c.deleted_at is null
order by c.created_at asc;

-- name: UpdateComment :one
update comments
set content = $2, edited_at = now(), updated_at = now()
where id = $1 and deleted_at is null
  and updated_at = $3
returning *;

-- name: SoftDeleteComment :execrows
update comments set deleted_at = now(), updated_at = now()
where id = $1 and deleted_at is null and updated_at = $2;

-- name: GetCommentAuthorID :one
select author_id from comments where id = $1 and deleted_at is null;

-- name: CountCommentsByPostIDs :many
select post_id, count(*) as comment_count
from comments
where post_id = any(sqlc.arg('post_ids')::uuid[]) and deleted_at is null
group by post_id;

-- name: ListCommentPreviewsByPostIDs :many
-- Tiga komen terbaharu setiap post, top-level sahaja. Feed gunakan ini
-- sebagai preview; laluan detail masih memuatkan thread penuh.
select *
from (
  select
    c.post_id,
    c.id,
    c.parent_comment_id,
    c.content,
    c.created_at,
    c.updated_at,
    c.edited_at,
    c.author_id,
    pr.member_id as author_member_id,
    pr.display_name as author_display_name,
    pr.avatar_r2_key as author_avatar_r2_key,
    row_number() over (partition by c.post_id order by c.created_at desc) as preview_rank
  from comments c
  join profiles pr on pr.user_id = c.author_id
  where c.post_id = any(sqlc.arg('post_ids')::uuid[])
    and c.parent_comment_id is null
    and c.deleted_at is null
) previews
where preview_rank <= 3
order by post_id, created_at asc;
