-- +goose Up
alter table refresh_tokens
  add column consumed_ip text;

-- +goose Down
alter table refresh_tokens
  drop column if exists consumed_ip;
