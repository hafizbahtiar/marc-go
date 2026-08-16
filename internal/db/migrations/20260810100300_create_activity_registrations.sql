-- +goose Up
create table activity_registrations (
  id uuid primary key default gen_random_uuid(),
  activity_id uuid not null references activities(id) on delete cascade,
  user_id uuid not null references users(id) on delete cascade,
  status text not null default 'registered'
    check (status in ('pending_payment', 'registered', 'cancelled')),

  -- Cangkuk fasa payment. Sengaja wujud dari awal supaya integrasi gateway
  -- kemudian tak perlukan migration atas jadual yang dah ada data.
  payment_status text not null default 'not_required'
    check (payment_status in ('not_required', 'pending', 'paid', 'refunded')),
  payment_ref text,

  -- Isi QR ahli. Legap dan rawak: sesiapa yang nampak nilai ni boleh
  -- ditandakan hadir, jadi ia tak boleh diteka dari id/user_id.
  checkin_token text not null unique,

  registered_at timestamptz not null default now(),
  cancelled_at timestamptz
);

-- Unik SEPARA: halang pendaftaran berganda, tapi benarkan daftar semula
-- selepas batal (baris 'cancelled' kekal sebagai sejarah).
create unique index activity_registrations_active_uniq
  on activity_registrations(activity_id, user_id)
  where status <> 'cancelled';

create index activity_registrations_activity_status_idx
  on activity_registrations(activity_id, status);
create index activity_registrations_user_id_idx
  on activity_registrations(user_id);

-- +goose Down
drop table if exists activity_registrations;
