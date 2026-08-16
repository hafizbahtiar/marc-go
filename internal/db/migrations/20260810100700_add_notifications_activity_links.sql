-- +goose Up
-- Deep-link untuk notifikasi aktiviti. Tanpa lajur ini,
-- activity_published / activity_cancelled / certificate_ready ialah
-- satu-satunya baris dalam senarai notifikasi yang tak boleh diketuk:
-- setiap jenis lain deep-link melalui post_id.
--
-- `on delete cascade` sama seperti post_id/comment_id: notifikasi yang
-- menuding kepada aktiviti atau sijil yang sudah tiada ialah item mati yang
-- sama, cuma lebih teruk.
alter table notifications
  add column activity_id uuid references activities(id) on delete cascade,
  add column certificate_id uuid references activity_certificates(id) on delete cascade;

-- +goose Down
alter table notifications
  drop column certificate_id,
  drop column activity_id;
