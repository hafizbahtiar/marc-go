-- name: CreateRefreshToken :one
insert into refresh_tokens (user_id, token_hash, expires_at, family_id)
values ($1, $2, $3, $4)
returning *;

-- name: ConsumeRefreshToken :one
-- Atomic single-use: UPDATE...RETURNING dalam SATU statement, guard
-- "consumed_at is null" jamin cuma SATU concurrent request menang kalau
-- hash sama dihantar serentak (row-level lock Postgres). Row TAK
-- dipadam (beza dari sebelum ni) - kekal untuk reuse detection: kalau
-- hash yang SAMA cuba consume LAGI selepas ni, row dah wujud tapi
-- consumed_at dah bukan null, so 0 rows returned di sini -> caller
-- boleh GetRefreshTokenByHash untuk detect reuse & revoke family.
--
-- consumed_ip direkod supaya reuse-grace-window (auth.go) boleh semak IP
-- request yang menang consume sama dengan IP request reuse - elak
-- attacker yang curi token dari IP lain lolos grace window sekadar
-- dengan race timing terhadap request pemilik sah.
update refresh_tokens
set consumed_at = now(), consumed_ip = $2
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
