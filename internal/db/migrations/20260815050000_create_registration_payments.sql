-- +goose Up
create table registration_payments (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  amount_cents integer not null check (amount_cents > 0),
  currency text not null default 'myr',
  gateway text not null check (gateway in ('toyyibpay')),
  gateway_ref text not null,
  status text not null default 'pending' check (status in ('pending', 'succeeded', 'failed')),
  created_at timestamptz not null default now()
);

-- gateway_ref (ToyyibPay billcode) unik setiap gateway — webhook update
-- row guna combo ni, elak race dua webhook retry terguna row lain (sama
-- pola `donations_gateway_gateway_ref_idx`).
create unique index registration_payments_gateway_gateway_ref_idx on registration_payments (gateway, gateway_ref);
create index registration_payments_user_id_idx on registration_payments (user_id);

-- +goose Down
drop table if exists registration_payments;
