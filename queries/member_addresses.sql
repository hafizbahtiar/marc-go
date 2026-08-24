-- name: ListAddressesByUser :many
select * from member_addresses
where user_id = $1
order by is_default desc, created_at asc;

-- name: GetAddressByIDAndUser :one
-- Ownership dikuatkuasakan DALAM query (bukan cuma filter selepas fetch)
-- - padanan corak `authz` package: query yang tak filter guna user id
-- dari token bermakna ownership tak dikuatkuasakan.
select * from member_addresses
where id = $1 and user_id = $2;

-- name: CountAddressesByUser :one
-- Had 3 alamat/ahli disemak app-layer (bukan constraint DB, "3" ialah
-- peraturan produk boleh berubah) - dipanggil dalam transaksi yang sama
-- sebelum INSERT, padanan cara sequences/nombor ahli dikira.
select count(*) from member_addresses where user_id = $1;

-- name: CreateAddress :one
insert into member_addresses (
  user_id, label, is_default, address_type, unit_number, floor, block,
  street, township, city, postcode, state
) values (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
returning *;

-- name: UpdateAddress :one
-- Partial update - medan tak dihantar (narg NULL) kekal nilai asal,
-- padanan pola UpdateProfile. `is_default` sengaja TIDAK di sini -
-- ditetapkan berasingan (SetDefault) dalam transaksi yang turut
-- nyahtetapkan default lama, supaya invariant "paling banyak SATU
-- default" sentiasa dikekalkan sepanjang transaksi.
update member_addresses
set
  label = coalesce(sqlc.narg('label')::text, label),
  address_type = coalesce(sqlc.narg('address_type')::text, address_type),
  unit_number = coalesce(sqlc.narg('unit_number')::text, unit_number),
  floor = coalesce(sqlc.narg('floor')::text, floor),
  block = coalesce(sqlc.narg('block')::text, block),
  street = coalesce(sqlc.narg('street')::text, street),
  township = coalesce(sqlc.narg('township')::text, township),
  city = coalesce(sqlc.narg('city')::text, city),
  postcode = coalesce(sqlc.narg('postcode')::text, postcode),
  state = coalesce(sqlc.narg('state')::text, state),
  updated_at = now()
where id = sqlc.arg('id') and user_id = sqlc.arg('user_id')
returning *;

-- name: UnsetDefaultForUser :exec
-- Nyahtetapkan default LAMA sebelum tetapkan default BAHARU, dalam
-- transaksi yang sama - partial unique index (satu default/ahli) akan
-- tolak dua baris `is_default=true` serentak kalau susunan ni songsang.
update member_addresses set is_default = false, updated_at = now()
where user_id = $1 and is_default;

-- name: SetDefault :one
update member_addresses set is_default = true, updated_at = now()
where id = $1 and user_id = $2
returning *;

-- name: DeleteAddress :exec
delete from member_addresses where id = $1 and user_id = $2;

-- name: GetOldestOtherByUser :one
-- Auto-promote lepas default dipadam - baris PALING LAMA (created_at)
-- selain baris yang baru dipadam jadi default baharu.
select * from member_addresses
where user_id = $1 and id <> $2
order by created_at asc
limit 1;
