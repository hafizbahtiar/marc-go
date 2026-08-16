-- +goose Up

-- Baris gilir untuk objek R2 yang perlu dipadam.
--
-- Kenapa gilir dan bukan padam terus dalam handler: padam R2 ialah
-- panggilan rangkaian yang boleh gagal. Kalau ia dibuat sebaris dalam
-- DELETE /posts, cuma ada dua pilihan buruk — gagalkan permintaan padam
-- pengguna sebab Cloudflare tak berapa sihat, atau telan ralat itu dan
-- bocorkan objek senyap-senyap (itulah keadaan sebelum ni). Dengan gilir,
-- padam post sentiasa berjaya serta-merta dan pembersihan dicuba semula
-- sampai jadi, termasuk merentas restart.
create table deleted_uploads (
  r2_key text primary key,

  -- 'post_deleted' | 'upload_abandoned' — untuk tahu dari mana sampah ni
  -- datang bila menyiasat penggunaan storan.
  reason text not null,

  attempts integer not null default 0,
  last_error text,

  -- Batu nisan, bukan padam baris. Baris ini KEKAL selepas objek berjaya
  -- dibuang supaya penyapu yatim di bawah tahu kunci itu dah diuruskan;
  -- kalau baris dibuang, setiap pusingan akan menggilir semula kunci yang
  -- sama selama-lamanya.
  deleted_at timestamptz,

  -- Backoff: dikemas kini setiap kali cubaan gagal supaya bucket yang
  -- bermasalah tak dihentam berulang kali.
  next_attempt_at timestamptz not null default now(),
  created_at timestamptz not null default now()
);

create index deleted_uploads_pending_idx
  on deleted_uploads (next_attempt_at) where deleted_at is null;

-- Pending upload yang tak pernah dilekatkan pada post ialah punca bocor
-- KEDUA, berasingan: pengguna pilih gambar (ia terus naik ke R2), kemudian
-- tinggalkan skrin karang. Objek itu kekal dalam bucket selamanya dan
-- tiada siapa pernah tahu. Index ni menyokong sapuan ikut umur.
create index pending_uploads_created_at_idx on pending_uploads (created_at);

-- +goose Down
drop index if exists pending_uploads_created_at_idx;
drop table if exists deleted_uploads;
