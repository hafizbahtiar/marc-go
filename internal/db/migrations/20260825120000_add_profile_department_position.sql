-- +goose Up

-- Bahagian/jawatan ahli — management (manager ke atas) sahaja boleh
-- tetapkan, bukan self-service (beza drpd emergency_contact/health_notes).
-- `on delete set null` (bukan restrict) — buang satu bahagian dari
-- `departments` TAK sepatutnya block, ahli yang terjejas jadi "tiada
-- bahagian" dan boleh ditetapkan semula.
alter table profiles
  add column department_code text references departments(code) on delete set null,
  add column position text;

create index profiles_department_code_idx on profiles(department_code);

-- +goose Down
alter table profiles
  drop column if exists department_code,
  drop column if exists position;
