-- name: CreateRegistrationPayment :one
insert into registration_payments (user_id, amount_cents, currency, gateway, gateway_ref, status)
values ($1, $2, $3, $4, $5, 'pending')
returning *;

-- name: UpdateRegistrationPaymentStatusByGatewayRef :one
-- `status <> 'succeeded'` = 'succeeded' ialah keadaan TERMINAL: webhook
-- retry/replay (atau event lewat sampai tak ikut turutan) tak boleh
-- turunkan bayaran yang dah berjaya jadi 'failed'. Percubaan gagal yang
-- dicuba semula pada gateway_ref yang sama tetap boleh naik
-- 'failed' -> 'succeeded' (sebab itu bukan `status = 'pending'`).
update registration_payments
set status = $3
where gateway = $1 and gateway_ref = $2 and status <> 'succeeded'
returning *;

-- name: ListPendingRegistrationPaymentsOlderThan :many
-- Baris 'pending' yang dah cukup umur untuk layak disemak semula terus
-- pada gateway (internal/paymentreconcile) — bukan `status <> 'succeeded'`
-- macam query UPDATE di atas, sengaja `status = 'pending'` sahaja: baris
-- 'failed' TAK perlu disemak semula (terminal jugak, sama macam
-- 'succeeded', reconcile tak sepatutnya "hidupkan semula" bayaran gagal
-- tanpa ahli cuba lagi secara eksplisit — bayaran baharu akan hasilkan
-- baris baharu).
select * from registration_payments
where status = 'pending' and created_at < $1
order by created_at;

-- name: HasSucceededRegistrationPayment :one
select exists(
  select 1 from registration_payments where user_id = $1 and status = 'succeeded'
);

-- name: GetLatestRegistrationPaymentStatus :one
-- Untuk `/me` — Flutter perlukan ni supaya ahli nampak bayaran mereka
-- berjaya/gagal/menunggu, bukan senyap (gap ditemui 2026-08-15: bayaran
-- gagal/berjaya dua-dua direkod betul dalam DB tapi client tak pernah
-- baca, jadi ahli nampak "tiada apa berlaku" tak kira hasil sebenar).
-- `pgx.ErrNoRows` bermakna ahli tak pernah cuba bayar langsung — caller
-- (Go) layan tu sebagai null, bukan ralat.
--
-- Utamakan 'succeeded' dulu (Opus verify 2026-08-15): `Checkout` cuma
-- sekat bayaran BERULANG bila dah ada baris 'succeeded' — kalau ahli
-- tekan Bayar dua kali (baris A, lepas tu B) dan bayar bil A dulu,
-- 'order by created_at desc' semata-mata akan pulang B ('pending', baris
-- LEBIH BAHARU) walhal A dah 'succeeded' — ahli nampak "sedang disahkan"
-- selama-lamanya walau dah bayar. `(status = 'succeeded') desc` letak
-- baris succeeded MANA-MANA PUN di atas dulu; `created_at desc` cuma
-- pemisah antara baris tak-succeeded (paparkan percubaan TERKINI).
select status from registration_payments
where user_id = $1
order by (status = 'succeeded') desc, created_at desc
limit 1;
