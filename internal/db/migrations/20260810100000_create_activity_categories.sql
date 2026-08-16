-- +goose Up
create table activity_categories (
  id uuid primary key default gen_random_uuid(),
  key text not null unique,
  name text not null,
  sort_order int not null default 0,
  is_active boolean not null default true,
  created_at timestamptz not null default now()
);

insert into activity_categories (key, name, sort_order) values
  ('badminton', 'Badminton', 10),
  ('futsal', 'Futsal', 20),
  ('bola_tampar', 'Bola Tampar', 30),
  ('larian', 'Larian', 40),
  ('ping_pong', 'Ping Pong', 50),
  ('lain_lain', 'Lain-lain', 900);

-- +goose Down
drop table if exists activity_categories;
