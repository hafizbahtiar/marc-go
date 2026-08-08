-- name: CreateDonation :one
insert into donations (user_id, donor_name, donor_email, amount_cents, currency, gateway, gateway_ref, status)
values ($1, $2, $3, $4, $5, $6, $7, 'pending')
returning *;

-- name: GetDonationByGatewayRef :one
select * from donations where gateway = $1 and gateway_ref = $2;

-- name: UpdateDonationStatusByGatewayRef :one
update donations
set status = $3
where gateway = $1 and gateway_ref = $2
returning *;
