-- +goose Up

-- Alamat ahli — sehingga 3 setiap ahli (had disemak app-layer, bukan
-- constraint DB sebab "3" ialah peraturan produk yang boleh berubah, bukan
-- invariant struktur). Wajib SATU default dikuatkuasakan DB (partial
-- unique index di bawah), app-layer auto-promote bila default dipadam.
create table member_addresses (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  label text,                          -- "Rumah"/"Pejabat" dsb, opsyenal
  is_default boolean not null default false,
  address_type text not null check (address_type in ('landed','highrise')),
  unit_number text,                    -- no. rumah (landed) / no. unit (highrise)
  floor text,                          -- tingkat — highrise
  block text,                          -- blok — highrise
  street text,                         -- nama jalan
  township text,                       -- nama taman/perumahan
  city text not null,                  -- bandar
  postcode text not null,              -- poskod (5 digit)
  state text not null,                 -- negeri
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index member_addresses_user_id_idx on member_addresses(user_id);
-- Paling banyak SATU default setiap ahli — dikuatkuasakan DB, bukan cuma app.
create unique index member_addresses_one_default_per_user
  on member_addresses(user_id) where is_default;

-- +goose Down
drop table if exists member_addresses;
