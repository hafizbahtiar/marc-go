-- +goose Up

-- Google Play Console mewajibkan app yang sokong penciptaan akaun sediakan
-- cara ahli MEMINTA pemadaman akaun + data berkaitan. Ni v1 sengaja
-- REQUEST-sahaja: rekod permintaan + jejak audit, staff tindak secara
-- manual (akses DB terus) buat masa ni. TIADA cascading/auto-purge post,
-- bayaran, pendaftaran aktiviti dsb — reka bentuk & uji pemadaman merentas
-- jadual yang selamat perlukan lebih masa drpd yang ada sekarang.
create table account_deletion_requests (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null unique references users(id) on delete cascade,
  requested_at timestamptz not null default now(),
  status text not null default 'pending' check (status in ('pending', 'completed')),
  completed_at timestamptz
);

-- +goose Down
drop table if exists account_deletion_requests;
