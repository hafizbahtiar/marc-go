# Modul Aktiviti — Reka Bentuk

Tarikh: 2026-08-09
Repo terlibat: `marc_go` (backend), `marc_flutter` (klien)

## Masalah

Kelab sukan mengadakan aktiviti. Ahli mendaftar, hadir, dan yang memenuhi
syarat kehadiran menerima sijil penyertaan yang boleh disahkan oleh pihak
ketiga.

## Skop

**Termasuk**: cipta & terbit aktiviti, kategori sukan, sesi berbilang hari,
pendaftaran dengan had kapasiti dan tarikh tutup, pembatalan sendiri,
kehadiran per-sesi (manual + imbas QR), penerbitan sijil PDF, halaman
pengesahan awam.

**Tidak termasuk (sengaja)**:
- **Integrasi pembayaran.** Yuran aktiviti akan wujud, tetapi spec ini hanya
  menyediakan `fee_cents` dan `payment_status` dalam schema. Integrasi
  gateway, pemegang slot semasa menunggu bayaran, luput pendaftaran belum
  bayar, dan bayaran balik bila aktiviti dibatalkan menjadi spec berasingan.
  Sebab: ia bergantung pada kerja yang belum selesai dalam `TODO.md`
  (ToyyibPay belum bermula, ambang RM500 belum di-*wire*), dan framing
  "sumbangan peribadi kepada pembangun" dalam laluan Stripe sedia ada tidak
  sesuai untuk yuran kelab.
- **Check-in self-scan dan kod ditaip.** Schema menyokong keempat-empat
  kaedah dari hari pertama; hanya `manual` dan `scan` dibina sekarang.
  Self-scan memerlukan token berputar untuk menghalang QR di-*screenshot*
  dan dikongsi — kerja yang tidak berbaloi sehingga management benar-benar
  terbeban.
- **Sijil pencapaian** (johan/naib johan) dan sijil peranan (jurulatih,
  pengadil). Penyertaan sahaja.
- **Poster/gambar aktiviti.**
- **Check-in luar talian.**

## Keputusan reka bentuk

### Kehadiran: satu jadual, empat kaedah

Keempat-empat kaedah check-in (manual, imbas oleh management, self-scan, kod
ditaip) menghasilkan baris kehadiran yang **sama**. Yang berbeza hanya
`method` dan `marked_by`. Ini bermakna menambah kaedah baharu kemudian ialah
kerja UI, bukan migration.

### `activities.starts_at` didenormalisasi

Sesi ialah sumber kebenaran untuk masa. `activities.starts_at`/`ends_at`
ialah min/maks sesi yang disimpan pada baris aktiviti, dikira semula dalam
transaksi yang sama setiap kali set sesi berubah.

Sebab: senarai aktiviti perlu diisih dan ditapis ikut tarikh dengan indeks.
Agregat `min()` atas join pada setiap senarai akan menjadi masalah prestasi
yang tidak perlu.

Harganya ialah satu invarian yang boleh dilanggar. Ia ditutup dengan ujian,
dan `PUT /sessions` (ganti keseluruhan set) memastikan hanya ada **satu**
laluan kod yang boleh melanggarnya.

### Nombor siri sijil guna jadual `sequences`, bukan `create sequence`

Sequence Postgres tidak berundur bila transaksi gagal — ia akan meninggalkan
lompang dalam penomboran sijil. Jadual `sequences` sedia ada (`key` →
`current_value`) dikemas kini dengan `update ... returning` dalam transaksi
yang sama, jadi ia berundur dengan betul.

### Penerbitan sijil ialah dua fasa

Menjana PDF dan memuat naik ke R2 tidak boleh berada dalam transaksi
Postgres: transaksi yang menahan kunci selama ratusan muat naik ialah
masalah, dan `rollback` tidak memadam objek R2.

1. **Transaksi**: kira yang layak, ambil nombor siri, masukkan baris sijil
   dengan `r2_key` **null**, tulis catatan audit, komit.
2. **Selepas komit**: untuk setiap baris `r2_key is null` — jana PDF, naik
   ke R2, kemas kini `r2_key`.

Fasa 2 boleh diulang. Kalau proses mati separuh jalan, panggil endpoint
sekali lagi dan ia menyambung; unik `(activity_id, user_id)` menghalang
pendua. Muat turun sijil yang `r2_key` masih null pulang `409` "sedang
disediakan".

### `verify_token` berasingan daripada `serial`

