-- +goose Up

-- Historical references must survive account deletion. The user identity is
-- removed, but the imported batch/audit trail remains available.
alter table legacy_member_import_batches
  alter column created_by drop not null;

alter table profiles
  drop constraint if exists profiles_approved_by_fkey,
  add constraint profiles_approved_by_fkey
    foreign key (approved_by) references users(id) on delete set null,
  drop constraint if exists profiles_staff_id_verified_by_fkey,
  add constraint profiles_staff_id_verified_by_fkey
    foreign key (staff_id_verified_by) references users(id) on delete set null;

alter table legacy_member_import_batches
  drop constraint if exists legacy_member_import_batches_created_by_fkey,
  add constraint legacy_member_import_batches_created_by_fkey
    foreign key (created_by) references users(id) on delete set null;

alter table legacy_member_import_rows
  drop constraint if exists legacy_member_import_rows_user_id_fkey,
  add constraint legacy_member_import_rows_user_id_fkey
    foreign key (user_id) references users(id) on delete set null;

alter table donations
  drop constraint if exists donations_user_id_fkey,
  add constraint donations_user_id_fkey
    foreign key (user_id) references users(id) on delete set null;

-- +goose Down
alter table donations
  drop constraint if exists donations_user_id_fkey,
  add constraint donations_user_id_fkey
    foreign key (user_id) references users(id);

alter table legacy_member_import_rows
  drop constraint if exists legacy_member_import_rows_user_id_fkey,
  add constraint legacy_member_import_rows_user_id_fkey
    foreign key (user_id) references users(id);

alter table legacy_member_import_batches
  drop constraint if exists legacy_member_import_batches_created_by_fkey,
  add constraint legacy_member_import_batches_created_by_fkey
    foreign key (created_by) references users(id);

alter table profiles
  drop constraint if exists profiles_staff_id_verified_by_fkey,
  add constraint profiles_staff_id_verified_by_fkey
    foreign key (staff_id_verified_by) references users(id),
  drop constraint if exists profiles_approved_by_fkey,
  add constraint profiles_approved_by_fkey
    foreign key (approved_by) references users(id);

alter table legacy_member_import_batches
  alter column created_by set not null;
