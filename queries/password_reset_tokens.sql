-- name: CreatePasswordResetToken :one
insert into password_reset_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: GetPasswordResetTokenByHash :one
-- UJIAN SAHAJA — tiada pemanggil produksi, dan jangan tambah satu. Kod
-- produksi MESTI guna ConsumePasswordResetToken: membaca token
-- dgn SELECT lalu memadamnya kemudian ialah tepat jurang TOCTOU yang
-- Consume wujud untuk menutup.
select * from password_reset_tokens where token_hash = $1;

-- name: ConsumePasswordResetToken :one
-- Tuntut token secara ATOMIK: satu pernyataan, `delete ... returning`.
--
-- Padanan `ConsumeRefreshToken` (queries/refresh_tokens.sql) dan atas
-- sebab yang SAMA: baca-dahulu-kemudian-tulis ada jurang TOCTOU — dua
-- permintaan serentak dgn hash yang sama kedua-duanya lulus bacaan lalu
-- kedua-duanya menulis. Dengan `delete ... returning`, kunci baris
-- Postgres menjamin hanya SATU dapat baris; yang lain dapat 0 baris.
delete from password_reset_tokens
where token_hash = $1
returning *;

-- name: DeletePasswordResetTokensByUser :exec
-- Dipanggil DUA tempat, atas sebab berbeza:
--   request — permintaan baharu membunuh pautan lama
--   confirm — sekali-guna, dalam transaksi yang sama dgn tukar kata laluan
delete from password_reset_tokens where user_id = $1;
