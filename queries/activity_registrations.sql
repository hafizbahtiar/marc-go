-- name: LockActivityForRegistration :one
-- `for update` atas baris aktiviti — ini yang menyerikan pendaftaran
-- serentak supaya kiraan kapasiti tak boleh basi antara baca dan tulis.
select * from activities where id = $1 and deleted_at is null for update;

-- name: CountActiveRegistrations :one
select count(*) from activity_registrations
where activity_id = $1 and status <> 'cancelled';

-- name: CreateRegistration :one
insert into activity_registrations (activity_id, user_id, status, payment_status, checkin_token)
values ($1, $2, $3, $4, $5)
returning *;

-- name: CancelRegistration :one
update activity_registrations
set status = 'cancelled', cancelled_at = now()
where activity_id = $1 and user_id = $2 and status <> 'cancelled'
returning *;

-- name: GetRegistrationByActivityAndUser :one
select * from activity_registrations
where activity_id = $1 and user_id = $2 and status <> 'cancelled';

-- name: GetRegistrationByCheckinToken :one
select * from activity_registrations
where checkin_token = $1 and status <> 'cancelled';

-- name: GetRegistrationByID :one
select * from activity_registrations where id = $1;

-- name: ListRegistrationsByActivity :many
-- attended_session_ids menjawab "sesi mana pendaftaran ini sudah hadir?" —
-- skrin kehadiran pengurusan menyemai suisnya daripada medan ini. Satu
-- boolean per peserta tidak mencukupi: kehadiran ialah per-sesi.
--
-- Agregat dikira SEKALI dalam subkueri berkumpulan, bukan satu kueri per
-- pendaftaran; senarai ini dibaca dengan seluruh peserta sekali gus, jadi
-- N+1 di sini bermakna satu perjalanan DB bagi setiap ahli.
--
-- coalesce(..., '{}') penting: left join memberi NULL untuk pendaftaran
-- tanpa kehadiran, dan NULL bersiri sebagai `null` dalam JSON. Klien yang
-- memanggil .map atasnya terhempas — [] ialah kontrak.
select r.*, pr.member_id, pr.display_name, pr.avatar_r2_key,
  coalesce(att.session_ids, '{}')::uuid[] as attended_session_ids
from activity_registrations r
join profiles pr on pr.user_id = r.user_id
left join (
  select at.registration_id, array_agg(at.session_id order by s.seq) as session_ids
  from activity_attendances at
  join activity_sessions s on s.id = at.session_id
  where s.activity_id = $1
  group by at.registration_id
) att on att.registration_id = r.id
where r.activity_id = $1 and r.status <> 'cancelled'
order by pr.display_name;

-- name: SetRegistrationPaymentRef :one
-- Simpan bill code ToyyibPay pada pendaftaran sedia ada, dipanggil sebaik
-- createBill berjaya semasa checkout yuran aktiviti. `fee_cents_paid`
-- snapshot amaun SEBENAR dihantar ke gateway pada saat checkout ni —
-- resit (payments.go) baca lajur ni dan bukan `activities.fee_cents`
-- hidup, supaya yuran yang ditukar SELEPAS bayar tak senyap ubah resit
-- yang sedia wujud (Opus verify 2026-08-15).
update activity_registrations
set payment_ref = $2, fee_cents_paid = $3
where id = $1
returning *;

-- name: UpdateRegistrationPaymentStatusByPaymentRef :one
-- Padanan UpdateRegistrationPaymentStatusByGatewayRef (registration_payments)
-- tapi bagi activity_registrations: kunci carian payment_ref (bill code),
-- bukan (gateway, gateway_ref) berasingan sebab jadual ni tak simpan lajur
-- gateway berasingan (satu gateway sahaja buat masa ini, ToyyibPay).
-- Kekang `payment_status <> 'paid'` idempotent — 'paid' ialah keadaan
-- terminal, replay webhook lepas tu ialah no-op.
--
-- SENGAJA TIADA `and status <> 'cancelled'`: kalau baris ni dah dibatal
-- (CancelStaleUnpaidBills) tapi webhook confirm lambat tiba, UPDATE ni
-- MASIH akan tanda payment_status='paid' walaupun status='cancelled' —
-- keadaan cancelled+paid yang ganjil, tapi SENGAJA supaya boleh dikesan
-- (handler Go log ERROR bila ini berlaku, lihat activity_registration_payment.go)
-- bukan senyap hilang. Kalau guard `status<>'cancelled'` ditambah di sini,
-- UPDATE gagal (0 baris), pgx.ErrNoRows dianggap "replay biasa", dan
-- kesnya jadi kelihatan macam tiada apa berlaku — walhal ahli dah bayar.
update activity_registrations
set payment_status = $2
where payment_ref = $1 and payment_status <> 'paid'
returning *;

-- name: ListPendingActivityRegistrationsOlderThan :many
-- Baris 'pending' YANG DAH cuba checkout (payment_ref wujud) dan dah
-- cukup umur untuk layak disemak semula terus pada gateway
-- (internal/paymentreconcile) — padanan `CancelStaleUnpaidBills` dari
-- segi skop (payment_ref is not null), tapi cutoff jauh lebih pendek
-- (reconcile MEMBETULKAN state, bukan membatalkan secara musnah, jadi
-- semak awal selamat — lihat internal/paymentreconcile untuk alasan
-- penuh). Baris `payment_ref is null` (tak pernah cuba checkout) dilangkau
-- — tiada apa nak disemak pada gateway untuk baris begitu.
select * from activity_registrations
where payment_status = 'pending' and payment_ref is not null and registered_at < $1
order by registered_at;

