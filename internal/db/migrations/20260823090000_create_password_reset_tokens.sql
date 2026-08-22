-- +goose Up

-- Reset kata laluan (L32). Cerminan `email_verification_tokens` — sama
-- bentuk, sama kitaran hayat, sengaja jadual BERASINGAN.
--
-- Kenapa bukan guna semula jadual pengesahan emel dgn lajur `purpose`:
-- ia menggabungkan dua kitaran hayat berbeza dan memerlukan migration
-- atas jadual yang sedang berfungsi — membeli kekemasan skema dengan
-- risiko pada laluan yang tiada kaitan.
--
-- Kenapa bukan token bertandatangan tanpa keadaan (JWT): token reset
-- MESTI sekali-guna dan MESTI boleh dibatalkan sebelum luput. Token
-- tanpa keadaan tak boleh jadi kedua-duanya.
create table password_reset_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  -- SHA-256 bagi token legap 32 bait. Token MENTAH hanya wujud dalam
  -- emel — kalau DB bocor, hash tak boleh mereset apa-apa.
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index password_reset_tokens_user_id_idx on password_reset_tokens(user_id);

-- +goose Down
drop table if exists password_reset_tokens;
