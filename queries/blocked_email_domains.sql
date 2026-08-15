-- name: IsEmailDomainBlocked :one
-- Semakan pendaftaran (/auth/register) — pelengkap kpd senarai statik
-- terbenam (internal/disposableemail), utk domain tambahan management.
select exists(select 1 from blocked_email_domains where domain = $1);

-- name: ListBlockedEmailDomains :many
-- Skrin pengurusan CRUD domain disekat.
select * from blocked_email_domains order by created_at desc;

-- name: AddBlockedEmailDomain :one
-- `on conflict do nothing` — idempoten, tambah domain yang dah wujud
-- bukan ralat (padanan pola ApproveProfile `status <> 'approved'`).
insert into blocked_email_domains (domain, added_by)
values ($1, $2)
on conflict (domain) do nothing
returning *;

-- name: RemoveBlockedEmailDomain :execrows
delete from blocked_email_domains where domain = $1;
