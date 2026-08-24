# Perluasan profil ahli: alamat, waris, kesihatan, status aktif (2026-08-25)

Menyentuh dua repo: `marc_go` (schema + API), `marc_flutter` (UI). Time-boxed -
spec ringkas + terus laksana guna subagent, bukan proses SDD penuh.

## Skop

1. **Alamat** - ahli boleh simpan sehingga **3** alamat, **1** wajib default.
   Medan: jenis (landed/highrise), no. unit/rumah, tingkat, blok, jalan,
   township, bandar, poskod, negeri.
2. **Waris** - nama + no. telefon kecemasan (single, bukan senarai - profil
   ada SATU waris utama, bukan berbilang. Kalau perlu >1 di masa depan,
   boleh diperluas jadi table macam alamat).
3. **Tahap kesihatan** - nota bebas teks (bukan enum - keadaan kesihatan
   terlalu pelbagai utk disenaraikan tertutup; freeform lebih fleksibel,
   padanan corak `bypass_reason`).
4. **Status aktif/tak aktif** - flag keahlian, BUKAN status kelulusan
   (`status` sedia ada pending/approved/rejected kekal tak berubah). Ahli
   `approved` boleh jadi tak aktif kemudian (cth berhenti, tak lagi terlibat)
   tanpa perlu tolak pendaftaran asal.

## Keputusan reka bentuk

| Soalan | Keputusan | Sebab |
|---|---|---|
| Alamat: table baru atau lajur JSON pada `profiles`? | **Table baru** `member_addresses` | Berbilang (sehingga 3) + query per-alamat (default, edit, padam) - JSON blob buat semua operasi ni janggal |
| "Alamat penuh" sbg satu lajur teks berasingan? | **Tidak disimpan** - dikira dari medan berstruktur bila perlu papar | Elak dua sumber kebenaran (teks bebas vs medan berstruktur boleh songsang) |
| Had 3 alamat + wajib 1 default | Had 3 = semak app-layer (COUNT dlm tx sebelum INSERT). Wajib 1 default = **partial unique index** (`where is_default`) + app-layer auto-promote bila default dipadam | DB constraint utk invariant "paling banyak SATU default", app logic utk had kiraan (sama pola sequences/counter sedia ada) |
| Waris: table atau lajur pada `profiles`? | **Lajur pada `profiles`** (`emergency_contact_name`, `emergency_contact_phone`) | Keadaan tunggal kekal, sama pola `telegram_chat_id` - bukan senarai bersejarah |
| Kesihatan: lajur pada `profiles`? | **Ya**, `health_notes text` | Sama pola - atribut profil tunggal |
| Siapa boleh edit alamat/waris/kesihatan? | **Ahli sendiri** (self-service, macam `display_name`/`phone`) | Data peribadi ahli isi sendiri, bukan admin |
| Siapa boleh tukar status aktif? | **Management sahaja**, hierarki rank sama macam `UpdateMemberRole` (caller.RoleRank > target.RoleRank, tak boleh ubah diri sendiri) | Status keahlian ialah keputusan organisasi, bukan self-service - padanan corak kelulusan/role sedia ada |
| RLS/Supabase policies? | **Tiada** - akses DB terus via `pgxpool` dari `marc_go`, authz dikuatkuasakan di handler (padanan seluruh backend sedia ada) | Projek ni bukan guna PostgREST/RLS langsung; itu reka bentuk awal (sebelum backend Go wujud) - schema semasa semua guna app-layer authz |

## Schema (`marc_go`)

### Migration 1 - `member_addresses` (table baru)

```sql
create table member_addresses (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  label text,                          -- "Rumah"/"Pejabat" dsb, opsyenal
  is_default boolean not null default false,
  address_type text not null check (address_type in ('landed','highrise')),
  unit_number text,                    -- no. rumah (landed) / no. unit (highrise)
  floor text,                          -- tingkat - highrise
  block text,                          -- blok - highrise
  street text,                         -- nama jalan
  township text,                          -- nama township/perumahan
  city text not null,                  -- bandar
  postcode text not null,              -- poskod (5 digit)
  state text not null,                 -- negeri
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index member_addresses_user_id_idx on member_addresses(user_id);
-- Paling banyak SATU default setiap ahli - dikuatkuasakan DB, bukan cuma app.
create unique index member_addresses_one_default_per_user
  on member_addresses(user_id) where is_default;
```

Had 3 alamat/ahli: semak `count(*)` dalam transaksi yang sama sebelum INSERT
(handler), padanan cara `sequences`/nombor ahli dikira - bukan constraint DB
sebab "3" ialah peraturan produk yang boleh berubah, bukan invariant struktur.

### Migration 2 - lajur baru pada `profiles`

```sql
alter table profiles
  add column emergency_contact_name text,
  add column emergency_contact_phone text,
  add column health_notes text,
  add column is_active boolean not null default true;
```

`is_active` default `true` - ahli sedia ada semua kekal aktif lepas migrate,
tiada kesan retroaktif.

## API (`marc_go`)

### Self-service (mana-mana ahli log masuk, profil sendiri)

