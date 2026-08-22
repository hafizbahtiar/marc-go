-- name: CreateTelegramLinkToken :one
insert into telegram_link_tokens (user_id, token_hash, expires_at)
values ($1, $2, $3)
returning *;

-- name: ConsumeTelegramLinkToken :one
-- Tuntut token secara ATOMIK: satu pernyataan, `delete ... returning`.
-- Padanan tepat ConsumePasswordResetToken (queries/password_reset_tokens.sql)
-- dan atas sebab yang SAMA -- baca-dahulu-kemudian-tulis ada jurang
-- TOCTOU yang membenarkan dua permintaan serentak kedua-duanya lulus.
delete from telegram_link_tokens
where token_hash = $1
returning *;

-- name: DeleteTelegramLinkTokensByUser :exec
delete from telegram_link_tokens where user_id = $1;
