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

-- name: InsertEmailVerificationSend :exec
insert into email_verification_sends (user_id) values ($1);

-- name: GetLatestEmailVerificationSendAt :one
select created_at from email_verification_sends
where user_id = $1
order by created_at desc
limit 1;

-- name: CountEmailVerificationSendsSince :one
select count(*)::int from email_verification_sends
where user_id = $1 and created_at > $2;
