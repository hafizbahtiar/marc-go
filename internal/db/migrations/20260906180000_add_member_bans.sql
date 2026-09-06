-- +goose Up
alter table profiles
  add column banned_at timestamptz,
  add column ban_expires_at timestamptz,
  add column ban_reason text,
  add column banned_by uuid references users(id);

create index profiles_active_ban_idx
  on profiles(ban_expires_at)
  where banned_at is not null;

-- +goose Down
drop index if exists profiles_active_ban_idx;
alter table profiles
  drop column if exists banned_by,
  drop column if exists ban_reason,
  drop column if exists ban_expires_at,
  drop column if exists banned_at;
