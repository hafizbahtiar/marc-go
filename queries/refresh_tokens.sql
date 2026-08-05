-- name: CreateRefreshToken :one
insert into refresh_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: ConsumeRefreshToken :one
-- Atomic: DELETE...RETURNING dalam SATU statement, supaya refresh token
-- betul-betul single-use. Kalau dua request serentak hantar hash yang
-- sama (race), Postgres punya row-level lock jamin cuma SATU dapat row
-- balik (menang); yang satu lagi dapat 0 rows -> pgx.ErrNoRows -> 401.
-- Guna GetRefreshTokenByHash + DeleteRefreshToken berasingan sebelum ni
-- ada TOCTOU gap yang boleh buat DUA-DUA request refresh berjaya
-- serentak guna token yang sama.
delete from refresh_tokens where token_hash = $1 returning *;

-- name: DeleteRefreshTokenByHash :exec
delete from refresh_tokens where token_hash = $1;
