-- +goose Up
create table activity_certificates (
  id uuid primary key default gen_random_uuid(),

  -- restrict, bukan cascade: aktiviti yang dah keluarkan sijil tak boleh
  -- dipadam begitu sahaja.
  activity_id uuid not null references activities(id) on delete restrict,
  user_id uuid not null references users(id) on delete cascade,

  serial text not null unique,

  -- Berasingan daripada serial. Serial berjujukan — kalau ia juga kunci
  -- pengesahan awam, sesiapa boleh tambah satu dan menuai nama semua ahli.
  verify_token text not null unique,

  -- Snapshot: PDF tak berubah selepas dijana, jadi halaman pengesahan
  -- mesti menunjukkan apa yang TERCETAK, bukan profil semasa.
  recipient_name text not null,
  activity_title text not null,
  activity_date date not null,

  issued_at timestamptz not null default now(),
  r2_key text,
  revoked_at timestamptz,
  revoked_reason text,
  unique (activity_id, user_id)
);

create index activity_certificates_user_id_idx on activity_certificates(user_id);

-- +goose Down
drop table if exists activity_certificates;
