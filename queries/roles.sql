-- name: GetRoleByKey :one
select * from roles where key = $1;

-- name: GetRoleByID :one
select * from roles where id = $1;

-- name: ListRoles :many
select * from roles order by rank;
