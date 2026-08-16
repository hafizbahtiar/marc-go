-- name: MarkAttendance :one
-- on conflict do nothing + returning: imbas kedua QR yang sama bukan ralat,
-- ia cuma tiada kerja. Handler membezakan "baharu" daripada "sudah ada"
-- melalui sama ada baris dipulangkan.
insert into activity_attendances (registration_id, session_id, method, marked_by)
values ($1, $2, $3, $4)
on conflict (registration_id, session_id) do nothing
returning *;

-- name: GetAttendance :one
select * from activity_attendances
where registration_id = $1 and session_id = $2;

-- name: DeleteAttendance :execrows
delete from activity_attendances
where registration_id = $1 and session_id = $2;

-- name: ListAttendanceByActivity :many
select at.* from activity_attendances at
join activity_sessions s on s.id = at.session_id
where s.activity_id = $1;

-- name: CountAttendanceByRegistration :one
select count(*) from activity_attendances where registration_id = $1;
