-- name: CreatePasswordResetToken :one
insert into password_reset_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: GetPasswordResetTokenByHash :one
select * from password_reset_tokens where token_hash = $1;

-- name: DeletePasswordResetTokensByUser :exec
-- Dipanggil DUA tempat, atas sebab berbeza:
--   request — permintaan baharu membunuh pautan lama
--   confirm — sekali-guna, dalam transaksi yang sama dgn tukar kata laluan
delete from password_reset_tokens where user_id = $1;
