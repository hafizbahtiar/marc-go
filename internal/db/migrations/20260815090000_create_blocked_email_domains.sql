-- +goose Up

-- Pelengkap kepada senarai statik terbenam (internal/disposableemail) —
-- BUKAN gantinya. Utk management tambah domain BAHARU yang senarai
-- statik terlepas (atau domain organisasi spesifik) TANPA perlu deploy
-- kod. Lihat internal/disposableemail/disposableemail.go untuk rasional
-- penuh keputusan produk 2026-08-15.
create table blocked_email_domains (
  domain text primary key,
  added_by uuid references users(id) on delete set null,
  created_at timestamptz not null default now()
);

-- +goose Down
drop table if exists blocked_email_domains;
