-- +goose Up
-- Jejak setiap permintaan hantar emel pengesahan (bukan token itu
-- sendiri). `email_verification_tokens` dipadam setiap resend, jadi
-- tak boleh dipakai untuk kira had harian / jeda 60s.
create table email_verification_sends (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  created_at timestamptz not null default now()
);

create index email_verification_sends_user_created_idx
  on email_verification_sends (user_id, created_at desc);

-- +goose Down
drop table if exists email_verification_sends;
