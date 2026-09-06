-- +goose Up

create table legacy_member_import_batches (
  id uuid primary key default gen_random_uuid(),
  source_filename text not null,
  source_sha256 text not null,
  created_by uuid not null references users(id),
  status text not null default 'dry_run'
    check (status in ('dry_run', 'ready', 'imported', 'failed')),
  total_rows integer not null default 0,
  valid_rows integer not null default 0,
  conflict_rows integer not null default 0,
  created_at timestamptz not null default now()
);

create index legacy_member_import_batches_created_idx
  on legacy_member_import_batches (created_at desc);

create table legacy_member_import_rows (
  id uuid primary key default gen_random_uuid(),
  batch_id uuid not null references legacy_member_import_batches(id) on delete cascade,
  source_row integer not null,
  legacy_number text not null default '',
  status text not null default 'conflict'
    check (status in ('valid', 'conflict', 'imported', 'claimed')),
  legacy_staff_id text not null default '',
  member_id text not null default '',
  display_name text not null default '',
  email text not null default '',
  phone text not null default '',
  department_code text not null default '',
  position text not null default '',
  emergency_name text not null default '',
  emergency_phone text not null default '',
  health_notes text not null default '',
  address text not null default '',
  category text not null default '',
  club_position text not null default '',
  legacy_status text not null default '',
  conflicts jsonb not null default '[]'::jsonb,
  warnings jsonb not null default '[]'::jsonb,
  user_id uuid references users(id),
  created_at timestamptz not null default now(),
  unique (batch_id, source_row)
);

create index legacy_member_import_rows_batch_idx
  on legacy_member_import_rows (batch_id, source_row);
create index legacy_member_import_rows_email_idx
  on legacy_member_import_rows (lower(email));
create index legacy_member_import_rows_staff_id_idx
  on legacy_member_import_rows (legacy_staff_id);

create table legacy_member_claim_tokens (
  id uuid primary key default gen_random_uuid(),
  row_id uuid not null unique references legacy_member_import_rows(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  consumed_at timestamptz,
  created_at timestamptz not null default now()
);

create index legacy_member_claim_tokens_expiry_idx
  on legacy_member_claim_tokens (expires_at)
  where consumed_at is null;

-- +goose Down
drop table if exists legacy_member_claim_tokens;
drop table if exists legacy_member_import_rows;
drop table if exists legacy_member_import_batches;