**`GET /me`** - tambah pada `profileResponse`:
```json
{
  "emergency_contact_name": "string | null",
  "emergency_contact_phone": "string | null",
  "health_notes": "string | null",
  "is_active": true
}
```

**`PATCH /me`** - tambah medan pilihan (pointer, `nil` = tak ubah, padanan
`display_name`/`phone` sedia ada):
- `emergency_contact_name` *string (max 100)
- `emergency_contact_phone` *string (max 30, disahkan `phone.NormalizeMY`
  sama macam `phone` - string kosong dibenarkan utk buang nombor)
- `health_notes` *string (max 500)

(`is_active` **bukan** boleh-tulis via `/me` - management sahaja, endpoint
berasingan di bawah.)

**`GET /me/addresses`** → `200 []addressResponse`

**`POST /me/addresses`** → body:
```json
{
  "label": "string?",
  "address_type": "landed | highrise",
  "unit_number": "string?",
  "floor": "string?",
  "block": "string?",
  "street": "string?",
  "township": "string?",
  "city": "string",
  "postcode": "string (5 digit)",
  "state": "string",
  "is_default": false
}
```
→ `201 addressResponse`. Tolak `400` kalau dah ada 3. Alamat PERTAMA ahli
paksa `is_default=true` tanpa kira body (elak keadaan "0 default").

**`PATCH /me/addresses/:id`** - semua medan di atas jadi pilihan (partial
update, padanan `PATCH /me`). `is_default=true` nyahtetapkan default lama
dalam transaksi yang sama. `404` kalau `:id` bukan milik caller.

**`DELETE /me/addresses/:id`** → `204`. Kalau baris dipadam ialah default DAN
ada baris lain tinggal, auto-promote baris paling lama (created_at) jadi
default baharu dalam transaksi yang sama.

`addressResponse`:
```json
{
  "id": "uuid",
  "label": "string | null",
  "is_default": true,
  "address_type": "landed | highrise",
  "unit_number": "string | null",
  "floor": "string | null",
  "block": "string | null",
  "street": "string | null",
  "township": "string | null",
  "city": "string",
  "postcode": "string",
  "state": "string",
  "created_at": "RFC3339",
  "updated_at": "RFC3339"
}
```

### Management

**`PATCH /members/:id/active`** → body `{"is_active": bool}` →
`200 {"user_id": "uuid", "is_active": bool}`.

Gate (padanan `UpdateMemberRole`):
- `caller.RoleCategory == authz.CategoryManagement`
- `caller.RoleRank > target.RoleRank`
- `targetID == callerID` → `400` (tak boleh ubah status sendiri)

Tulis `audit.Record` (`EntityProfile`, `ActionUpdate`, old/new `is_active`),
sama pola `UpdateMemberRole`.

**`GET /members`** (`memberResponse`) - tambah `is_active: bool` supaya
senarai management papar status.

## Flutter (`marc_flutter`)

- `Profile` (`profile_providers.dart`): tambah `emergencyContactName`,
  `emergencyContactPhone`, `healthNotes`, `isActive`.
- `MemberRow`: tambah `isActive`.
- `AddressRow` model baru + `AddressRepository` (dio `GET/POST/PATCH/DELETE
  /me/addresses...`) + `addressesProvider` (`FutureProvider<List<AddressRow>>`).
- `EditProfilePage`: tambah field waris (nama + telefon) + nota kesihatan -
  reuse `AuthField`, ikut corak sedia ada tepat (Form + validator ringkas).
- Skrin baru `manage_addresses_page.dart` - senarai alamat (ListTile ringkas,
  padanan `members_page.dart`, BUKAN gaya kad - ikut arahan terkini "simple,
  good UI/UX, jangan lari dari modul lain"). Tambah/edit via skrin/borang
  berasingan (`address_form_page.dart`) - dropdown negeri (16 negeri/wilayah
  Malaysia), radio landed/highrise (tunjuk/sembunyi tingkat+blok ikut jenis),
  suis "jadikan default", padam dgn confirm dialog (`confirm_dialog.dart`
  sedia ada).
- `ProfilePage`: pautan masuk ke skrin alamat.
- `members_page.dart`: management nampak status aktif (chip kedua, warna
  beza drpd role chip) + tindakan tukar (confirm dialog), gate `canEdit`
  sedia ada (rank hierarchy) dipakai semula.
- UI bahasa Melayu, ikut tema `AppTheme` sedia ada - TIADA warna/komponen
  baru di luar `ColorScheme`/`AppSemanticColors`.

## Pelan pelaksanaan (ringkas)

1. **Backend**: migration ×2, `sqlc generate`, handler alamat (CRUD) + luas
   `UpdateMe`/`Me` + endpoint `PATCH /members/:id/active` + luas
   `Members`/`toMemberResponse`, daftar route (`router.go`), `go build`/`go
   vet`/test unit sedia ada masih lulus.
2. **Frontend**: model + repository + provider, `EditProfilePage` tambah
   medan, skrin alamat baharu (list + form), `members_page.dart` tambah
   status aktif, `flutter analyze` bersih.

Dua kerja di atas laksana **selari** (subagent berasingan) - kontrak API di
atas tetap (fixed), jadi frontend boleh bina terus terhadap kontrak tanpa
tunggu backend siap dulu.
