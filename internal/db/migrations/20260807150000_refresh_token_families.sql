-- +goose Up
alter table refresh_tokens
  add column family_id uuid not null default gen_random_uuid(),
  add column consumed_at timestamptz;

create index refresh_tokens_family_id_idx on refresh_tokens(family_id);

-- +goose Down
drop index if exists refresh_tokens_family_id_idx;
alter table refresh_tokens
  drop column if exists consumed_at,
  drop column if exists family_id;
