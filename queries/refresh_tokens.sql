-- name: CreateRefreshToken :one
-- user_agent/created_ip dirakam pada masa token dikeluarkan (issueTokens)
-- semata-mata untuk skrin "sesi aktif" - supaya ahli boleh kenal device
-- mana yang log masuk sebelum tekan "log keluar" padanya. Nilai mentah
-- disimpan; tiada parsing jadi "iPhone/Chrome" di sini.
insert into refresh_tokens (user_id, token_hash, expires_at, family_id, user_agent, created_ip)
values ($1, $2, $3, $4, $5, $6)
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

-- name: ListActiveRefreshTokensByUser :many
-- Sesi aktif milik pemanggil sendiri. `expires_at > now()` sahaja yang
-- ditapis: baris yang dah dirotate (consumed_at bukan null) tapi family
-- masih hidup sengaja TAK ditapis - ia masih mewakili device yang log
-- masuk, dan menapisnya akan buat device aktif hilang dari senarai
-- sebaik sahaja app refresh token.
select * from refresh_tokens
where user_id = $1 and expires_at > now()
order by created_at desc;

-- name: DeleteRefreshTokenByIDAndUser :execrows
-- Ownership dikuatkuasakan DALAM query (bukan semak dalam Go selepas
-- fetch) - padanan GetAddressByIDAndUser. `:execrows` supaya caller
-- boleh bezakan "dipadam" drpd "tiada baris" dan pulang 404 tanpa
-- membocorkan kewujudan id sesi milik orang lain.
delete from refresh_tokens where id = $1 and user_id = $2;
