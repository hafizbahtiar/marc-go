-- name: CreateRefreshToken :one
insert into refresh_tokens (user_id, token_hash, expires_at, family_id)
values ($1, $2, $3, $4)
returning *;

-- name: ConsumeRefreshToken :one
-- Atomic single-use: UPDATE...RETURNING dalam SATU statement, guard
-- "consumed_at is null" jamin cuma SATU concurrent request menang kalau
-- hash sama dihantar serentak (row-level lock Postgres). Row TAK
-- dipadam (beza dari sebelum ni) — kekal untuk reuse detection: kalau
-- hash yang SAMA cuba consume LAGI selepas ni, row dah wujud tapi
-- consumed_at dah bukan null, so 0 rows returned di sini -> caller
-- boleh GetRefreshTokenByHash untuk detect reuse & revoke family.
update refresh_tokens
set consumed_at = now()
where token_hash = $1 and consumed_at is null
returning *;

-- name: GetRefreshTokenByHash :one
select * from refresh_tokens where token_hash = $1;

-- name: RevokeRefreshTokenFamily :exec
delete from refresh_tokens where family_id = $1;

-- name: DeleteRefreshTokenByHash :exec
delete from refresh_tokens where token_hash = $1;

-- name: DeleteRefreshTokensByUser :exec
delete from refresh_tokens where user_id = $1;
