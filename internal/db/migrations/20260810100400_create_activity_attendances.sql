-- +goose Up
create table activity_attendances (
  id uuid primary key default gen_random_uuid(),
  registration_id uuid not null references activity_registrations(id) on delete cascade,
  session_id uuid not null references activity_sessions(id) on delete cascade,

  -- Keempat-empat kaedah check-in hasilkan baris yang SAMA; hanya method
  -- dan marked_by berbeza. 'self_scan' dan 'code' belum ada UI — schema
  -- sokong supaya menambahnya nanti kerja UI, bukan migration.
  method text not null check (method in ('manual', 'scan', 'self_scan', 'code')),

  marked_by uuid references users(id) on delete set null,
  checked_in_at timestamptz not null default now(),
  unique (registration_id, session_id)
);

create index activity_attendances_session_id_idx on activity_attendances(session_id);

-- +goose Down
drop table if exists activity_attendances;