Nombor siri berjujukan. Kalau ia juga kunci pengesahan awam, sesiapa boleh
menuai nama semua ahli kelab dengan menambah satu. Token legap 32 aksara
menutup enumerasi itu.

### Snapshot nama dan tajuk pada sijil

PDF tidak berubah selepas dijana. Kalau halaman pengesahan membaca profil
semasa, ahli yang menukar nama akan menyebabkan halaman tidak sepadan dengan
sijil di tangan.

### Pendedahan privasi yang disedari

`GET /verify/certificates/{token}` ialah endpoint **awam pertama** yang
mendedahkan nama ahli. Ini bercanggah dengan arah kerja privasi setakat ini
(emel disembunyikan daripada bukan-management, objek R2 ditandatangani).

Justifikasi: sijil yang tidak boleh disahkan tiada nilai, dan nama + acara
memang sudah tercetak pada sijil yang penerima sendiri edarkan. Ia keputusan
sedar, bukan terlepas pandang. Perlindungan: token legap, baldi had kadar
bernama `verify`, dan respons terhad kepada medan minimum.

---

## Schema

Migration goose, satu fail setiap jadual, ikut konvensyen sedia ada.

### `activity_categories`

```
id           uuid pk default gen_random_uuid()
key          text not null unique
name         text not null
sort_order   int not null default 0
is_active    boolean not null default true
created_at   timestamptz not null default now()
```

Seed awal (ikut corak `seed_roles`): badminton, futsal, bola tampar, larian,
ping pong, lain-lain.

### `activities`

```
id                        uuid pk
category_id               uuid not null references activity_categories(id) on delete restrict
title                     text not null
description               text not null default ''
location_name             text not null
location_address          text not null default ''
starts_at                 timestamptz not null   -- min(sesi), didenormalisasi
ends_at                   timestamptz not null   -- maks(sesi), didenormalisasi
registration_opens_at     timestamptz            -- null = terbuka serta-merta
registration_closes_at    timestamptz not null
capacity                  int check (capacity > 0)   -- null = tiada had
fee_cents                 int not null default 0 check (fee_cents >= 0)
currency                  text not null default 'MYR'
attendance_threshold_pct  smallint not null default 100
                          check (attendance_threshold_pct between 1 and 100)
status                    text not null default 'draft'
                          check (status in ('draft','published','cancelled','completed'))
cancelled_reason          text
certificates_issued_at    timestamptz
created_by                uuid references users(id) on delete set null
created_at                timestamptz not null default now()
updated_at                timestamptz not null default now()
deleted_at                timestamptz
```

Indeks: `(status, starts_at desc)`, `(category_id)`.

### `activity_sessions`

```
id           uuid pk
activity_id  uuid not null references activities(id) on delete cascade
seq          int not null
title        text not null default ''
starts_at    timestamptz not null
ends_at      timestamptz not null check (ends_at > starts_at)
unique (activity_id, seq)
```

Indeks: `(activity_id, starts_at)`. Aktiviti sehari mempunyai satu sesi
automatik — tiada dua laluan kod.

### `activity_registrations`

```
id              uuid pk
activity_id     uuid not null references activities(id) on delete cascade
user_id         uuid not null references users(id) on delete cascade
status          text not null default 'registered'
                check (status in ('pending_payment','registered','cancelled'))
payment_status  text not null default 'not_required'
                check (payment_status in ('not_required','pending','paid','refunded'))
payment_ref     text          -- cangkuk fasa 2, null buat masa ini
checkin_token   text not null unique
registered_at   timestamptz not null default now()
cancelled_at    timestamptz
```

Unik separa: `(activity_id, user_id) where status <> 'cancelled'` — halang
pendaftaran berganda, benarkan pendaftaran semula selepas batal.

Indeks: `(activity_id, status)`, `(user_id)`.

### `activity_attendances`

```
id               uuid pk
registration_id  uuid not null references activity_registrations(id) on delete cascade
session_id       uuid not null references activity_sessions(id) on delete cascade
method           text not null check (method in ('manual','scan','self_scan','code'))
marked_by        uuid references users(id) on delete set null  -- null bila self check-in
checked_in_at    timestamptz not null default now()
unique (registration_id, session_id)
```

### `activity_certificates`

