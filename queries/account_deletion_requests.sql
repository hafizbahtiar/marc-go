-- name: CreateAccountDeletionRequest :one
-- `on conflict do nothing` — idempoten, ahli boleh tekan "padam akaun"
-- berkali-kali tanpa ralat (padanan pola AddBlockedEmailDomain). Baris
-- SEDIA ADA (bukan yang baru dicuba) yang perlu dipulangkan pada
-- konflik — lihat GetAccountDeletionRequestByUserID di handler.
insert into account_deletion_requests (user_id)
values ($1)
on conflict (user_id) do nothing
returning *;

-- name: GetAccountDeletionRequestByUserID :one
-- Guna oleh CreateAccountDeletionRequest bila insert kena `on conflict do
-- nothing` (tiada baris dipulangkan), dan utk pelaporan/staff semak status
-- kemudian.
select * from account_deletion_requests where user_id = $1;
