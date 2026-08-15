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

-- name: HasSucceededRegistrationPayment :one
select exists(
  select 1 from registration_payments where user_id = $1 and status = 'succeeded'
);
