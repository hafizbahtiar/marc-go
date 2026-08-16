-- +goose Up
create table activities (
  id uuid primary key default gen_random_uuid(),
  category_id uuid not null references activity_categories(id) on delete restrict,
  title text not null,
  description text not null default '',
  location_name text not null,
  location_address text not null default '',

  -- starts_at/ends_at ialah min/maks activity_sessions yang DIDENORMALISASI.
  -- Sesi ialah sumber kebenaran; dua lajur ni dikira semula dalam transaksi
  -- yang sama setiap kali set sesi berubah (lihat ReplaceActivitySessions).
  -- Sebab: senarai aktiviti perlu isih+tapis ikut tarikh dengan indeks, dan
  -- min() atas join pada setiap senarai terlalu mahal.
  starts_at timestamptz not null,
  ends_at timestamptz not null,

  registration_opens_at timestamptz,
  registration_closes_at timestamptz not null,
  capacity int check (capacity > 0),
  fee_cents int not null default 0 check (fee_cents >= 0),
  currency text not null default 'MYR',
  attendance_threshold_pct smallint not null default 100
    check (attendance_threshold_pct between 1 and 100),
  status text not null default 'draft'
    check (status in ('draft', 'published', 'cancelled', 'completed')),
  cancelled_reason text,
  certificates_issued_at timestamptz,
  created_by uuid references users(id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create index activities_status_starts_at_idx on activities(status, starts_at desc);
create index activities_category_id_idx on activities(category_id);

-- +goose Down
drop table if exists activities;
