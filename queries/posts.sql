-- name: CreatePost :one
insert into posts (author_id, type, content)
values ($1, $2, $3)
returning *;

-- name: GetPostByID :one
select
  p.*,
  u.email as author_email,
  pr.member_id as author_member_id,
  pr.display_name as author_display_name,
  pr.avatar_r2_key as author_avatar_r2_key
from posts p
join users u on u.id = p.author_id
join profiles pr on pr.user_id = p.author_id
where p.id = $1 and p.deleted_at is null;

-- name: ListPosts :many
-- Keyset pagination atas (created_at, id) — bukan created_at je, elak
-- row terlepas kalau ada tie timestamp betul-betul kat sempadan page
-- (null cursor = page pertama).
select
  p.*,
  u.email as author_email,
  pr.member_id as author_member_id,
  pr.display_name as author_display_name,
  pr.avatar_r2_key as author_avatar_r2_key
from posts p
join users u on u.id = p.author_id
join profiles pr on pr.user_id = p.author_id
where p.deleted_at is null
  and (
    sqlc.narg('cursor_created_at')::timestamptz is null
    or (p.created_at, p.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
order by p.created_at desc, p.id desc
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

-- name: ListPostImageKeys :many
select r2_key from post_images where post_id = $1;

-- name: ListOrphanedPostImageKeys :many
-- Gambar milik post yang DAH dipadam tapi belum pernah digilir untuk
-- dibuang. Menangkap dua perkara: post yang dipadam SEBELUM gilir
-- pembersihan wujud, dan mana-mana kunci yang terlepas sejak itu.
select pi.r2_key
from post_images pi
join posts p on p.id = pi.post_id
where p.deleted_at is not null
  and not exists (select 1 from deleted_uploads d where d.r2_key = pi.r2_key)
limit $1;
