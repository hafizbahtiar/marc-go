-- +goose Up
create table certificate_templates (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  is_active boolean not null default false,
  primary_color text not null default '#E21E28',
  secondary_color text not null default '#223145',
  logo_url text not null default '/marc-logo-penuh.png',
  title text not null default 'Sijil Penyertaan',
  subtitle text not null default 'MARC',
  body_text text not null default 'Diberikan kepada [Nama penerima] atas penyertaan dalam aktiviti MARC.',
  issuer_name text not null default 'MARC',
  signature_name text not null default 'Pengurusan MARC',
  footer_text text not null default 'Sijil ini dijana secara rasmi oleh MARC.',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create unique index certificate_templates_one_active_idx
  on certificate_templates (is_active)
  where is_active;

insert into certificate_templates (
  name, is_active, primary_color, secondary_color, logo_url, title, subtitle,
  body_text, issuer_name, signature_name, footer_text
) values (
  'Template MARC Standard',
  true,
  '#E21E28',
  '#223145',
  '/marc-logo-penuh.png',
  'Sijil Penyertaan',
  'MARC',
  'Diberikan kepada [Nama penerima] atas penyertaan dalam aktiviti MARC.',
  'MARC',
  'Pengurusan MARC',
  'Sijil ini dijana secara rasmi oleh MARC.'
);

-- +goose Down
drop table if exists certificate_templates;
