-- +goose Up

-- L29 — membolehkan baris bayaran ditulis SEBELUM bil gateway wujud.
--
-- Susunan lama: createBill dahulu, INSERT kemudian. Kalau INSERT gagal,
-- bil ToyyibPay yang SAH tinggal hidup dan boleh dibayar sedangkan tiada
-- baris merujuknya — webhook mengena 0 baris dan menyenyapkannya sebagai
-- "replay biasa", dan reconcile melelar baris `registration_payments`
-- jadi ia buta kepada apa yang tak pernah wujud. Duit masuk, sifar
-- rekod.
--
-- Susunan baharu memerlukan baris 'pending' TANPA `gateway_ref` wujud
-- dahulu — dan itu mustahil di bawah skema lama pada DUA kiraan:
--
--   1. `gateway_ref text not null` menolak baris tanpa ref terus.
--   2. Indeks unik PENUH atas (gateway, gateway_ref) bermakna walaupun
--      rentetan kosong dipakai sebagai pengganti, ahli KEDUA yang
--      checkout akan berlanggar dengan yang pertama.
--
-- Kedua-duanya diselesaikan di sini: lajur jadi nullable, dan indeks
-- jadi SEPARA supaya hanya ref SEBENAR dikekang unik. NULL tidak pernah
-- berlanggar dengan NULL dalam indeks unik Postgres, tapi predikat
-- separa dibuat eksplisit supaya niatnya boleh dibaca daripada skema.
alter table registration_payments alter column gateway_ref drop not null;

drop index if exists registration_payments_gateway_gateway_ref_idx;

create unique index registration_payments_gateway_gateway_ref_idx
  on registration_payments (gateway, gateway_ref)
  where gateway_ref is not null;

-- Reconcile dan webhook kedua-duanya melangkau baris tanpa ref (tiada
-- apa nak ditanya pada gateway untuk baris begitu), jadi indeks separa
-- ni turut memadankan corak capaian sebenar.

-- +goose Down

-- ⚠️ MEMUSNAHKAN DATA. Mengembalikan `not null` mustahil selagi baris
-- ref-NULL wujud, dan baris itu ialah tepat yang dicipta oleh susunan
-- baharu (checkout yang bilnya tak pernah berjaya dicipta). Ia dibuang
-- di sini.
--
-- Selamat dalam praktik: baris ref-NULL bermakna createBill GAGAL, jadi
-- tiada bil wujud dan tiada duit pernah berpindah tangan. Ia rekod
-- percubaan, bukan rekod kewangan. Tapi ia TETAP kehilangan data —
-- jangan jalankan Down ni pada produksi tanpa mengeksportnya dahulu.
delete from registration_payments where gateway_ref is null;

drop index if exists registration_payments_gateway_gateway_ref_idx;

create unique index registration_payments_gateway_gateway_ref_idx
  on registration_payments (gateway, gateway_ref);

alter table registration_payments alter column gateway_ref set not null;
