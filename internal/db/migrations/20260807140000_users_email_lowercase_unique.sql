-- +goose Up
create unique index users_email_lower_idx on users (lower(email));

-- +goose Down
drop index if exists users_email_lower_idx;
