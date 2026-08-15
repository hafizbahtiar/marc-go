-- +goose Up

-- `reminder_sent_at` — bendera "peringatan H-1 dah dihantar" (padanan
-- pola `certificates_issued_at` sedia ada: timestamp NULL sekali sahaja,
-- gate tindakan sekali-sahaja). SEBAB WUJUD: dua goroutine latar sedia
-- ada (reaper 15 minit, retention harian) berjalan pada SETIAP replika
-- Railway — tanpa bendera ni, N replika akan hantar N peringatan kepada
-- orang yang sama setiap kali sapuan jalan (internal/activitylifecycle).
alter table activities add column reminder_sent_at timestamptz;

-- Jenis notifikasi baharu utk peringatan H-1.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected',
                  'activity_published', 'activity_cancelled', 'certificate_ready', 'activity_reminder'));

-- +goose Down
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected',
                  'activity_published', 'activity_cancelled', 'certificate_ready'));
alter table activities drop column reminder_sent_at;
