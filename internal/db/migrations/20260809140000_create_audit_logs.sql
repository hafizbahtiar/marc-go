-- +goose Up

-- Jejak audit generik untuk SEMUA entiti yang boleh diubah (post, comment,
-- dan apa-apa yang datang kemudian). Sengaja satu jadual, bukan
-- post_edits/comment_edits berasingan — laporan "apa yang si polan ubah
-- hari ni" jadi satu query, dan entiti baharu tak perlu migration baharu.
create table audit_logs (
  -- bigserial, bukan uuid: log append-only bervolum tinggi, id monotonik
  -- bagi pagination keyset (`id < $before`) yang stabil & murah.
  id bigserial primary key,

  entity_type text not null,
  entity_id uuid not null,
  action text not null check (action in ('create', 'update', 'delete')),

  -- Pelaku. on delete set null supaya padam akaun TIDAK memusnahkan jejak
  -- audit; sebab tu member_id & role disnapshot sebagai teks di bawah —
  -- kedua-duanya kekal walaupun user hilang, dan role snapshot merekod
  -- kuasa yang dia ADA masa tu (role boleh berubah kemudian).
  actor_id uuid references users(id) on delete set null,
  actor_member_id text,
  actor_role_key text,

  -- Delta sahaja, bukan snapshot penuh baris: untuk 'update' hanya field
  -- yang betul-betul berubah disimpan dalam old_values/new_values, dan
  -- changed_fields menyenaraikan namanya untuk tapisan murah.
  -- 'create' -> old_values null; 'delete' -> new_values null.
  changed_fields text[] not null default '{}',
  old_values jsonb,
  new_values jsonb,

  -- Konteks permintaan. Nullable: tindakan sistem (webhook, cron) tiada.
  -- NOTA PDPA: ini data peribadi — ikat pada polisi simpanan sebelum
  -- guna dalam produksi (lihat komen pruning di bawah).
  ip_address text,
  user_agent text,

  created_at timestamptz not null default now()
);

-- "Sejarah entiti ni" — corak bacaan utama (timeline satu post/comment).
create index audit_logs_entity_idx
  on audit_logs (entity_type, entity_id, id desc);

-- "Apa yang user ni pernah buat" — siasatan penyalahgunaan.
create index audit_logs_actor_idx
  on audit_logs (actor_id, id desc) where actor_id is not null;

-- Feed audit global + pruning ikut umur.
create index audit_logs_created_at_idx on audit_logs (created_at desc);

-- Jejak audit yang boleh disunting bukan jejak audit. UPDATE disekat
-- sepenuhnya di peringkat DB supaya bug (atau orang dengan akses psql)
-- tak boleh tulis semula sejarah.
--
-- DELETE sengaja DIBENARKAN: pruning ikut polisi simpanan ialah operasi
-- sah, dan menyekatnya bermakna jadual ni membesar selamanya.
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

-- +goose Down
drop trigger if exists audit_logs_no_update on audit_logs;
drop function if exists audit_logs_reject_update();
drop table if exists audit_logs;
