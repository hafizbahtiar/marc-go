-- name: CreatePendingUpload :exec
insert into pending_uploads (r2_key, user_id) values ($1, $2);

-- name: DeletePendingUpload :exec
delete from pending_uploads where r2_key = $1 and user_id = $2;

-- name: IsPendingUploadOwnedByUser :one
select exists(select 1 from pending_uploads where r2_key = $1 and user_id = $2);
