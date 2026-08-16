-- +goose Up
update users set email = lower(email) where email <> lower(email);
create unique index users_email_lower_idx on users (lower(email));

-- +goose Down
drop index if exists users_email_lower_idx;
