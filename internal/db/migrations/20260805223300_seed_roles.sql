-- +goose Up
insert into roles (key, name, category, rank) values
  ('ahli', 'Ahli', 'ahli', 10),
  ('supervisor', 'Supervisor', 'management', 50),
  ('manager', 'Manager', 'management', 60),
  ('superadmin', 'Super Admin', 'management', 100);

-- +goose Down
delete from roles where key in ('ahli', 'supervisor', 'manager', 'superadmin');
