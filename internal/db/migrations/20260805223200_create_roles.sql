-- +goose Up
create table roles (
  id smallint generated always as identity primary key,
  key text not null unique,
  name text not null,
  category text not null check (category in ('management', 'ahli')),
  rank integer not null default 0,
  created_at timestamptz not null default now()
);

-- +goose Down
drop table if exists roles;
