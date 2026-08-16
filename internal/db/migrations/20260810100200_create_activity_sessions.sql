-- +goose Up
create table activity_sessions (
  id uuid primary key default gen_random_uuid(),
  activity_id uuid not null references activities(id) on delete cascade,
  seq int not null,
  title text not null default '',
  starts_at timestamptz not null,
  ends_at timestamptz not null check (ends_at > starts_at),
  unique (activity_id, seq)
);

create index activity_sessions_activity_id_starts_at_idx
  on activity_sessions(activity_id, starts_at);

-- +goose Down
drop table if exists activity_sessions;
