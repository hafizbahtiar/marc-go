-- +goose Up
-- Metadata device untuk skrin "sesi aktif" (self-service). Nullable
-- sebab baris sedia ada dicipta sebelum migration ni tak ada nilainya -
-- UI tunjuk sebagai tidak diketahui, bukan gagal.
alter table refresh_tokens
  add column user_agent text,
  add column created_ip text;

-- +goose Down
alter table refresh_tokens
  drop column if exists created_ip,
  drop column if exists user_agent;
