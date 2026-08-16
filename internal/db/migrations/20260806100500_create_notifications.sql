-- +goose Up
create table notifications (
  id uuid primary key default gen_random_uuid(),
  recipient_id uuid not null references users(id) on delete cascade,
  actor_id uuid not null references users(id) on delete cascade,
  type text not null check (type in ('post_like', 'post_comment')),
  post_id uuid not null references posts(id) on delete cascade,
  comment_id uuid references comments(id) on delete cascade,
  read_at timestamptz,
  created_at timestamptz not null default now()
);

create index notifications_recipient_id_created_at_idx on notifications(recipient_id, created_at desc);

-- +goose Down
drop table if exists notifications;
