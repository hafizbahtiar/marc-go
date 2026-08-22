-- +goose Up

-- Indeks pada post_images.r2_key — DUA query menapis ikut lajur ni dan
-- kedua-duanya sebelum ni seq scan seluruh jadual:
--
--   ListStalePendingUploads      (baharu, lihat TODO.md L28) — semak kunci
--                                yang MASIH dilekatkan pada post sebelum
--                                menggilirkannya untuk dipadam dari R2
--   ListOrphanedPostImageKeys    (sedia ada) — imbas post_images penuh
--                                setiap pusingan reaper
--
-- post_images tumbuh selama-lamanya (baris dikekalkan walaupun post
-- soft-deleted, sebagai rekod apa yang pernah dilekatkan), jadi seq scan
-- setiap 15 minit jadi lebih mahal setiap hari. Bukan unik: kunci yang
-- sama boleh muncul pada lebih drpd satu baris kalau post dipadam lalu
-- dicipta semula dgn kunci sedia ada, dan tiada apa dalam skema yang
-- menghalangnya.
create index post_images_r2_key_idx on post_images(r2_key);

-- +goose Down
drop index if exists post_images_r2_key_idx;
