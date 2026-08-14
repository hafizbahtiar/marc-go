-- +goose Up
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected',
                  'activity_published', 'activity_cancelled', 'certificate_ready'));

-- +goose Down
-- NOTE: sama seperti 20260807120100 — down ini akan gagal kalau baris
-- activity_*/certificate_ready sudah wujud. Itu dijangka untuk rollback
-- dev, bukan masalah untuk deploy ke hadapan.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected'));
