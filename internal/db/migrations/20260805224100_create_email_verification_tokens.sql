-- +goose Up
create table email_verification_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index email_verification_tokens_user_id_idx on email_verification_tokens(user_id);

-- +goose Down
drop table if exists email_verification_tokens;
