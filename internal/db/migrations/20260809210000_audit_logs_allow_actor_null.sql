-- +goose Up

-- Betulkan kebuntuan: memadam user MUSTAHIL sebelum ni.
--
-- `audit_logs.actor_id` ada `on delete set null` supaya jejak audit kekal
-- apabila akaun dipadam. Tetapi "set null" itu satu **UPDATE** pada
-- audit_logs, dan trigger append-only menolak semua UPDATE kecuali bentuk
-- redaksi PII yang tepat. Kesannya: `delete from users` gagal dengan
-- "audit_logs append-only: hanya redaksi ip_address/user_agent
-- dibenarkan", dan akaun tak boleh dipadam langsung.
--
-- Peraturan baharu, lebih tepat dan meliputi kedua-dua kes: tiga lajur
-- (`actor_id`, `ip_address`, `user_agent`) boleh DIKOSONGKAN kepada NULL
-- dan tiada apa lagi. Setiap lajur lain mesti kekal sama, dan lajur yang
-- boleh dikosongkan itu tak boleh ditulis ganti dengan nilai LAIN — cuma
-- dibuang. Sejarah kekal tak boleh ditulis semula.
-- +goose StatementBegin
create or replace function audit_logs_reject_update() returns trigger as $$
begin
  if new.id is not distinct from old.id
     and new.entity_type is not distinct from old.entity_type
     and new.entity_id is not distinct from old.entity_id
     and new.action is not distinct from old.action
     and new.actor_member_id is not distinct from old.actor_member_id
     and new.actor_role_key is not distinct from old.actor_role_key
     and new.changed_fields is not distinct from old.changed_fields
     and new.old_values is not distinct from old.old_values
     and new.new_values is not distinct from old.new_values
     and new.created_at is not distinct from old.created_at
     -- Boleh dikosongkan: kekal sama, atau jadi NULL. Tiada yang lain.
     and (new.actor_id is not distinct from old.actor_id or new.actor_id is null)
     and (new.ip_address is not distinct from old.ip_address or new.ip_address is null)
     and (new.user_agent is not distinct from old.user_agent or new.user_agent is null)
  then
    return new;
  end if;

  raise exception 'audit_logs append-only: hanya pengosongan actor_id/ip_address/user_agent dibenarkan';
end;
$$ language plpgsql;
-- +goose StatementEnd

-- +goose Down
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
