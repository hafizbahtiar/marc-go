-- name: LikePost :execrows
insert into post_likes (post_id, user_id)
values ($1, $2)
on conflict (post_id, user_id) do nothing;

-- name: UnlikePost :exec
delete from post_likes where post_id = $1 and user_id = $2;

-- name: CountPostLikes :one
select count(*) from post_likes where post_id = $1;

-- name: PostLikedByUser :one
select exists(select 1 from post_likes where post_id = $1 and user_id = $2);

-- name: CountPostLikesByPostIDs :many
select post_id, count(*) as like_count
from post_likes
where post_id = any(sqlc.arg('post_ids')::uuid[])
group by post_id;

-- name: PostsLikedByUser :many
-- Untuk tandakan "liked_by_me" bila list post — pulang subset post_ids
-- yang user ni dah like.
select post_id from post_likes
where user_id = $1 and post_id = any(sqlc.arg('post_ids')::uuid[]);

-- name: LikeComment :execrows
-- `:execrows`, bukan `:exec` (L35, 2026-08-22). Handler perlu tahu sama
-- ada baris BENAR-BENAR masuk sebelum memberitahu penulis komen —
-- `on conflict do nothing` bermakna like berulang ialah no-op, dan
-- memberitahu tanpa syarat menjadikan endpoint ni gelung spam push
-- bersasar. Corak SAMA yang L18 tegakkan pada `LikePost`; ia dibawa ke
-- sini SERENTAK dengan notifikasi ditambah, bukan selepasnya.
insert into comment_likes (comment_id, user_id)
values ($1, $2)
on conflict (comment_id, user_id) do nothing;

-- name: UnlikeComment :exec
delete from comment_likes where comment_id = $1 and user_id = $2;

-- name: CountCommentLikesByCommentIDs :many
select comment_id, count(*) as like_count
from comment_likes
where comment_id = any(sqlc.arg('comment_ids')::uuid[])
group by comment_id;

-- name: CommentsLikedByUser :many
select comment_id from comment_likes
where user_id = $1 and comment_id = any(sqlc.arg('comment_ids')::uuid[]);
