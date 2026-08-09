-- name: CreateAuditLog :exec
insert into audit_logs (
  entity_type, entity_id, action,
  actor_id, actor_member_id, actor_role_key,
  changed_fields, old_values, new_values,
  ip_address, user_agent
)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ListAuditLogsByEntity :many
-- Timeline satu entiti (cth semua suntingan pada satu post).
select * from audit_logs
where entity_type = $1 and entity_id = $2
order by id desc
limit $3;

-- name: ListAuditLogs :many
-- Feed audit global dengan tapisan pilihan. Pagination keyset guna
-- `before_id` (bukan OFFSET) — stabil walaupun baris baharu masuk
-- semasa pengguna membelek.
select * from audit_logs
where (sqlc.narg('entity_type')::text is null or entity_type = sqlc.narg('entity_type')::text)
  and (sqlc.narg('action')::text is null or action = sqlc.narg('action')::text)
  and (sqlc.narg('actor_id')::uuid is null or actor_id = sqlc.narg('actor_id')::uuid)
  and (sqlc.narg('before_id')::bigint is null or id < sqlc.narg('before_id')::bigint)
order by id desc
limit sqlc.arg('row_limit');

-- name: DeleteAuditLogsBefore :execrows
-- Pruning polisi simpanan. Belum dipanggil dari mana-mana — sengaja,
-- supaya polisi diputuskan dulu sebelum data dibuang.
delete from audit_logs where created_at < $1;