```
id              uuid pk
activity_id     uuid not null references activities(id) on delete restrict
user_id         uuid not null references users(id) on delete cascade
serial          text not null unique          -- MARC-2026-000123
verify_token    text not null unique          -- rawak legap 32 aksara
recipient_name  text not null                 -- snapshot
activity_title  text not null                 -- snapshot
activity_date   date not null                 -- snapshot
issued_at       timestamptz not null default now()
r2_key          text                          -- null = PDF belum siap
revoked_at      timestamptz
revoked_reason  text
unique (activity_id, user_id)
```

### Migration sokongan

Luaskan `check` jenis pada `notifications` untuk jenis baharu aktiviti —
ikut corak `20260807120100_widen_notifications_member_status`.

---

## Endpoint

Router sedia ada mempunyai tiga kumpulan: `protected` (auth), `approved`
(+ status diluluskan), `verified` (+ emel disahkan). Semakan management
dibuat dalam handler melalui `authz.IsManagement` — **tiada middleware
`RequireManagement` dalam kod ini**, jangan cipta corak baharu.

### Baca — kumpulan `approved`

```
GET /activities                  senarai, cursor pagination
                                 tapis: category, upcoming|past
GET /activities/{id}             detail + sesi + kiraan pendaftaran + keadaan aku
GET /activity-categories
GET /me/activities               penyertaan aku
GET /me/certificates
GET /me/certificates/{id}/file   → presigned URL R2
```

### Tulis oleh ahli — kumpulan `verified`

```
POST   /activities/{id}/registration
DELETE /activities/{id}/registration
```

Diletakkan pada `verified` kerana pendaftaran ialah komitmen yang membawa
nama sebenar ahli ke atas sijil — emel yang tidak disahkan bermakna tiada
cara menghubungi orang yang menuntut slot.

### Management — `verified` + `authz.IsManagement`

```
POST   /activities
PATCH  /activities/{id}
POST   /activities/{id}/publish
POST   /activities/{id}/cancel
PUT    /activities/{id}/sessions
GET    /activities/{id}/registrations
POST   /activities/{id}/sessions/{sid}/attendance
DELETE /activities/{id}/sessions/{sid}/attendance/{rid}
POST   /activities/{id}/certificates
POST   /certificates/{id}/revoke
```

### Awam — tiada auth

```
GET /verify/certificates/{token}
```

### Peraturan

**`PUT` sesi menggantikan keseluruhan set** dalam satu transaksi, kemudian
mengira semula `activities.starts_at`/`ends_at`. Sesi yang sudah mempunyai
kehadiran tidak boleh dibuang — `409`.

**Tanda kehadiran menerima `registration_id` ATAU `checkin_token`.** Skrin
senarai menghantar `registration_id` (`method='manual'`); scanner menghantar
`checkin_token` (`method='scan'`). Handler yang sama.

**Tetingkap masa check-in**: dari 2 jam sebelum `session.starts_at` hingga 2
jam selepas `session.ends_at`. Di luar tetingkap memerlukan tindakan pindaan
berasingan yang dicatat audit sebagai pindaan, bukan check-in biasa. Tanpa
had ini, kehadiran boleh ditanda seminggu kemudian tanpa jejak — dan sijil
bergantung padanya.

**Perebutan kapasiti**: `select ... for update` atas baris `activities`,
kemudian kira pendaftaran aktif dalam transaksi yang sama. Pada skala
ratusan ahli ini percuma dan betul tanpa memerlukan Redis.

**Had kadar**: baldi bernama `verify` untuk endpoint pengesahan awam. Baldi
mesti dinamakan — baldi tanpa nama berkongsi kunci Redis dengan `auth` dan
saling menghabiskan kuota.

**Jejak audit** (`audit_logs` sedia ada): cipta/kemas kini/terbit/batal
aktiviti, tanda & buang tanda kehadiran, pindaan kehadiran di luar
tetingkap, terbit & tarik sijil.

**Tiada audit untuk pendaftaran ahli** — volum tinggi, dan baris pendaftaran
sendiri menyimpan `registered_at`/`cancelled_at`. Keputusan sama seperti
`create` post.

**Push** (modul sedia ada): aktiviti diterbitkan, peringatan H-1, sijil
sedia, aktiviti dibatalkan.

---

## Sijil

### Kelayakan

Server mengira sendiri siapa layak — management tidak menyenaraikan:

```
registration.status = 'registered'
  AND (kehadiran / jumlah_sesi) * 100 >= activity.attendance_threshold_pct
  AND (fee_cents = 0 OR payment_status = 'paid')   -- klausa fasa 2
```

