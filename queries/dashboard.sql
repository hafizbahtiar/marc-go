-- name: CountUnreadNotifications :one
select count(*) from notifications
where recipient_id = $1 and read_at is null;

-- name: CountMyCertificates :one
select count(*) from activity_certificates
where user_id = $1 and revoked_at is null;

-- name: CountApprovedMembers :one
select count(*) from profiles
where status = 'approved' and is_active = true;

-- name: ListMyUpcomingRegistrations :many
-- Corak ListMyRegistrations (activity_registrations.sql:206) tetapi
-- hanya yang BELUM tamat dan dihadkan 3 - kad dashboard, bukan senarai
-- penuh (itu /my-activities).
select r.id, r.activity_id, r.payment_status,
  a.title, a.starts_at, a.ends_at, c.name as category_name
from activity_registrations r
join activities a on a.id = r.activity_id
join activity_categories c on c.id = a.category_id
where r.user_id = $1
  and r.status <> 'cancelled'
  and a.deleted_at is null
  and a.ends_at >= now()
order by a.starts_at asc
limit 3;

-- name: ListOpenActivitiesForMe :many
-- Aktiviti terbitan akan datang yang pemanggil BELUM daftar. `not
-- exists` (bukan left join + is null) supaya perancang boleh berhenti
-- pada padanan pertama.
select a.id, a.title, a.starts_at, a.fee_cents, a.currency,
  c.name as category_name,
  (select count(*) from activity_registrations r2
    where r2.activity_id = a.id and r2.status <> 'cancelled') as registration_count
from activities a
join activity_categories c on c.id = a.category_id
where a.deleted_at is null
  and a.status = 'published'
  and a.ends_at >= now()
  and not exists (
    select 1 from activity_registrations r
    where r.activity_id = a.id and r.user_id = $1 and r.status <> 'cancelled'
  )
order by a.starts_at asc
limit 3;

-- name: CountPendingMembers :one
select count(*) from profiles where status = 'pending';

-- name: CountNewMembersThisMonth :one
select count(*) from profiles
where approved_at >= date_trunc('month', now());

-- name: MemberStatsByDepartment :many
-- Tanpa had di sini - handler yang memotong kepada 6 teratas + baris
-- "Lain-lain", supaya jumlah keseluruhan kekal tepat.
select coalesce(d.code, '') as code,
       coalesce(d.name, 'Tiada bahagian') as name,
       count(*) as count
from profiles p
left join departments d on d.code = p.department_code
where p.status = 'approved' and p.is_active = true
group by d.code, d.name
order by count desc;

-- name: ActivityStatsThisMonth :one
-- attendance_rate: kehadiran direkod bagi sesi yang SUDAH TAMAT dalam
-- bulan semasa, dibahagi pendaftaran aktif pada aktiviti sesi-sesi itu.
-- Sesi belum tamat dikecualikan supaya kadar tidak nampak rendah palsu
-- sepanjang bulan berjalan. Pembahagi sifar -> null (bukan 0).
select
  (select count(*) from activities
    where deleted_at is null and status = 'published' and ends_at >= now())
    as upcoming,
  (select count(*) from activity_registrations
    where status <> 'cancelled' and registered_at >= date_trunc('month', now()))
    as registrations_this_month,
  (select case when denom.jumlah = 0 then null
               else numer.jumlah::float8 / denom.jumlah::float8 end
   from
     (select count(*) as jumlah from activity_attendances at
       join activity_sessions s on s.id = at.session_id
      where s.ends_at >= date_trunc('month', now()) and s.ends_at < now()) numer,
     (select count(*) as jumlah from activity_registrations r
       where r.status <> 'cancelled'
         and r.activity_id in (
           select s.activity_id from activity_sessions s
            where s.ends_at >= date_trunc('month', now()) and s.ends_at < now())) denom
  ) as attendance_rate;

-- name: SumRegistrationRevenueThisMonth :one
select coalesce(sum(amount_cents), 0)::bigint
from registration_payments
where status = 'succeeded' and created_at >= date_trunc('month', now());

-- name: SumActivityRevenueThisMonth :one
-- fee_cents_paid = snapshot amaun yang BENAR-BENAR dibayar; sengaja
-- BUKAN activities.fee_cents hidup (yuran boleh ditukar selepas bayar).
select coalesce(sum(fee_cents_paid), 0)::bigint
from activity_registrations
where payment_status = 'paid'
  and fee_cents_paid is not null
  and registered_at >= date_trunc('month', now());

-- name: SumDonationRevenueThisMonth :one
select coalesce(sum(amount_cents), 0)::bigint
from donations
where status = 'succeeded' and created_at >= date_trunc('month', now());
