-- +goose Up

-- staff_id: mandatory opaque staff/employee number, no format
-- validation, verified by management (rank >= manager) before
-- member_id issuance. See docs/superpowers/specs/2026-09-02-staff-id-verification-design.md
alter table profiles
  add column staff_id text,
  add column staff_id_verified_at timestamptz,
  add column staff_id_verified_by uuid references users(id);

-- member_id is now issued only on staff-id verification, not at
-- registration time - defer NOT NULL until that happens.
alter table profiles
  alter column member_id drop not null;

-- Only 'approved' rows are auto-verified. 'pending'/'rejected' rows
-- get a placeholder staff_id but stay UNVERIFIED - they never
-- went through real verification and some may still owe the fee.
-- Auto-verifying them would silently exempt them from payment AND
-- make them one click from approval with no real check ever done.
update profiles
set staff_id = user_id::text,
    staff_id_verified_at = case when status = 'approved' then now() else null end
where staff_id is null;

alter table profiles
  alter column staff_id set not null;

create unique index profiles_staff_id_key on profiles (staff_id);

-- +goose Down

-- Down does NOT attempt to restore member_id's NOT NULL - it will
-- fail once any unverified (member_id IS NULL) row exists. No safe
-- automatic reverse for that part; leaving member_id nullable on
-- rollback is intentional, not an oversight.
drop index if exists profiles_staff_id_key;

alter table profiles
  drop column if exists staff_id,
  drop column if exists staff_id_verified_at,
  drop column if exists staff_id_verified_by;
