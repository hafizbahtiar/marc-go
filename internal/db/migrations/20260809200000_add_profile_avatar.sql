-- +goose Up

-- Kunci objek R2 untuk gambar profil. Nullable — kebanyakan ahli takkan
-- ada satu, dan UI jatuh balik kepada huruf pertama nama.
--
-- Simpan KUNCI, bukan URL penuh: domain awam bucket boleh berubah (kita
-- guna r2.dev sekarang dan akan bertukar sebelum produksi), dan URL yang
-- disimpan akan basi senyap. URL dibina waktu runtime oleh
-- `storage.PublicURL`, sama macam gambar post.
alter table profiles add column avatar_r2_key text;

-- +goose Down
alter table profiles drop column if exists avatar_r2_key;
