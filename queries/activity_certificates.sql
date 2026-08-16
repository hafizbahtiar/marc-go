-- name: ListEligibleForCertificate :many
-- Server mengira sendiri siapa layak — management tidak menyenaraikan.
-- Klausa payment_status kekal walaupun payment belum diintegrasikan:
-- fee_cents sentiasa 0 buat masa ini, jadi ia sentiasa benar.
-- display_name boleh null, tapi recipient_name pada sijil not null —
-- jatuh balik ke member_id supaya ahli tanpa nama paparan tetap dapat
-- nama yang boleh dicetak.
select r.id as registration_id, r.user_id,
  coalesce(pr.display_name, pr.member_id) as display_name,
  (select count(*) from activity_attendances at where at.registration_id = r.id) as attended
from activity_registrations r
join profiles pr on pr.user_id = r.user_id
join activities a on a.id = r.activity_id
where r.activity_id = $1
  and r.status = 'registered'
  and (a.fee_cents = 0 or r.payment_status = 'paid')
order by coalesce(pr.display_name, pr.member_id);

-- name: CreateCertificate :one
insert into activity_certificates (
  activity_id, user_id, serial, verify_token,
  recipient_name, activity_title, activity_date
) values ($1, $2, $3, $4, $5, $6, $7)
on conflict (activity_id, user_id) do nothing
returning *;

-- name: SetCertificateR2Key :exec
update activity_certificates set r2_key = $2 where id = $1;

-- name: ListCertificatesPendingFile :many
-- Fasa 2 penerbitan menyambung dari sini. Baris tanpa r2_key ialah kerja
-- yang belum siap, bukan ralat.
select * from activity_certificates
where activity_id = $1 and r2_key is null and revoked_at is null;

-- name: ListCertificatesByActivity :many
select * from activity_certificates where activity_id = $1 order by serial;

-- name: ListMyCertificates :many
select ac.*, c.name as category_name
from activity_certificates ac
join activities a on a.id = ac.activity_id
join activity_categories c on c.id = a.category_id
where ac.user_id = $1 and ac.revoked_at is null
order by ac.issued_at desc;

-- name: GetCertificateByID :one
select * from activity_certificates where id = $1;

-- name: GetCertificateByVerifyToken :one
select * from activity_certificates where verify_token = $1;

-- name: RevokeCertificate :one
update activity_certificates
set revoked_at = now(), revoked_reason = $2
where id = $1 and revoked_at is null
returning *;
