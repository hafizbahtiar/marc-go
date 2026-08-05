-- +goose Up
create table profiles (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null unique references users(id) on delete cascade,
  member_id text not null unique,
  display_name text,
  phone text,
  role_id smallint not null references roles(id),
  email_verified boolean not null default false,
  created_at timestamptz not null default now()
);

create index profiles_role_id_idx on profiles(role_id);

-- +goose Down
drop table if exists profiles;
