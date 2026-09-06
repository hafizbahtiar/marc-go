-- name: ListCertificateTemplates :many
select *
from certificate_templates
order by is_active desc, updated_at desc, name asc;

-- name: GetCertificateTemplate :one
select *
from certificate_templates
where id = $1;

-- name: GetActiveCertificateTemplate :one
select *
from certificate_templates
where is_active
order by updated_at desc
limit 1;

-- name: UpdateCertificateTemplate :one
update certificate_templates
set
  name = $2,
  primary_color = $3,
  secondary_color = $4,
  logo_url = $5,
  title = $6,
  subtitle = $7,
  body_text = $8,
  issuer_name = $9,
  signature_name = $10,
  footer_text = $11,
  updated_at = now()
where id = $1 and updated_at = $12
returning *;

-- name: PublishCertificateTemplate :one
with deactivated as (
  update certificate_templates
  set is_active = false, updated_at = now()
  where is_active and id <> $1
)
update certificate_templates
set is_active = true, updated_at = now()
where certificate_templates.id = $1
  and certificate_templates.updated_at = $2
returning *;
