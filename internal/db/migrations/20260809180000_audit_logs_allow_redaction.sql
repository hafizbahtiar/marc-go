-- +goose Up

-- Longgarkan sekatan append-only pada audit_logs supaya SATU jenis UPDATE
-- dibenarkan: meredaksi metadata permintaan (ip_address, user_agent).
--
-- Kenapa perlu: kedua-dua lajur itu data peribadi. Di bawah PDPA ia tak
-- patut disimpan lebih lama daripada tujuannya (siasatan penyalahgunaan),
-- tetapi CATATAN audit itu sendiri — siapa ubah apa — bernilai jauh lebih
-- lama. Memadam keseluruhan baris hanya untuk membuang alamat IP akan
-- memusnahkan jejak audit demi privasi, sedangkan kedua-duanya boleh
-- dipenuhi serentak: kekalkan catatan, buang metadata rangkaian.
--
-- Trigger ditukar daripada peringkat-PENYATA kepada peringkat-BARIS sebab
-- ia kini perlu memeriksa nilai lama lawan baharu, bukan sekadar menolak
-- semua UPDATE. Setiap lajur lain mesti tidak berubah, dan ip/user_agent
-- hanya boleh menjadi NULL — ia TAK boleh ditulis ganti dengan nilai lain.
-- `is not distinct from` (bukan `=`) supaya perbandingan NULL berkelakuan
-- betul.
-- +goose StatementBegin
create or replace function audit_logs_reject_update() returns trigger as $$
begin
  if new.id is not distinct from old.id
     and new.entity_type is not distinct from old.entity_type
     and new.entity_id is not distinct from old.entity_id
     and new.action is not distinct from old.action
     and new.actor_id is not distinct from old.actor_id
     and new.actor_member_id is not distinct from old.actor_member_id
     and new.actor_role_key is not distinct from old.actor_role_key
     and new.changed_fields is not distinct from old.changed_fields
     and new.old_values is not distinct from old.old_values
     and new.new_values is not distinct from old.new_values
     and new.created_at is not distinct from old.created_at
     and new.ip_address is null
     and new.user_agent is null
  then
    return new;
  end if;

  raise exception 'audit_logs append-only: hanya redaksi ip_address/user_agent dibenarkan';
end;
$$ language plpgsql;
-- +goose StatementEnd

drop trigger if exists audit_logs_no_update on audit_logs;

create trigger audit_logs_no_update
  before update on audit_logs
  for each row execute function audit_logs_reject_update();

-- Menyokong kedua-dua sapuan simpanan (redaksi ikut umur, padam ikut umur)
-- tanpa imbasan penuh jadual.
create index if not exists audit_logs_pii_idx
  on audit_logs (created_at)
  where ip_address is not null or user_agent is not null;

-- +goose Down
drop index if exists audit_logs_pii_idx;
drop trigger if exists audit_logs_no_update on audit_logs;

-- +goose StatementBegin
create or replace function audit_logs_reject_update() returns trigger as $$
begin
  raise exception 'audit_logs append-only: UPDATE tidak dibenarkan';
end;
$$ language plpgsql;
-- +goose StatementEnd

create trigger audit_logs_no_update
  before update on audit_logs
  for each statement execute function audit_logs_reject_update();
