-- +goose Up
alter table activity_certificates
  add column category_name text not null default '',
  add column template_primary_color text not null default '#E21E28',
  add column template_secondary_color text not null default '#223145',
  add column template_title text not null default 'Sijil Penyertaan',
  add column template_subtitle text not null default 'MARC',
  add column template_body_text text not null default 'Diberikan kepada [Nama penerima] atas penyertaan dalam aktiviti MARC.',
  add column template_issuer_name text not null default 'MARC',
  add column template_signature_name text not null default 'Pengurusan MARC',
  add column template_footer_text text not null default 'Sijil ini dijana secara rasmi oleh MARC.';

-- +goose Down
alter table activity_certificates
  drop column if exists category_name,
  drop column if exists template_primary_color,
  drop column if exists template_secondary_color,
  drop column if exists template_title,
  drop column if exists template_subtitle,
  drop column if exists template_body_text,
  drop column if exists template_issuer_name,
  drop column if exists template_signature_name,
  drop column if exists template_footer_text;
