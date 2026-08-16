-- +goose Up
alter table notifications alter column post_id drop not null;
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected'));

-- +goose Down
-- NOTE: this down migration will fail if any member_* notifications
-- exist by the time it runs (post_id null violates the restored NOT
-- NULL, and the old type check rejects member_* rows) — expected for a
-- dev rollback, not a concern for forward deploys.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment'));
alter table notifications alter column post_id set not null;
