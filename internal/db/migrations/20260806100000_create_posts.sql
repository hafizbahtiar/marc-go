-- +goose Up
create table posts (
  id uuid primary key default gen_random_uuid(),
  author_id uuid not null references users(id) on delete cascade,
  type text not null default 'normal' check (type in ('normal', 'announcement')),
  content text not null,
  created_at timestamptz not null default now(),
  edited_at timestamptz,
  deleted_at timestamptz
);

create index posts_created_at_idx on posts(created_at desc);
create index posts_author_id_idx on posts(author_id);

-- +goose Down
drop table if exists posts;
