-- +goose Up
update certificate_templates
set
  primary_color = '#E21E28',
  secondary_color = '#223145',
  logo_url = '/marc-logo-penuh.png',
  updated_at = now()
where name = 'Template MARC Standard';

-- +goose Down
-- Keep existing template customizations when rolling back.
