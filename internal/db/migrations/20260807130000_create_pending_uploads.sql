-- +goose Up
create table pending_uploads (
  r2_key text primary key,
  user_id uuid not null references users(id) on delete cascade,
  created_at timestamptz not null default now()
);

create index pending_uploads_user_id_idx on pending_uploads(user_id);

-- +goose Down
drop table if exists pending_uploads;
