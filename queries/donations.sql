-- name: CreateDonation :one
insert into donations (user_id, donor_name, donor_email, amount_cents, currency, gateway, gateway_ref, status)
values ($1, $2, $3, $4, $5, $6, $7, 'pending')
returning *;

-- name: GetDonationByGatewayRef :one
select * from donations where gateway = $1 and gateway_ref = $2;

-- name: ListMyDonations :many
-- Sejarah derma seorang ahli, untuk `GET /me/payments` (L33, 2026-08-22).
--
-- Tanpa query ni, `GET /me/payments/donation/:id/receipt` mati secara
-- praktikal: ia perlukan `donations.id`, dan tiada permukaan API yang
-- pernah mendedahkan id itu kepada pemiliknya. Endpoint resit wujud sejak
-- awal; yang hilang cuma cara menemuinya.
--
-- Diskop `user_id`, jadi derma TANPA NAMA (user_id null) tak pernah
-- muncul — betul: penderma itu tiada akaun untuk menuntut baris ni, dan
-- emel resit yang dihantar semasa webhook ialah satu-satunya jejak mereka
-- ada, mengikut reka bentuk (lihat komen `GetMyDonationByID`).
--
-- Status 'pending'/'failed' TURUT dipulangkan (bukan 'succeeded' sahaja),
-- padanan `ListMyRegistrationPayments`: sejarah patut menunjukkan
-- percubaan yang gagal, bukan senyap menghilangkannya. Butang resit
-- digate pada status di sisi klien.
select * from donations
where user_id = $1
order by created_at desc;

-- name: GetMyDonationByID :one
-- Resit — hanya donation SENDIRI (ahli log masuk, user_id = caller).
-- Donation anonymous (user_id null) TIADA laluan muat turun resit sini —
-- emel resit yang dihantar semasa webhook satu-satunya jejak mereka ada,
-- tiada akaun untuk log masuk dan tuntut baris ni.
select * from donations where id = $1 and user_id = $2;

-- name: ListPendingDonationsOlderThan :many
-- Baris 'pending' yang dah cukup umur untuk layak disemak semula terus
-- pada gateway (internal/paymentreconcile) — padanan alasan
-- ListPendingRegistrationPaymentsOlderThan (registration_payments.sql):
-- cuma 'pending', bukan 'failed' (terminal, tak perlu disemak semula).
--
-- Tingkap atas + limit — lihat komen penuh pada
-- `ListPendingRegistrationPaymentsOlderThan` (L30). Sebab sama terpakai:
-- PaymentIntent Stripe yang ditinggalkan kekal `requires_payment_method`,
-- yang `CheckStatus` petakan kepada "pending" selama-lamanya.
select * from donations
where status = 'pending'
  and created_at < sqlc.arg('stale_before')
  and created_at > sqlc.arg('oldest')
order by created_at
limit sqlc.arg('row_limit');

-- name: UpdateDonationStatusByGatewayRef :one
-- `status <> 'succeeded'` = 'succeeded' ialah keadaan TERMINAL: webhook
-- retry/replay (atau event lewat sampai tak ikut turutan) tak boleh
-- turunkan donation yang dah berjaya jadi 'failed'. Kad yang ditolak
-- kemudian dicuba semula atas PaymentIntent yang sama tetap boleh naik
-- 'failed' -> 'succeeded' (sebab itu bukan `status = 'pending'`).
update donations
set status = $3
where gateway = $1 and gateway_ref = $2 and status <> 'succeeded'
returning *;