Endpoint hanya menerima aktiviti yang sesi terakhirnya sudah tamat.
Menerbitkan sijil untuk aktiviti yang belum berlaku tidak boleh diperbetulkan
dengan bersih — sijil sudah berada di telefon orang.

### PDF

Modul baharu `internal/certificate`, fungsi tulen
`GeneratePDF(Certificate) ([]byte, error)` — tiada DB, tiada R2, tiada
rangkaian di dalamnya. Sama seperti `internal/receipt`, itulah yang
menjadikannya boleh diuji tanpa infra.

- `go-pdf/fpdf` (sudah dalam `go.mod`), **landskap A4**.
- Kandungan: kepala jenama MARC, "SIJIL PENYERTAAN", nama penerima, tajuk
  aktiviti, tarikh, kategori, nombor siri, QR menuju URL pengesahan.
- Kebergantungan baharu: `skip2/go-qrcode` → PNG dalam memori →
  `RegisterImageReader`. Tiada fail sementara.
- **Fon**: `receipt` guna Helvetica terbina dengan
  `UnicodeTranslatorFromDescriptor` (cp1252). Nama yang mengandungi aksara
  di luar cp1252 akan hilang senyap-senyap. Sahkan nama boleh dikodkan
  **sebelum** menjana dan tolak dengan mesej jelas, bukan terbitkan sijil
  dengan nama tercacat.

Nombor siri: `MARC-<tahun>-<6 digit>`, kunci `sequences` =
`certificate_serial`.

### Halaman pengesahan awam

`GET /verify/certificates/{verify_token}` — tiada auth.

Pulang **hanya**: nama penerima, tajuk aktiviti, tarikh aktiviti, tarikh
terbit, nombor siri, status (`sah` / `ditarik balik`).

Tiada emel, tiada `user_id`, tiada status keahlian, tiada senarai aktiviti
lain.

Token tidak sah pulang `404` yang **sama** dengan sijil yang tidak wujud —
tiada oracle yang membezakan "token salah" daripada "sijil ditarik".

### Tarik balik

`revoked_at` + `revoked_reason`, catatan audit, `r2_key` masuk gilir
`deleted_uploads` dengan `reason = 'certificate_revoked'` untuk reaper sedia
ada.

Halaman pengesahan terus menunjukkan sijil itu **ditarik balik** — baris
kekal, hanya failnya hilang. Memadam baris akan menjadikan sijil yang
ditarik nampak seperti tidak pernah wujud, yang lebih teruk.

---

## Flutter

Struktur ikut corak `features/posts` (`*_models.dart`, `*_providers.dart`,
pages, `widgets/`), Riverpod + go_router + dio.

```
lib/features/activities/
  activity_models.dart
  activity_providers.dart
  activities_page.dart           senarai + tapis kategori, tab Akan Datang / Lepas
  activity_detail_page.dart      sesi, slot berbaki, butang daftar/batal
  my_activities_page.dart        penyertaan aku + QR check-in
  my_certificates_page.dart      senarai sijil + muat turun
  manage/
    activity_form_page.dart      cipta/edit + editor sesi
    registrations_page.dart      senarai peserta, tanda hadir per sesi
    checkin_scanner_page.dart    kamera → scan QR peserta
    issue_certificates_page.dart pratonton yang layak → terbitkan
  widgets/
```

**Navigasi**: tab baharu "Aktiviti" dalam `nav_shell`. "Sijil Saya" dan
"Aktiviti Saya" di bawah Profil — jarang dilawati, tidak berbaloi satu tab.

### Kebergantungan baharu

- `qr_flutter` — papar QR pendaftaran, rendering tempatan sahaja.
- `mobile_scanner` — scanner management.
- **Tiada pakej PDF.** Endpoint pulang presigned URL, `url_launcher` (sudah
  ada) membukanya dalam pelihat PDF peranti. `gal` hanya untuk imej;
  menyimpan PDF akan menarik `path_provider` + `open_filex` + kebenaran
  storan Android untuk faedah hampir sifar.

**Risiko Android — `mobile_scanner`.** `pubspec.yaml` mempunyai komen
panjang tentang `permission_handler` dipin ke 12.0.3 kerana 13.x menarik
masuk `compileSdk 37`. `mobile_scanner` boleh melanggar siling yang sama.
Pin versi yang membina pada compileSdk 35, sahkan `flutter build apk` lulus
**sebelum** menulis skrin scanner, dan catat sebabnya dalam komen `pubspec`
mengikut corak sedia ada. Kalau ia menolak untuk dipin, manual check-in
tetap berfungsi sepenuhnya — ini melambatkan satu skrin, bukan modul.

