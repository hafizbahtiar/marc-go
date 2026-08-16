-- +goose Up

-- Opus verify 2026-08-15 (ciri resit yuran) dedah bug MEDIUM: resit yuran
-- aktiviti baca `activities.fee_cents`/`title` HIDUP semasa jana PDF —
-- pengurus tukar yuran aktiviti SELEPAS ahli bayar, resit yang sedia
-- wujud (dijana semula setiap muat turun, tulis ganti kunci R2 STABIL)
-- senyap bertukar papar jumlah yang ahli TAK PERNAH bayar. Yuran
-- pendaftaran/donation tiada bug ni sebab amaun disimpan TERUS pada
-- baris bayaran (registration_payments.amount_cents/donations.amount_cents)
-- — activity_registrations sepatutnya sama, tapi tiada snapshot langsung
-- sebelum ni.
--
-- `fee_cents_paid` — snapshot amaun SEBENAR dihantar ke gateway semasa
-- checkout (activity_registration_payment.go, sama nilai dgn
-- `payment.CreateParams.AmountCents`), ditulis SEKALI oleh
-- `SetRegistrationPaymentRef` dan tak pernah diubah selepas tu. NULL utk
-- baris yang belum pernah checkout (fee percuma, atau baris lama sebelum
-- migration ni) — pemanggil `coalesce(fee_cents_paid, activities.fee_cents)`
-- sebagai fallback.
alter table activity_registrations add column fee_cents_paid integer;

-- +goose Down
alter table activity_registrations drop column fee_cents_paid;
