-- name: CreateRegistrationPayment :one
-- SENGAJA tanpa `gateway_ref` (L29, 2026-08-22). Baris ditulis SEBELUM
-- bil gateway dicipta, jadi ref belum wujud pada titik ni — ia diisi
-- oleh `SetRegistrationPaymentGatewayRef` sebaik createBill pulang.
--
-- Susunan ni yang menjadikan bil yatim mustahil: kalau proses mati
-- antara INSERT dan createBill, yang tinggal ialah baris 'pending' tanpa
-- ref — kelihatan, boleh diaudit, dan TIADA bil untuk dibayar. Susunan
-- lama (createBill dahulu) meninggalkan yang sebaliknya: bil yang boleh
-- dibayar tanpa baris, yang webhook mahupun reconcile tak dapat lihat.
insert into registration_payments (user_id, amount_cents, currency, gateway, status)
values ($1, $2, $3, $4, 'pending')
returning *;

-- name: SetRegistrationPaymentGatewayRef :one
-- Isi `gateway_ref` sebaik createBill berjaya. Dikunci pada `id` (bukan
-- ref) sebab ref itulah yang belum wujud.
--
-- Guard `gateway_ref is null` menjadikannya sekali-tulis: sebaik bil
-- dikaitkan, tiada laluan boleh menunjuknya kepada bil LAIN. Tanpa
-- guard, pepijat di tempat lain boleh menulis ganti ref bagi bayaran
-- yang sudah berjaya dan mengalihkan rekod kewangan kepada bil orang
-- lain.
update registration_payments
set gateway_ref = $2
where id = $1 and gateway_ref is null
returning *;

-- name: MarkRegistrationPaymentFailed :exec
-- Dipanggil bila createBill GAGAL selepas baris dicipta. Baris dikekalkan
-- (bukan dipadam) supaya sejarah "Bayaran Saya" ahli menunjukkan
-- percubaan itu benar-benar berlaku — dan `ListPendingRegistrationPayments
-- OlderThan` tak perlu menapis baris yang takkan pernah ada bil.
--
-- Guard `gateway_ref is null` memastikan ni tak boleh menjatuhkan bayaran
-- yang bilnya SUDAH dicipta.
update registration_payments
set status = 'failed'
where id = $1 and gateway_ref is null;

-- name: UpdateRegistrationPaymentStatusByGatewayRef :one
-- `status <> 'succeeded'` = 'succeeded' ialah keadaan TERMINAL: webhook
-- retry/replay (atau event lewat sampai tak ikut turutan) tak boleh
-- turunkan bayaran yang dah berjaya jadi 'failed'. Percubaan gagal yang
-- dicuba semula pada gateway_ref yang sama tetap boleh naik
-- 'failed' -> 'succeeded' (sebab itu bukan `status = 'pending'`).
update registration_payments
set status = $3
where gateway = $1 and gateway_ref = $2 and status <> 'succeeded'
returning *;

-- name: ListPendingRegistrationPaymentsOlderThan :many
-- Baris 'pending' yang dah cukup umur untuk layak disemak semula terus
-- pada gateway (internal/paymentreconcile) — bukan `status <> 'succeeded'`
-- macam query UPDATE di atas, sengaja `status = 'pending'` sahaja: baris
-- 'failed' TAK perlu disemak semula (terminal jugak, sama macam
-- 'succeeded', reconcile tak sepatutnya "hidupkan semula" bayaran gagal
-- tanpa ahli cuba lagi secara eksplisit — bayaran baharu akan hasilkan
-- baris baharu).
--
-- TINGKAP ATAS + LIMIT (L30, 2026-08-22). Sebelum ni query ni ada had
-- umur BAWAH sahaja, dan baris yang ditinggalkan TAK PERNAH keluar
-- daripada 'pending': bil ToyyibPay yang tak dibayar pulang
-- `No data found!` selama-lamanya, jadi `CheckStatus` pulang "pending"
-- selama-lamanya. Setiap checkout terbiar kekal dalam senarai semakan
-- SELAMANYA, dan setiap 30 minit ia satu panggilan HTTP keluar lagi —
-- bebanan yang membesar secara monotonik sepanjang hayat sistem.
--
-- `stale_before` = had bawah (cukup umur untuk layak disemak).
-- `oldest` = had atas: lebih tua drpd ni bukan lagi kerja rekonsiliasi,
-- ia kerja pembersihan. Baris begitu TIDAK hilang — ia kekal dalam DB
-- dan tetap kelihatan melalui /admin/payments; ia cuma berhenti dipoll.
-- `gateway_ref is not null` (L29, 2026-08-22): baris tanpa ref bermakna
-- createBill tak pernah berjaya, jadi tiada bil untuk ditanya pada
-- gateway. Padanan skop `ListPendingActivityRegistrationsOlderThan`,
-- yang sudah lama menapis dgn cara sama atas sebab yang sama.
select * from registration_payments
where status = 'pending'
  and gateway_ref is not null
  and created_at < sqlc.arg('stale_before')
  and created_at > sqlc.arg('oldest')
order by created_at
limit sqlc.arg('row_limit');

-- name: GetMyRegistrationPaymentByID :one
-- Resit — hanya baris SENDIRI (user_id caller), sertakan medan papar
-- (no. ahli/nama/emel) supaya handler resit tak perlu query kedua.
select rp.id, rp.amount_cents, rp.currency, rp.gateway, rp.gateway_ref, rp.status, rp.created_at,
  p.member_id, p.display_name, u.email
from registration_payments rp
join profiles p on p.user_id = rp.user_id
join users u on u.id = rp.user_id
where rp.id = $1 and rp.user_id = $2;

-- name: ListMyRegistrationPayments :many
-- Sejarah PENUH percubaan yuran pendaftaran seorang ahli (bukan cuma
-- status terkini macam GetLatestRegistrationPaymentStatus) — utk skrin
-- "Sejarah Bayaran Saya".
select * from registration_payments
where user_id = $1
order by created_at desc;

-- name: HasSucceededRegistrationPayment :one
select exists(
  select 1 from registration_payments where user_id = $1 and status = 'succeeded'
);

-- name: GetLatestRegistrationPaymentStatus :one
-- Untuk `/me` — Flutter perlukan ni supaya ahli nampak bayaran mereka
-- berjaya/gagal/menunggu, bukan senyap (gap ditemui 2026-08-15: bayaran
-- gagal/berjaya dua-dua direkod betul dalam DB tapi client tak pernah
-- baca, jadi ahli nampak "tiada apa berlaku" tak kira hasil sebenar).
-- `pgx.ErrNoRows` bermakna ahli tak pernah cuba bayar langsung — caller
-- (Go) layan tu sebagai null, bukan ralat.
--
-- Utamakan 'succeeded' dulu (Opus verify 2026-08-15): `Checkout` cuma
-- sekat bayaran BERULANG bila dah ada baris 'succeeded' — kalau ahli
-- tekan Bayar dua kali (baris A, lepas tu B) dan bayar bil A dulu,
-- 'order by created_at desc' semata-mata akan pulang B ('pending', baris
-- LEBIH BAHARU) walhal A dah 'succeeded' — ahli nampak "sedang disahkan"
-- selama-lamanya walau dah bayar. `(status = 'succeeded') desc` letak
-- baris succeeded MANA-MANA PUN di atas dulu; `created_at desc` cuma
-- pemisah antara baris tak-succeeded (paparkan percubaan TERKINI).
select status from registration_payments
where user_id = $1
order by (status = 'succeeded') desc, created_at desc
limit 1;
