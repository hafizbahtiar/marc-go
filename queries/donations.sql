-- name: CreateDonation :one
insert into donations (user_id, donor_name, donor_email, amount_cents, currency, gateway, gateway_ref, status)
values ($1, $2, $3, $4, $5, $6, $7, 'pending')
returning *;

-- name: GetDonationByGatewayRef :one
select * from donations where gateway = $1 and gateway_ref = $2;

-- name: ListPendingDonationsOlderThan :many
-- Baris 'pending' yang dah cukup umur untuk layak disemak semula terus
-- pada gateway (internal/paymentreconcile) — padanan alasan
-- ListPendingRegistrationPaymentsOlderThan (registration_payments.sql):
-- cuma 'pending', bukan 'failed' (terminal, tak perlu disemak semula).
select * from donations
where status = 'pending' and created_at < $1
order by created_at;

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
