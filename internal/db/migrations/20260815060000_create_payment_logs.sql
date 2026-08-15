-- +goose Up

-- payment_logs — jejak PERISTIWA bayaran merentas SEMUA modul (donation
-- Stripe, yuran pendaftaran, yuran aktiviti — dua yang terakhir ToyyibPay).
-- BUKAN audit_logs (yang delta perubahan MEDAN pada entiti yang boleh
-- disunting) — ni log peristiwa append-only untuk diagnosis + reconcile,
-- keputusan produk 2026-08-15 lepas beberapa insiden webhook ToyyibPay
-- (parse gagal senyap, billcode tak dijumpai) yang cuma dapat didiagnosis
-- betul-betul lepas gali log Railway + query DB terus, sepatutnya boleh
-- nampak terus dari SATU jadual kalau ni dah wujud dari awal.
--
-- SENGAJA tiada CHECK pada `event`/`status` (beza drpd `module` di bawah,
-- yang ada set tetap kecil dan bermakna untuk retention/filter) — ni
-- jadual log/observability, bukan state machine yang menggerbang tingkah
-- laku macam registration_payments.status. Kekang terlalu ketat pada
-- medan log cuma buat baris log MASA HADAPAN (event baharu, status baharu
-- dari gateway baharu) ditolak DB sebab kita tak ramal semua bentuk
-- awal-awal — kos gagal ralat lebih tinggi drpd kos ejaan tak konsisten
-- pada data yang cuma dibaca manusia/reconciler.
create table payment_logs (
  -- bigserial, bukan uuid: log append-only bervolum tinggi, id monotonik
  -- bagi pagination keyset yang stabil & murah (padanan corak audit_logs).
  id bigserial primary key,

  module text not null check (module in ('donation', 'registration_fee', 'activity_fee')),
  event text not null,
  status text not null,

  gateway text not null,
  gateway_ref text,
  amount_cents integer,

  -- on delete set null: padam akaun TAK memusnahkan jejak kewangan —
  -- sama rasional macam audit_logs.actor_id.
  user_id uuid references users(id) on delete set null,
  -- Baris berkaitan (donations.id / registration_payments.id /
  -- activity_registrations.id) — TIADA foreign key sengaja: tiga jadual
  -- berbeza berkongsi lajur ni, dan baris payment_logs MESTI kekal walau
  -- baris asal dipadam/tak wujud lagi (log historial, bukan rujukan
  -- langsung).
  related_id uuid,

  message text,
  -- Payload MENTAH gateway (webhook body / respons poll) — CRITICAL:
  -- seluruh insiden 2026-08-15 (";" mentah, "%" tak sah, billcode tak
  -- dijumpai) hanya dapat didiagnosis SEBENAR lepas SSH terus ke Railway
  -- + query DB. Kalau payload mentah dah tersimpan di sini dari awal,
  -- diagnosis jadi SATU query, bukan berjam-jam. Boleh bawa PII pembayar
  -- (nama/emel/phone via billTo/billEmail/billPhone ToyyibPay) — akses DB
  -- terhad kepada pelayan, dan retention 3 bulan (internal/retention)
  -- hadkan tempoh ia hidup.
  --
  -- `text`, BUKAN `jsonb` — Opus verify 2026-08-15 jumpa bug: callback
  -- ToyyibPay form-urlencoded (`billcode=abc&status=1&...`), BUKAN JSON.
  -- `jsonb` tolak INSERT terus ("invalid input syntax for type json"),
  -- dan sebab paymentlog.Record best-effort (sengaja, elak gagalkan
  -- laluan bayaran sebenar), kegagalan tu senyap — payload TAK PERNAH
  -- tersimpan untuk DUA modul ToyyibPay, TEPAT dua yang jadi sebab ciri
  -- ni dibina. Stripe (JSON) je yang berfungsi. `text` terima apa-apa
  -- bentuk (form/JSON/multipart) tanpa validasi struktur — betul untuk
  -- log diagnostik, bukan data yang perlu diquery ikut medan JSON.
  raw_payload text,

  created_at timestamptz not null default now()
);

create index payment_logs_module_created_idx on payment_logs (module, created_at desc);
create index payment_logs_gateway_ref_idx on payment_logs (gateway, gateway_ref) where gateway_ref is not null;
-- Retention (internal/retention) padam ikut created_at — indeks berasingan
-- drpd (module, created_at) di atas supaya sapuan retention tak perlu
-- imbas ikut lajur module yang tak relevan untuknya.
create index payment_logs_created_at_idx on payment_logs (created_at);

-- +goose Down
drop table if exists payment_logs;
