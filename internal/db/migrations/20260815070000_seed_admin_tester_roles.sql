-- +goose Up

-- `admin` — tier baharu ANTARA manager(60) dan superadmin(100), kategori
-- 'management' (keputusan produk 2026-08-15). Lebih kuasa drpd manager
-- tapi TAK automatik dapat semua kuasa superadmin — mana-mana semakan
-- kebenaran yang secara eksplisit menuntut rank superadmin (bukan
-- authz.IsManagement umum) kekal luar capaian admin sehingga
-- dikuatkuasakan sebaliknya.
--
-- `tester` — akaun review Google Play/App Store, kategori 'ahli'
-- SENGAJA: tester berkelakuan macam ahli biasa untuk SEMUA capaian
-- (daftar, post, like, daftar aktiviti, lihat skrin) — reviewer perlu uji
-- aliran app sebenar utk lulus review. Rank 5 (bawah ahli=10, bukan sama)
-- supaya ia tak pernah tersilap disamakan dengan ahli dalam perbandingan
-- rank (cth pemberian role management: `newRole.Rank >= caller.RoleRank`).
-- Sekatan sebenar (checkout bayaran) dikuatkuasakan oleh
-- `middleware.BlockTesterWrites` pada route checkout, BUKAN oleh
-- rank/category — category 'ahli' sengaja supaya semua gate
-- authz.IsManagement sedia ada terus terpakai tanpa ubah apa-apa.
insert into roles (key, name, category, rank) values
  ('tester', 'Tester', 'ahli', 5),
  ('admin', 'Admin', 'management', 80);

-- +goose Down
delete from roles where key in ('tester', 'admin');
