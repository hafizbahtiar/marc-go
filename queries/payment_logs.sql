-- name: CreatePaymentLog :exec
insert into payment_logs (
  module, event, status, gateway, gateway_ref, amount_cents,
  user_id, related_id, message, raw_payload
) values (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
);

-- name: ListPaymentLogsByGatewayRef :many
-- Sejarah penuh satu bayaran (semua peristiwa: checkout, webhook,
-- reconcile) — untuk diagnosis satu insiden, bukan tinjauan am.
select * from payment_logs
where gateway = $1 and gateway_ref = $2
order by created_at asc;

-- name: ListRecentPaymentLogs :many
-- Tinjauan am terkini merentas modul — endpoint admin.
select * from payment_logs
where (sqlc.narg('module')::text is null or module = sqlc.narg('module'))
order by id desc
limit $1;

-- name: DeletePaymentLogsOlderThan :execrows
-- Retention 3 bulan (keputusan produk 2026-08-15) — dipanggil
-- internal/retention, padanan pola sapuan audit_logs sedia ada.
delete from payment_logs where created_at < $1;
