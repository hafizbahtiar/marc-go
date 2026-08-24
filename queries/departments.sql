-- name: ListDepartments :many
-- Skrin pengurusan CRUD bahagian/jabatan - susunan organisasi (bukan abjad).
select * from departments order by sort_order asc, name asc;

-- name: AddDepartment :one
insert into departments (code, name, sort_order, added_by)
values ($1, $2, $3, $4)
returning *;

-- name: UpdateDepartment :one
update departments
set
  name = coalesce(sqlc.narg('name')::text, name),
  sort_order = coalesce(sqlc.narg('sort_order')::int, sort_order)
where code = sqlc.arg('code')
returning *;

-- name: RemoveDepartment :execrows
delete from departments where code = $1;

-- name: DepartmentExists :one
select exists(select 1 from departments where code = $1);
