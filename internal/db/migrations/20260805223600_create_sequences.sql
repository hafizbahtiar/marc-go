-- +goose Up
create table sequences (
  key text primary key,
  current_value bigint not null default 0,
  updated_at timestamptz not null default now()
);

-- +goose Down
drop table if exists sequences;
