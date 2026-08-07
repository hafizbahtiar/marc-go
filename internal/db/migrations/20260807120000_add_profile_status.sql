-- +goose Up
alter table profiles
  add column status text not null default 'pending'
    check (status in ('pending', 'approved', 'rejected')),
  add column approved_by uuid references users(id),
  add column approved_at timestamptz;

-- Backfill: everyone who registered BEFORE this migration is already a
-- known/active member — only rows created AFTER this point get the new
-- 'pending' default. approved_by/approved_at stay null for these (no
-- real approver to attribute retroactively).
update profiles set status = 'approved';

create index profiles_status_idx on profiles(status);

-- +goose Down
drop index if exists profiles_status_idx;
alter table profiles
  drop column if exists approved_at,
  drop column if exists approved_by,
  drop column if exists status;
