-- name: NextSequence :one
insert into sequences (key, current_value, updated_at)
values ($1, 1, now())
on conflict (key) do update set
  current_value = sequences.current_value + 1,
  updated_at = now()
returning current_value;
