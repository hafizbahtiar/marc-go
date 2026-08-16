-- +goose Up
create table comments (
  id uuid primary key default gen_random_uuid(),
  post_id uuid not null references posts(id) on delete cascade,
  parent_comment_id uuid references comments(id) on delete cascade,
  author_id uuid not null references users(id) on delete cascade,
  content text not null,
  created_at timestamptz not null default now(),
  edited_at timestamptz,
  deleted_at timestamptz
);

create index comments_post_id_idx on comments(post_id);
create index comments_parent_comment_id_idx on comments(parent_comment_id);

-- +goose Down
drop table if exists comments;