### Tingkah laku UI

**QR ahli ialah `checkin_token` daripada data yang sudah dimuatkan** — tiada
panggilan rangkaian untuk menjananya. Liputan di gelanggang sukan selalunya
teruk; ahli boleh buka QR sebelum sampai dan ia kekal berfungsi tanpa
isyarat.

**Scanner kekal terbuka**: scan → hantar → toast "✓ Ahmad hadir" → sedia
untuk orang seterusnya. Jangan tolak pengguna keluar skrin setiap scan.
Nyahlantun token yang sama selama 3 saat.

**Empat keadaan kegagalan scan mesti berbeza**: sudah ditanda hadir (bukan
ralat — tunjuk hijau), tidak berdaftar, di luar tetingkap masa, tiada
rangkaian. Logik pemetaan diasingkan daripada skrin supaya boleh diuji tanpa
kamera.

**Check-in memerlukan rangkaian — sengaja.** Tiada baris gilir luar talian:
check-in yang disimpan di peranti boleh dimanipulasi dengan menukar jam
telefon, dan sijil bergantung padanya. Kalau liputan menjadi masalah
sebenar, manual tick + tulis kemudian ialah jalan keluar yang jujur.

**Pendaftaran guna optimistic update** untuk kiraan slot, tetapi mesti
menerima `409 penuh` daripada server dengan bersih — server yang jadi hakim.

---

## Ujian

### Tulen, tiada infra

- `internal/certificate` — `GeneratePDF` pulang bait bermula `%PDF`, muat
  QR, dan **menolak** nama yang tidak boleh dikodkan.
- Kelayakan sijil sebagai fungsi tulen `(hadir, jumlah_sesi, ambang) →
  layak?`. Kes sempadan: 2/3 pada ambang 66 (lulus) vs 67 (gagal), sifar
  sesi, ambang 100.
- Pengiraan tetingkap masa check-in.

### Lawan Postgres sebenar — `ACTIVITY_TEST_DB`

Ikut corak `HANDLER_TEST_DB`, di-skip secara lalai, guna DB buangan.

- **Perlumbaan kapasiti** — N goroutine mendaftar serentak untuk 1 slot
  terakhir; tepat satu berjaya. Tanpa ujian ini, `select ... for update`
  hanya niat baik.
- **Invarian `starts_at`/`ends_at`** — `PUT` sesi, sahkan ringkasan sepadan
  min/maks. Termasuk membuang sesi paling awal dan menambah sesi lebih awal.
- **Unik separa** — daftar → batal → daftar semula berjaya; daftar dua kali
  gagal.
- **Idempoten penerbitan** — panggil endpoint dua kali, bilangan sijil tidak
  berubah, tiada nombor siri terbazir.
- **Sambungan semula fasa 2** — sijil dengan `r2_key` null, panggil endpoint,
  hanya baris itu diisi.
- **Nombor siri berundur** — paksa transaksi gagal, `sequences.current_value`
  tidak melompat.
- **Kebocoran halaman pengesahan** — penegasan atas medan respons bahawa
  emel, `user_id`, dan status keahlian TIDAK hadir. Ditulis sebagai
  penegasan medan supaya menambah medan pada masa depan memecahkan ujian.
  Ini satu-satunya cara semakan privasi bertahan lebih lama daripada niat.
- **Sijil ditarik** — pengesahan masih pulang baris berstatus ditarik; token
  tidak wujud dan token salah dua-dua `404` yang sama.
- **Kebenaran** — ahli biasa ditolak pada **setiap** endpoint management.
- **Tetingkap masa** — check-in ditolak di luar tetingkap; pindaan yang
  dibenarkan meninggalkan catatan audit.

### Lawan R2 sebenar — `R2_LIVE_TEST=1`

- PDF sijil naik dan boleh diambil semula melalui presigned URL.
- Sijil ditarik → kunci masuk `deleted_uploads`; reaper memadam objek.

### Flutter

- Ujian widget: skrin detail dalam keadaan penuh, pendaftaran ditutup, sudah
  daftar, dibatalkan.
- Empat keadaan kegagalan scanner dipetakan kepada mesej berbeza.
- `flutter build apk` lulus selepas `mobile_scanner` ditambah, sebelum
  sebarang skrin scanner ditulis.

### Jurang yang diketahui (manual sahaja)

Penampilan visual PDF, kamera sebenar mengimbas QR sebenar, aliran push.