-- name: CancelStaleUnstartedPayments :many
-- Batal pendaftaran berbayar yang ahli TAK PERNAH cuba checkout
-- (payment_ref masih NULL — tiada bil ToyyibPay pernah dicipta) selepas
-- cutoff PENDEK. Selamat dibatal cepat: tiada webhook akan datang untuk
-- baris ni sebab tiada bil wujud langsung.
--
-- `and status <> 'cancelled'` — tanpa ni, baris yang DAH dibatal pusingan
-- sebelum kena UPDATE semula setiap 15 minit selama-lamanya, tulis ganti
-- `cancelled_at` (rosakkan jejak audit "bila SEBENAR ia dibatal") dan
-- kembungkan bilangan baris dilaporkan log tanpa sebab.
update activity_registrations
set status = 'cancelled', cancelled_at = now()
where payment_status = 'pending' and status <> 'cancelled'
  and payment_ref is null and registered_at < $1
returning *;

-- name: CancelStaleUnpaidBills :many
-- Batal pendaftaran yang DAH cuba checkout (payment_ref wujud, bil
-- ToyyibPay sebenar dicipta) selepas cutoff PANJANG — sengaja lain drpd
-- CancelStaleUnstartedPayments.
--
-- Kenapa cutoff PANJANG di sini: bil ToyyibPay yang dah dicipta boleh
-- disahkan bila-bila masa oleh webhook (FPX/bank kadang ambil lebih 45
-- minit — pembayar boleh tinggalkan app lama sebelum sambung semula ke
-- laman bank). Kalau baris ni dibatal pada cutoff PENDEK yang sama
-- macam "tak pernah cuba", webhook yang tiba LEPAS itu (UPDATE ...
-- WHERE payment_ref = $1 AND payment_status <> 'paid', TIADA
-- `status <> 'cancelled'` guard dengan sengaja — lihat komen
-- UpdateRegistrationPaymentStatusByPaymentRef) akan tetap tanda
-- payment_status='paid' atas baris yang `status='cancelled'` — ahli
-- dah BAYAR tapi slotnya HILANG, tiada jejak melainkan seseorang cari
-- baris cancelled+paid secara manual. Cutoff panjang kurangkan
-- kebarangkalian tetingkap lumba ni secara drastik (bukan hapuskan
-- 100% — itu perlukan reka bentuk lebih kompleks, dianggap tak
-- berbaloi buat masa ini: risiko kapasiti terikat lebih lama jauh
-- lebih kecil drpd risiko kehilangan bayaran ahli).
update activity_registrations
set status = 'cancelled', cancelled_at = now()
where payment_status = 'pending' and status <> 'cancelled'
  and payment_ref is not null and registered_at < $1
returning *;

-- name: ListMyRegistrations :many
select r.*, a.title, a.starts_at, a.ends_at, a.status as activity_status,
  c.name as category_name
from activity_registrations r
join activities a on a.id = r.activity_id
join activity_categories c on c.id = a.category_id
where r.user_id = $1 and r.status <> 'cancelled' and a.deleted_at is null
order by a.starts_at desc;

-- name: GetMyActivityFeeByID :one
-- Resit — hanya pendaftaran SENDIRI (user_id caller), sertakan medan
-- papar (tajuk aktiviti/yuran/no. ahli/nama/emel). `fee_cents` guna
-- `coalesce(r.fee_cents_paid, a.fee_cents)` — SENGAJA bukan
-- `a.fee_cents` hidup terus: yuran aktiviti boleh ditukar SELEPAS ahli
-- bayar (`PATCH /activities/:id`), dan resit dijana SEMULA setiap muat
-- turun (tulis ganti kunci R2 stabil) — tanpa snapshot ni, resit sedia
-- wujud akan senyap papar jumlah yang ahli tak pernah bayar (Opus
-- verify 2026-08-15). Fallback ke `a.fee_cents` cuma utk baris lama
-- sebelum lajur `fee_cents_paid` wujud. `title` SENGAJA kekal hidup
-- (bukan snapshot) — nama aktiviti bukan tuntutan kewangan, papar nama
-- TERKINI lebih berguna drpd bekukan typo asal.
select r.id, r.payment_status, r.payment_ref, r.registered_at,
  a.title, coalesce(r.fee_cents_paid, a.fee_cents) as fee_cents, a.currency,
  p.member_id, p.display_name, u.email
from activity_registrations r
join activities a on a.id = r.activity_id
join profiles p on p.user_id = r.user_id
join users u on u.id = r.user_id
where r.id = $1 and r.user_id = $2;

-- name: ListMyActivityPayments :many
-- Sejarah yuran aktiviti seorang ahli — TERMASUK pendaftaran yang telah
-- dibatalkan (beza sengaja drpd ListMyRegistrations di atas, yang tolak
-- baris 'cancelled' sebab tab "Aktiviti Saya" tu untuk pendaftaran AKTIF
-- sahaja): sejarah bayaran patut tetap tunjuk percubaan yang gagal/tak
-- sempat dibayar sebelum disapu, bukan senyap hilang. `fee_cents` —
-- lihat komen `coalesce` di GetMyActivityFeeByID di atas, sebab sama.
select r.*, a.title, coalesce(r.fee_cents_paid, a.fee_cents) as fee_cents,
  a.currency, a.starts_at
from activity_registrations r
join activities a on a.id = r.activity_id
where r.user_id = $1 and r.payment_status <> 'not_required'
order by r.registered_at desc;
