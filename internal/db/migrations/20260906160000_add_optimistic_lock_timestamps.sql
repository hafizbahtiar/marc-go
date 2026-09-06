-- +goose Up

alter table profiles
  add column updated_at timestamptz not null default now();

alter table posts
  add column updated_at timestamptz not null default now();

alter table comments
  add column updated_at timestamptz not null default now();

alter table activity_categories
  add column updated_at timestamptz not null default now();

create index profiles_updated_at_idx on profiles(updated_at);
create index posts_updated_at_idx on posts(updated_at);
create index comments_updated_at_idx on comments(updated_at);
create index activity_categories_updated_at_idx on activity_categories(updated_at);

-- +goose Down
drop index if exists activity_categories_updated_at_idx;
drop index if exists comments_updated_at_idx;
drop index if exists posts_updated_at_idx;
drop index if exists profiles_updated_at_idx;
alter table activity_categories drop column if exists updated_at;
alter table comments drop column if exists updated_at;
alter table posts drop column if exists updated_at;
alter table profiles drop column if exists updated_at;
