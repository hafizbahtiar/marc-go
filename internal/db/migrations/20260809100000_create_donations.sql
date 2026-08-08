-- +goose Up
create table donations (
  id uuid primary key default gen_random_uuid(),
  user_id uuid references users(id),
  donor_name text,
  donor_email text,
  amount_cents integer not null check (amount_cents > 0),
  currency text not null default 'myr',
  gateway text not null check (gateway in ('stripe', 'sociabuzz')),
  gateway_ref text not null,
  status text not null default 'pending' check (status in ('pending', 'succeeded', 'failed')),
  created_at timestamptz not null default now(),
  -- Jejak wajib: kalau bukan ahli log masuk (user_id null), donor_email
  -- MESTI diisi — keputusan produk (2026-08-09) supaya SEMUA donation ada
  -- jejak dalaman, walau anonymous drpd app MARC.
  constraint donations_traceable check (user_id is not null or donor_email is not null)
);

-- gateway_ref (contoh Stripe PaymentIntent id) unik setiap gateway —
-- webhook update row guna combo ni, elak race dua webhook retry
-- terguna row lain.
create unique index donations_gateway_gateway_ref_idx on donations (gateway, gateway_ref);
create index donations_user_id_idx on donations (user_id) where user_id is not null;

-- +goose Down
drop table if exists donations;
