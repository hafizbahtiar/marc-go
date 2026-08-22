-- name: CreatePendingUpload :exec
insert into pending_uploads (r2_key, user_id) values ($1, $2);

-- name: DeletePendingUpload :exec
delete from pending_uploads where r2_key = $1 and user_id = $2;

-- name: IsPendingUploadOwnedByUser :one
select exists(select 1 from pending_uploads where r2_key = $1 and user_id = $2);

-- name: EnqueueDeletedUpload :exec
-- on conflict do nothing: padam post yang sama dua kali (atau retry) tak
-- patut gagal, dan objek tu memang dah dalam gilir.
insert into deleted_uploads (r2_key, reason)
values ($1, $2)
on conflict (r2_key) do nothing;

-- name: ListDueDeletedUploads :many
select * from deleted_uploads
where deleted_at is null and next_attempt_at <= now()
order by next_attempt_at
limit $1;

-- name: MarkDeletedUploadDone :exec
-- Tandakan, jangan padam baris — lihat komen 'deleted_at' dlm migration.
update deleted_uploads set deleted_at = now() where r2_key = $1;

-- name: MarkDeletedUploadFailed :exec
-- Backoff eksponen ringkas, dihadkan pada 1 jam.
update deleted_uploads
set attempts = attempts + 1,
    last_error = $2,
    next_attempt_at = now() + least(power(2, attempts)::int, 60) * interval '1 minute'
where r2_key = $1;

-- name: ListStalePendingUploads :many
-- Pending upload yang tak pernah dilekatkan pada mana-mana post ATAU profil.
--
-- Dua klausa `not exists` ni BUKAN pendua kepada laluan Go (Opus verify
-- 2026-08-22, L28). Baris yang dipulangkan di sini ialah senarai PADAM:
-- semuanya akan digilir ke `deleted_uploads` dan objek R2nya dibuang.
-- Sebelum ni query cuma menapis ikut UMUR dan bergantung SEPENUHNYA pada
-- baris dikeluarkan semasa post dicipta — sedangkan laluan itu
-- (`posts.go`) mengabaikan ralat `DeletePendingUpload`, jadi satu DELETE
-- yang gagal bermakna gambar post yang MASIH dipaparkan dipadam 6 jam
-- kemudian, kekal, tanpa ralat di mana-mana.
--
-- Laluan Go kini menyemak ralat itu juga, tapi kedua-dua lapisan
-- dikekalkan dengan sengaja: kos melanggar invarian ni ialah kehilangan
-- data yang tak boleh dipulihkan, dan semakan di SINI turut melindungi
-- mana-mana laluan tulis MASA HADAPAN yang terlupa mengeluarkan barisnya.
--
-- Laluan avatar (`applyAvatar`) sentiasa menyemak ralatnya, jadi klausa
-- `profiles` lebih kepada simetri drpd pembaikan pepijat — tapi tanpa ia,
-- query ni betul atas sebab yang bergantung pada fail LAIN, dan itulah
-- tepatnya bentuk kelemahan yang L28 wujud untuk hapuskan.
select pu.* from pending_uploads pu
where pu.created_at < $1
  and not exists (
    select 1 from post_images pi where pi.r2_key = pu.r2_key
  )
  and not exists (
    select 1 from profiles pr where pr.avatar_r2_key = pu.r2_key
  )
order by pu.created_at
limit $2;

-- name: DeletePendingUploadByKey :exec
-- Tanpa skop user — untuk penyapu latar, bukan permintaan pengguna.
delete from pending_uploads where r2_key = $1;

-- name: DeleteDoneDeletedUploadsBefore :execrows
-- Prune batu nisan lama. Selamat sebab objek R2 sendiri dah tiada; baris
-- ni cuma menghalang penggiliran semula, dan objek yang dah dipadam
-- takkan muncul semula dalam post_images/pending_uploads.
delete from deleted_uploads
where deleted_at is not null and deleted_at < $1;
