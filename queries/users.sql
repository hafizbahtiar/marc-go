-- name: CreateUser :one
insert into users (email, password_hash)
values ($1, $2)
returning *;

-- name: GetUserByEmail :one
select * from users where email = $1;

-- name: GetUserByID :one
select * from users where id = $1;

-- name: UpdateUserPassword :exec
update users set password_hash = $2 where id = $1;
