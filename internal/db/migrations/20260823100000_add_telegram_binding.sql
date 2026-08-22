-- +goose Up

-- Integrasi Telegram Fasa 1 (binding akaun). Lajur pada `profiles`
-- (BUKAN `users`) -- `users` cuma pegang kelayakan (id/email/
-- password_hash); atribut akaun spt `email_verified`/`avatar_r2_key`
-- sedia ada duduk pada `profiles`. Binding ni keadaan kekal-tunggal,
-- padanan profil yg sama.
alter table profiles
  add column telegram_chat_id bigint unique,
  add column telegram_username text,
  add column telegram_linked_at timestamptz;

-- Token deep-link sementara, sekali-guna -- cerminan
-- password_reset_tokens, TTL lebih pendek (10 minit, bukan 1 jam)
-- sebab aliran ni app->Telegram serta-merta, bukan tunggu emel.
create table telegram_link_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index telegram_link_tokens_user_id_idx on telegram_link_tokens(user_id);

-- +goose Down
drop table if exists telegram_link_tokens;
alter table profiles
  drop column if exists telegram_chat_id,
  drop column if exists telegram_username,
  drop column if exists telegram_linked_at;
