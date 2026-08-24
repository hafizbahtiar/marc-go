-- +goose Up

-- Waris (kontak kecemasan) + nota kesihatan - atribut profil TUNGGAL,
-- sama pola telegram_chat_id (bukan senarai bersejarah). `health_notes`
-- sengaja freeform text (bukan enum) - keadaan kesihatan terlalu pelbagai
-- utk disenaraikan tertutup, padanan corak `bypass_reason`.
--
-- `is_active` - flag KEAHLIAN, BUKAN status kelulusan (`status` sedia ada
-- pending/approved/rejected kekal tak berubah). Default `true` supaya
-- ahli sedia ada semua kekal aktif lepas migrate, tiada kesan retroaktif.
alter table profiles
  add column emergency_contact_name text,
  add column emergency_contact_phone text,
  add column health_notes text,
  add column is_active boolean not null default true;

-- +goose Down
alter table profiles
  drop column if exists emergency_contact_name,
  drop column if exists emergency_contact_phone,
  drop column if exists health_notes,
  drop column if exists is_active;
