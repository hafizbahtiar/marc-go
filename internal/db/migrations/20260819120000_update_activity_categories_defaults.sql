-- +goose Up
insert into activity_categories (key, name, sort_order) values
  ('hiking', 'Hiking', 10),
  ('brisk_walk', 'Brisk Walk', 20),
  ('water_rafting', 'Water Rafting', 30),
  ('camping', 'Camping', 40),
  ('caving', 'Caving', 50),
  ('atv', 'ATV', 60),
  ('berbasikal', 'Berbasikal', 70),
  ('csr', 'CSR', 80),
  ('bola_sepak', 'Bola Sepak', 90),
  ('badminton', 'Badminton', 100),
  ('ping_pong', 'Ping Pong', 110),
  ('dart', 'Dart', 120),
  ('bola_tampar', 'Bola Tampar', 130),
  ('bola_jaring', 'Bola Jaring', 140),
  ('riadah', 'Riadah', 150),
  ('lain_lain', 'Lain-lain', 160)
on conflict (key) do update
  set name = excluded.name,
      sort_order = excluded.sort_order,
      is_active = true;

-- Dropped from the default list — deactivated (not deleted) so existing
-- activities that already reference these categories keep working.
update activity_categories
  set is_active = false
  where key in ('futsal', 'larian');

-- +goose Down
update activity_categories
  set is_active = true
  where key in ('futsal', 'larian');

delete from activity_categories
  where key in (
    'hiking', 'brisk_walk', 'water_rafting', 'camping', 'caving', 'atv',
    'berbasikal', 'csr', 'bola_sepak', 'dart', 'bola_jaring', 'riadah'
  );

update activity_categories set sort_order = 10 where key = 'badminton';
update activity_categories set sort_order = 30 where key = 'bola_tampar';
update activity_categories set sort_order = 50 where key = 'ping_pong';
update activity_categories set sort_order = 900 where key = 'lain_lain';
