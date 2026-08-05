-- name: CreateEmailVerificationToken :one
insert into email_verification_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: GetEmailVerificationTokenByHash :one
select * from email_verification_tokens where token_hash = $1;

-- name: DeleteEmailVerificationToken :exec
delete from email_verification_tokens where id = $1;

-- name: DeleteEmailVerificationTokensByUser :exec
delete from email_verification_tokens where user_id = $1;
