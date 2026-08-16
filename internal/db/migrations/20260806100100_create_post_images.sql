-- +goose Up
create table post_images (
  id uuid primary key default gen_random_uuid(),
  post_id uuid not null references posts(id) on delete cascade,
  r2_key text not null,
  position smallint not null default 0
);

create index post_images_post_id_idx on post_images(post_id);

-- +goose Down
drop table if exists post_images;
