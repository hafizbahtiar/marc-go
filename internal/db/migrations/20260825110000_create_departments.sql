-- +goose Up

-- Rujukan bahagian/jabatan organisasi (MAIWP) - superadmin SAHAJA boleh
-- urus (padanan gate `blocked_email_domains`), skop root-level config.
-- `code` sbg primary key (natural key, padanan corak `blocked_email_domains`
-- guna `domain` sbg PK - tiada uuid buatan perlu utk senarai rujukan
-- yang kod dia sendiri dah unik & stabil). `sort_order` kekalkan susunan
-- organisasi ASAL (Pej. Setiausaha/KPE dulu, dst) - BUKAN abjad.
create table departments (
  code text primary key,
  name text not null,
  sort_order integer not null default 0,
  added_by uuid references users(id) on delete set null,
  created_at timestamptz not null default now()
);

-- Kod TIADA '/' - path segment CRUD (`PATCH/DELETE /admin/departments/:code`)
-- pakai Gin default (URL didahulukan-decode SEBELUM padanan laluan), jadi
-- '/' dlm kod pecah jadi >1 segmen dan 404 senyap (Opus verify 2026-08-25).
insert into departments (code, name, sort_order) values
  ('PEJ. SU-KPE', 'PEJABAT SETIAUSAHA / KETUA PEGAWAI EKSEKUTIF', 10),
  ('BKP', 'BAHAGIAN KHIDMAT PENGURUSAN', 20),
  ('BPI', 'BAHAGIAN PEMBANGUNAN INSAN', 30),
  ('MCL', 'MAIWP CAWANGAN LABUAN', 40),
  ('BPPH', 'BAHAGIAN PEMBANGUNAN DAN PELABURAN HARTANAH', 50),
  ('BAZ', 'BAHAGIAN AGIHAN ZAKAT', 60),
  ('UUU', 'UNIT UNDANG-UNDANG', 70),
  ('BWP', 'BAHAGIAN KEWANGAN DAN PELABURAN', 80),
  ('BPA', 'BAHAGIAN PEMBANGUNAN ASNAF', 90),
  ('UAD', 'UNIT AUDIT DALAM', 100),
  ('BPSM', 'BAHAGIAN PEMBANGUNAN SUMBER MANUSIA', 110),
  ('PEJ. TKPEP', 'PEJABAT TIMBALAN KETUA PEGAWAI EKSEKUTIF PENGURUSAN', 120),
  ('IKB', 'INSTITUT KEMAHIRAN BAITULMAL', 130),
  ('UKK', 'UNIT KOMUNIKASI KORPORAT', 140),
  ('UIP', 'UNIT INTEGRITI DAN PEMATUHAN', 150),
  ('BWA', 'BAHAGIAN WAKAF DAN SUMBER AM', 160),
  ('PEJ. TKPEO', 'PEJABAT TIMBALAN KETUA EKSEKUTIF OPERASI', 170);

-- +goose Down
drop table if exists departments;
