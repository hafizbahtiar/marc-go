-- +goose Up
-- bootstrap: extension untuk gen_random_uuid() dipakai oleh table-table lain
create extension if not exists pgcrypto;

-- +goose Down
drop extension if exists pgcrypto;
