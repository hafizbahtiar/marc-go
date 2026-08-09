-- name: CreatePendingUpload :exec
insert into pending_uploads (r2_key, user_id) values ($1, $2);

-- name: DeletePendingUpload :exec
delete from pending_uploads where r2_key = $1 and user_id = $2;

-- name: IsPendingUploadOwnedByUser :one
select exists(select 1 from pending_uploads where r2_key = $1 and user_id = $2);

-- name: EnqueueDeletedUpload :exec
-- on conflict do nothing: padam post yang sama dua kali (atau retry) tak
-- patut gagal, dan objek tu memang dah dalam gilir.
insert into deleted_uploads (r2_key, reason)
values ($1, $2)
on conflict (r2_key) do nothing;

-- name: ListDueDeletedUploads :many
select * from deleted_uploads
where deleted_at is null and next_attempt_at <= now()
order by next_attempt_at
limit $1;

-- name: MarkDeletedUploadDone :exec
-- Tandakan, jangan padam baris — lihat komen 'deleted_at' dlm migration.
update deleted_uploads set deleted_at = now() where r2_key = $1;

-- name: MarkDeletedUploadFailed :exec
-- Backoff eksponen ringkas, dihadkan pada 1 jam.
update deleted_uploads
set attempts = attempts + 1,
    last_error = $2,
    next_attempt_at = now() + least(power(2, attempts)::int, 60) * interval '1 minute'
where r2_key = $1;

-- name: ListStalePendingUploads :many
-- Pending upload yang tak pernah dilekatkan pada mana-mana post.
select * from pending_uploads
where created_at < $1
order by created_at
limit $2;

-- name: DeletePendingUploadByKey :exec
-- Tanpa skop user — untuk penyapu latar, bukan permintaan pengguna.
delete from pending_uploads where r2_key = $1;

-- name: DeleteDoneDeletedUploadsBefore :execrows
-- Prune batu nisan lama. Selamat sebab objek R2 sendiri dah tiada; baris
-- ni cuma menghalang penggiliran semula, dan objek yang dah dipadam
-- takkan muncul semula dalam post_images/pending_uploads.
delete from deleted_uploads
where deleted_at is not null and deleted_at < $1;
