-- +goose Up

-- L35 — like pada KOMEN kini menghantar notifikasi kepada penulis komen
-- (keputusan produk 2026-08-22). Sebelum ni cuma like pada POST yang
-- memberitahu; ketiadaan `comment_like` bukan keputusan yang direkod,
-- ia jurang.
--
-- Luaskan kekangan, bukan buang: senarai tertutup di sini ialah yang
-- menahan jenis tersalah eja daripada masuk secara senyap lalu menjadi
-- notifikasi yang klien tak tahu cara papar.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'comment_like',
                  'member_pending', 'member_approved', 'member_rejected',
                  'activity_published', 'activity_cancelled', 'certificate_ready',
                  'activity_reminder'));

-- +goose Down

-- ⚠️ Gagal kalau baris 'comment_like' sudah wujud — dijangka untuk
-- rollback dev, padanan gelagat 20260807120100/20260810100600.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment',
                  'member_pending', 'member_approved', 'member_rejected',
                  'activity_published', 'activity_cancelled', 'certificate_ready',
                  'activity_reminder'));
