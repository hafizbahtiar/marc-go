# Staff Number Verification - Design Spec

**Status:** Draft v2 - belum diimplementasi. Ditulis 2026-09-02, disemak
oleh Opus (spec/plan review terhadap kod sebenar) sama hari - v1 ada
beberapa bug reka bentuk kritikal (route/ID salah, gate yuran boleh
dipintas, `member_id` nullable pecahkan >20 tapak kod). Semua dibetulkan
di bawah. Klarifikasi tambahan pemilik produk (aliran penuh + keperluan
pembetulan nombor staff) turut digabung.

## Keperluan asal (pemilik produk)

1. Tambah nombor staff, mandatory untuk register. Ahli sedia ada, isi
   dengan user id.
2. Admin/manager (kecuali tester - jangan bagi hak management apa pun)
   dan ke atas boleh verify staff id/nombor staff.
3. Lepas register, boleh terus guna app tanpa buat yuran JIKA staff id
   disahkan (verified).
4. Buat payment page: sejarah bayaran, bayaran tertunggak (cth belum
   bayar yuran pendaftaran).
5. Ada aktiviti berbayar - boleh join bila dah buat yuran sahaja. (AKAN
   DATANG - bukan skop sesi ni, backend gateway aktiviti dah wujud sejak
   Stage aktiviti, tiada kerja baharu diperlukan untuk item ni sekarang.)
6. Ahli yang belum verify staff tak boleh buat apa-apa - termasuk tak
   dapat nombor ahli.

**Klarifikasi susulan (Malay, direkod verbatim):**

> user register -> tunggu admin verify -> user dapat nombor ahli ->
> boleh guna app -> tapi untuk join aktiviti perlu bayar yuran first ->
> akan datang aktiviti ada bayaran, so user yang belum bayar yuran
> perlu bayar dahulu

Ni SAHKAN aliran yang dah direka: lepas verify + dapat member_id, ahli
boleh guna app PENUH (feed, profile, dll) tanpa perlu bayar yuran
PENDAFTARAN. "Bayar yuran dulu" yang disebut di sini merujuk kepada
**yuran AKTIVITI** (`activities.fee_cents`), bukan yuran pendaftaran -
gate tu MEMANG dah wujud & berfungsi sejak Stage aktiviti
(`ToyyibPayGateway` instance kedua, `marc_go/TODO.md` bahagian "Yuran
aktiviti - DIBINA DAN DIWIRING"), tak berkaitan/tak terjejas oleh ciri
ni langsung. Tiada kerja baharu diperlukan untuk bahagian ni.

> existing user yang dah bypass bayaran -> kira dah verify staff id, so
> buat masa ni untuk existing user isi ja user id table tu. nanti
> admin/superadmin boleh tukarkan direct staff id.

Ni tambah keperluan baharu: **kebolehan admin/superadmin (rank >= 80,
BUKAN manager rank 60) membetulkan `staff_id` bila-bila masa**,
termasuk pada profile yang SUDAH verified - bukan cuma semasa verify
pertama kali. Lihat endpoint baharu `PATCH /members/:id/staff-id`
di bawah.

## Keputusan disahkan (Q&A 2026-09-02)

- **Q1 - bila nombor ahli (`member_id`) dijana:** DITANGGUHKAN sampai
  staff id disahkan. `member_id` kini dijana di `POST /auth/register`
  (`generateMemberID`, `internal/http/handlers/auth.go:268-288`).
  Selepas ciri ni, `member_id` mula sebagai `NULL`, dijana pertama kali
  semasa `VerifyStaffID` berjaya. **Sequence bulan** yang dipakai
  ialah bulan SEMASA VERIFY (bukan bulan daftar) - `generateMemberID`
  dipanggil masa verify berlaku, ia guna `time.Now()`. Kesan: ahli
  daftar akhir September, diverify Oktober, dapat `MARC2026/10/...`
  bukan `/09/`. Ini KEPUTUSAN (bukan gap) - member_id memang bermaksud
  "bila rasmi jadi ahli berdaftar", bukan "bila mula daftar akaun".
- **Q2 - hubungan dgn kelulusan (`pending`→`approved`):** Staff verified
  jadi **syarat WAJIB** sebelum `ApproveMember` boleh lulus.
  `setMemberStatus` (profile.go) gagal 400 kalau cuba approve ahli yang
  `staff_id_verified_at IS NULL`.
- **Q3 - hubungan dgn yuran pendaftaran (ToyyibPay):** Staff verified =
  exemption yuran BAHARU, **OR** dengan gate sedia ada
  (`HasSucceededRegistrationPayment`).
- **Q4 - skop sesi ni:** Staff number + verifikasi + exemption yuran +
  keupayaan pembetulan nombor staff (admin/superadmin) SAHAJA. Payment
  page "tertunggak" (outstanding) turut dimasukkan sebagai enhancement
  kecil pada `/me/payments` sedia ada. Aktiviti berbayar wajib-bayar-dulu
  KEKAL macam sedia ada (dah dibina, tak disentuh).

## ⚠️ Kesan logik yang perlu disedari (bukan bug, keputusan reka bentuk)

Sebab staff ID **mandatory untuk SEMUA pendaftaran**, dan staff
verified **WAJIB** sebelum approve DAN staff verified **exempt** yuran
sepenuhnya - laluan bayar ToyyibPay pendaftaran jadi tak relevan untuk
SEMUA ahli baharu selepas ciri ni siap. Ini konsisten dengan keperluan
#3 pemilik produk - direkodkan supaya sedar akibat, bukan halangan.

`internal/payment/toyyibpay.go` dan route `/registration-payments/*`
**TAK dibuang** - laluan bayar kekal wujud dalam kod, tapi
**PENGESAHAN SEMASA SEMAKAN v2**: sebab verifikasi staff kini SYARAT
WAJIB sebelum SEBARANG kelulusan (gate 400 di Task 8 Step 3 jalan
SEBELUM blok yuran), `staffExempt` sentiasa `true` pada titik blok
yuran dijalankan - cawangan `else` (yang mengandungi
`HasSucceededRegistrationPayment`, gate rank admin utk
`BypassPayment`, semakan `bypass_reason`, `HasPendingRegistrationPayment`)
jadi **kod mati** buat masa ni (bukan laluan "pengecualian pembayaran
utk ahli tak diverify" macam draf sebelum ni salah anggap - laluan tu
TAK WUJUD lagi sebab tak boleh sampai ke situ langsung tanpa verified).
**Ini SENGAJA dikekalkan** (bukan dibuang) sebagai titik lanjutan kod
utk kemungkinan jenis "ahli am" (bukan staff) pada masa depan yg
disebut dlm bahagian "Kesan logik" - implementer Task 8 WAJIB faham
cawangan tu kod mati HARI NI, ujian regresi untuknya (kalau ditulis)
mesti diakui sebagai "sedia utk masa depan", bukan "menguji gelagat
semasa". **PENTING**: `BypassPayment` MESTI kekal tak boleh
mempengaruhi audit bila `staffExempt` true - lihat bahagian "Gate
yuran - butiran KRITIKAL" di bawah, ini punca bug paling serius yang
ditemui semasa semakan v1 (disahkan DIBAIKI betul semasa semakan v2 -
`req.BypassPayment` diset `false` SEBELUM audit dibaca, dalam
`setMemberStatus` yg sama, `req` dihantar by-value).

## Skema data (migration baharu)

```sql
alter table profiles
  add column staff_id text,
  add column staff_id_verified_at timestamptz,
  add column staff_id_verified_by uuid references users(id);

alter table profiles
  alter column member_id drop not null;

-- BACKFILL: HANYA ahli status='approved' yang auto-verified (mereka
-- betul-betul dah approved & guna app sebelum ciri ni, tiada sesiapa
-- perlu "verify" retroaktif). Ahli 'pending'/'rejected' sedia ada
-- MESTI TIDAK auto-exempt - mereka belum pernah lalui verifikasi
-- sebenar, dan sesetengah mungkin masih berhutang yuran. Kalau baris
-- ni auto-verified, mereka terus exempt yuran + boleh diluluskan
-- serta-merta tanpa verifikasi sebenar - lubang keselamatan/kewangan.
update profiles
set staff_id = user_id::text,
    staff_id_verified_at = case when status = 'approved' then now() else null end,
    staff_id_verified_by = null
where staff_id is null;

alter table profiles
  alter column staff_id set not null;

create unique index profiles_staff_id_key on profiles (staff_id);
```

**Panjang medan**: `staff_id` had panjang **64 aksara** (semakan
aplikasi, bukan `varchar(64)` DB - padan pola validasi `display_name`
sedia ada) - elak rentetan sangat panjang membengkakkan index unique
tanpa sebab (mustahil nombor staff organisasi sebenar >64 aksara).

**Down migration**: `alter column member_id set not null` di laluan
`Down` akan GAGAL kalau ada baris `member_id IS NULL` semasa itu (ahli
pending yang belum diverify). Migration `Down` untuk ciri ni **sengaja
tidak** cuba pulihkan NOT NULL tu - kalau perlu rollback, backfill
`member_id` palsu dahulu secara manual sebelum jalankan Down (dicatat
sebagai amaran dalam komen migration, bukan diautomasikan).

**Kenapa unique index**: dua ahli tak patut kongsi nombor staff
organisasi yang sama. Backfill guna `user_id::text` (UUID, unique per
definisi), jadi tiada konflik migration.

## Perubahan API

### `POST /auth/register` - tambah medan wajib

Request tambah `staff_id` (string, wajib, tak kosong selepas trim,
maksimum 64 aksara). `CreateProfile` (auth.go:280) tak lagi panggil
`generateMemberID` - `member_id` dihantar sebagai `NULL`. Ralat 400
kalau `staff_id` kosong/>64 aksara.

**Konflik unique (staff_id dah digunakan ahli lain)**: `CreateUser`
sedia ada dah handle `isUniqueViolation` (utk email) - `CreateProfile`
kini PERLU laluan sama: tangkap unique-violation pada `staff_id`,
pulang **409** "nombor staff ini sudah didaftarkan" (BUKAN 500 generik
sedia ada). Ini elak serangan "staff-id squatting" (seseorang
daftar guna nombor staff org rakan sekerja dia utk sekat rakan sekerja
tu daftar kemudian) jatuh senyap sebagai 500 yang mengelirukan - 409
sekurang-kurangnya beri isyarat jelas kpd mangsa sebenar utk hubungi
management terus (management ada endpoint pembetulan di bawah utk
selesaikan kes squatting - tukar nombor staff pada akaun yang salah).

**TIADA parsing/validasi format** pada `staff_id` (rentetan legap,
organisasi yang tentukan format, bukan app).

**Kesan `member_id` NULL pada laluan lain (WAJIB disemak semasa
implementasi, bukan pilihan)**: `member_id` tukar dari `string` (sqlc)
kepada `pgtype.Text` (nullable) - ini pecahkan sebarang kod yang
mengandaikan ia sentiasa diisi. Sekurang-kurangnya SATU kes SEBENAR
(bukan hipotesis) ditemui semasa semakan: `registration_payment.go`
(sekitar baris 125) guna `MemberID` sebagai fallback `billTo`/reference
ToyyibPay dgn komen sedia ada "tak pernah kosong dua-dua sebab
member_id sentiasa diisi semasa daftar" - andaian tu jadi PALSU
selepas ciri ni. Fallback rantai kena jadi: `display_name` →
`member_id` (kalau ada) → **`email`** (baharu, sentiasa wujud) - elak
`billTo` kosong dihantar ke ToyyibPay (`createBill` pulang ralat
`"billTo parameter is empty"` utk ahli belum verified yg cuba bayar
derma/yuran aktiviti sebelum staff diverify). Implementer WAJIB grep
`\.MemberID\b` merentas repo (bukan cuma fail generated) dan semak
setiap tapak - jangan andai sqlc regenerate cukup.

### `POST /members/:id/verify-staff-id` (baharu)

**PENTING - laluan URL & makna `:id`**: TIADA prefix `/admin` (grup
`approved` sedia ada TIADA prefix tu - lihat `router.go:192`,
`/members/:id/approve` bukan `/admin/members/:id/approve`). `:id`
ialah **`user_id`** (BUKAN `profiles.id` PK) - padan 100% dgn pola
`setMemberStatus` sedia ada yang buat
`h.queries.GetProfileByUserID(ctx, targetID)`. `GetProfileByID` (by
profile PK) **TIDAK WUJUD** dalam codebase ni - jangan cipta query
baharu yang menyimpang drpd pola sedia ada, guna `GetProfileByUserID`.

Route: `approved.POST("/members/:id/verify-staff-id", ...)`, gate
DALAM handler (padan pola `setMemberStatus`):
`authz.IsAtLeastRole(caller, "manager")` - rank **>= 60**. `supervisor`
(rank 50) **TAK dapat** hak ni. `tester` (rank 5, category `ahli`)
tertolak automatik.

**Self-lockout guard** (defense-in-depth, padan
`setMemberStatus` profile.go:1205): tolak 400 kalau `targetID ==
caller.UserID`, walau secara teori tak reachable hari ni (caller rank
>=60 dah mesti verified sendiri utk approved status yang route perlukan)
- satu baris kod, tutup keseluruhan kelas risiko kalau gate `approved`
kelak dilonggarkan.

**Hanya profile `status='pending'` boleh diverify**: 409/400 kalau
target `status == 'rejected'` (elak mint `member_id` utk ahli yang dah
ditolak) atau `status == 'approved'` (dah verified secara struktur
sejak jadi syarat approve - request ni jadi idempoten no-op automatik
utk kes ni, bukan ralat).

Request body pilihan: `{"staff_id": "..."}` - benarkan management
betulkan salah taip semasa VERIFY PERTAMA sahaja (bukan pembetulan
selepas verified - guna endpoint `PATCH` di bawah utk tu). Konflik
unique pada override → **409** (sama pola dgn register).

Logik (`ProfileHandler.VerifyStaffID`, profile.go), **SEMUA dalam
SATU transaksi DB** (padan pola `setMemberStatus` guna `pool.Begin`,
BUKAN panggilan `:one` bertelanjang di luar tx - ni penting utk
`generateMemberID`/`NextSequence` tak "leak" sequence number bila
transaksi verify gagal/rollback selepas sequence diambil):

1. Gate rank (403 kalau caller rank < 60).
2. Self-lockout (400 kalau target == caller).
3. Load target via `GetProfileByUserID` (404 kalau tiada).
4. Kalau `status == 'rejected'` → 409 "ahli ni dah ditolak".
5. **Idempoten**: kalau `staff_id_verified_at` SUDAH diisi, pulang
   200 dgn state sedia ada terus (tiada tulis DB).
6. Buka transaksi. Kalau `member_id IS NULL`, panggil `generateMemberID`
   (fungsi SEDIA ADA, `auth.go:329`, reuse - guna `qtx` dalam tx yg
   sama, padan pola `activity_certificates.go:207-224` yang sengaja
   generate ID dalam tx yg sama utk elak leak sequence).
7. `UPDATE profiles SET staff_id=coalesce(override,staff_id),
   staff_id_verified_at=now(), staff_id_verified_by=caller,
   member_id=coalesce(member_id, generated) WHERE user_id=$1 AND
   staff_id_verified_at IS NULL RETURNING *` - kalau 0 rows
   affected (race: orang lain verify dulu antara langkah 5 dan sini),
   `ROLLBACK` transaksi (buang sequence yg terlanjur diambil - accept
   nombor "berlubang" dlm kes race yg jarang berlaku, LEBIH BAIK drpd
   assign dua member_id pada satu profile), baca semula profile
   TERKINI (guna `qtx` yg SAMA sebelum rollback, atau connection baharu
   selepas), pulang 200 dgn state SEBENAR (bukan zero-value - JANGAN
   abaikan ralat baca-semula ni, log kalau gagal, pulang 500 kalau
   baca-semula pun gagal, JANGAN pulang `member_id:""` senyap).
8. Commit. Audit log (`audit.EntityStaffIDVerification`).
9. **TIADA rate limiter baharu** - padan pola `ApproveMember`.

### `PATCH /members/:id/staff-id` (baharu - keperluan susulan)

Pembetulan `staff_id` BILA-BILA MASA (verified atau tidak) -
keperluan eksplisit pemilik produk: ahli sedia ada dibackfill dgn
`staff_id = user_id` sebagai placeholder, admin/superadmin perlu
boleh tukar ke nombor staff SEBENAR bila client sediakan data tu
kemudian. **BEZA drpd verify**: gate rank **>= 80** (admin/superadmin
SAHAJA, BUKAN manager - lebih ketat drpd verify sebab ni "override"
data yg boleh dah verified, bukan tindakan kelulusan pertama).

Route: `approved.PATCH("/members/:id/staff-id", ...)`, `:id` =
`user_id` (sama pola). Body: `{"staff_id": "..."}` wajib, tak
kosong selepas trim, <=64 aksara.

Logik:
1. Gate rank >= 80 (403 selainnya).
2. Load target via `GetProfileByUserID` (404 kalau tiada).
3. `UPDATE profiles SET staff_id=$1 WHERE user_id=$2` - **TIDAK
   sentuh** `staff_id_verified_at`/`staff_id_verified_by`/
   `member_id` (pembetulan nilai, bukan verifikasi semula - profile
   yg dah verified KEKAL verified, member_id yg dah dijana KEKAL sama).
4. Konflik unique → 409.
5. Audit log (`audit.EntityStaffIDCorrection`, jenis baharu -
   BEZA drpd `EntityStaffIDVerification`, supaya jejak audit boleh
   bezakan "verify pertama kali" drpd "pembetulan kemudian").

### `setMemberStatus` (profile.go) - gate baharu SEBELUM gate yuran

Tambah semakan SEBELUM blok `HasSucceededRegistrationPayment` sedia ada
(profile.go:1249): kalau `status == "approved"` dan
`target.StaffIDVerifiedAt` tidak `.Valid` → **400** "nombor staff
ahli ni belum disahkan - sahkan nombor staff dulu sebelum meluluskan".
**TIADA bypass** untuk gate ni.

#### Gate yuran - butiran KRITIKAL (bug ditemui semasa semakan Opus v1)

Blok yuran sedia ada (profile.go ~1249-1305) BUKAN cuma "satu semakan
`if !paid`" - ia empat perkara bersambung: (a) normalisasi
`paid → req.BypassPayment = false`, (b) gate rank admin utk
`BypassPayment`, (c) keperluan `BypassReason` tak kosong, (d) semakan
`HasPendingRegistrationPayment` (elak bayaran berganda). Reka bentuk
v1 yang cuma "skip WHOLE block kalau staffExempt" SILAP - ia turut
skip (b)/(c) SEDANGKAN blok audit SELEPAS transaksi (yang rekod
`bypass_payment`/`bypass_reason`) TIDAK di-skip, jadi:

**Exploit v1 (DIBAIKI oleh reka bentuk v2 di bawah)**: manager (rank
60, TAK dibenarkan bypass yuran - itu gate rank admin/80) POST
`/members/:id/approve` dgn `{"bypass_payment": true}` tanpa
`bypass_reason`. Ahli tu MEMANG staff-exempt (verified), jadi approve
akan 200 macam mana pun - tapi audit log akan rekod SEOLAH-OLAH manager
tu guna kuasa bypass admin yang dia sebenarnya tiada, dgn alasan kosong.
Rekod audit palsu/mengelirukan.

**Reka bentuk v2 (betul)**:

```go
// Tentukan staffExempt DULU, SEBELUM sebarang logik bypass/paid.
staffExempt := target.StaffIDVerifiedAt.Valid

if staffExempt {
    // Exempt sepenuhnya. PAKSA bypass flag ke false eksplisit -
    // walau caller hantar bypass_payment=true, JANGAN biar ia sampai
    // ke logik/audit bypass di bawah. Tiada apa "dibypass" sebenarnya
    // sebab exemption datang drpd staff verified, bukan drpd kuasa
    // admin - audit MESTI tak rekod bypass_payment=true dlm kes ni.
    req.BypassPayment = false
} else {
    // ...KOD SEDIA ADA TAK BERUBAH LANGSUNG (a)-(d)...
    // (b)/(c)/(d) HANYA jalan bila staffExempt FALSE - gate admin
    // rank & bypass_reason kekal berkuat kuasa PENUH utk ahli yg
    // BUKAN staff-exempt.
}
```

**`HasPendingRegistrationPayment` (d) MESTI kekal semak walau
`staffExempt`** - kes tepi: ahli ada bil ToyyibPay pending (dari
sebelum staff diverify, cth dia cuba bayar dulu sementara tunggu), lalu
staff diverify, lalu management approve. Kalau semakan (d) di-skip
sepenuhnya bila staffExempt, ahli tu diluluskan SEDANGKAN ada bil
pending yang mungkin lepas ni tiba-tiba `succeeded` (webhook lewat) -
duit masuk tanpa refund path yang jelas (ahli dah exempt, tak perlu
duit tu, tapi webhook akan cuba proses `succeeded` pada
`registration_payments` yang dah tak relevan). **Keputusan**: biarkan
webhook tetap proses macam biasa (rekod kekal utk audit kewangan),
tapi TAMBAH semakan (d) sebagai AMARAN (log warning, bukan block) bila
staffExempt DAN ada bil pending - staff kena tahu utk refund manual
kalau bil tu lepas ni `succeeded`. TIADA block keras di sini (jangan
sekat approval staff-exempt semata sebab ada bil pending yg IRRELEVANT
kepada exemption dia).

### `GET /me/payments` (payments.go) - tambah ringkasan "tertunggak"

**Pembetulan penting drpd v1**: handler `Mine` (payments.go:282) TIDAK
ada local var `profile` sedia ada - ia cuma fetch
`regRows`/`actRows`/`donRows`. Implementasi WAJIB tambah SATU panggilan
`h.queries.GetProfileByUserID(ctx, callerID)` (query yg SUDAH wujud,
dipakai di tempat lain) - ini SATU query tambahan, bukan "tiada query
tambahan langsung" macam draf v1 silap kata.

Tambah `outstanding_registration_fee: bool` pada respons sedia ada -
`true` bila `!profile.StaffIDVerifiedAt.Valid` DAN tiada baris
`registration_fee[]` (yg dah difetch) berstatus `succeeded`.

Flutter (`payment_history_page.dart`): banner "Yuran pendaftaran belum
dibayar" di ATAS senarai `registration_fee[]` bila
`outstanding_registration_fee == true`, dengan butang terus ke
`CheckoutPage` sedia ada.

## Paparan `staff_id` pada UI pengurusan (gap v1)

`GET /members` (senarai pending, `?status=pending`) dan/atau
`GET /members/:id` (butiran satu ahli - semak nama fungsi sebenar di
`profile.go`) WAJIB tambah `staff_id` + `staff_id_verified_at`
pada respons JSON - management perlu NAMPAK nombor staff SEBELUM boleh
verify (skrin pending-members Flutter, Task Flutter berkaitan,
memerlukan medan ni). v1 lupa sebut perubahan respons ni langsung
walau UI-nya (Task Flutter) mengandaikan medan tu wujud.

## Ralat & kes tepi

- **Register tanpa `staff_id` / >64 aksara** → 400.
- **Register dgn `staff_id` yg dah digunakan ahli lain** → 409
  (BUKAN 500).
- **`:id` malformed (bukan UUID sah)** pada verify/patch → 400 (padan
  pola sedia ada `uuid.Parse` gagal di handler lain), BUKAN 404.
- **Verify staff ID pada profile yang SUDAH verified** → 200
  idempoten.
- **Verify staff ID pada profile `status='rejected'`** → 409.
- **Verify staff ID oleh caller rank < 60** → 403.
- **Verify staff ID pada `user_id` yang TAK WUJUD** → 404.
- **Verify staff ID pada DIRI SENDIRI** → 400 (self-lockout guard).
- **Approve ahli yang staff belum verified** → 400, TIADA bypass.
- **Manager (bukan admin) hantar `bypass_payment=true` pada ahli
  staff-exempt** → approve tetap 200 (sebab exempt), tapi
  `req.BypassPayment` DIPAKSA `false` sebelum audit - TIADA rekod audit
  palsu "bypass digunakan".
- **Dua verify concurrent pada profile SAMA** → SATU berjaya (row
  affected), yang lain rollback + baca-semula + pulang state SEBENAR
  (bukan zero-value).
- **Race verify vs approve serentak**: approve baca
  `staff_id_verified_at` sebelum tulis status - kalau verify belum
  commit, approve gagal 400 (selamat, boleh cuba lagi).
- **`PATCH staff-id` pada `staff_id` yang bercanggah dgn ahli
  lain** → 409.
- **Nombor staff pelupusan/spam**: DI LUAR skop teknikal - proses
  verifikasi MANUAL, app tak cuba sahkan kewujudan sebenar.

## Isu keselamatan yang disemak Opus (2026-09-02) - status selepas v2

1. **IDOR/self-verify** - self-lockout guard ditambah v2 (defense-in-
   depth, walau unreachable hari ni bawah gate `approved` sedia ada).
2. **Downgrade role lepas verify** - TIADA isu, padan pola
   `approved_by` sedia ada (tiada semakan retroaktif).
3. **`member_id` generation race** - SELAMAT, `NextSequence` atomic;
   race SAMA-PROFILE dikendalikan via `WHERE staff_id_verified_at
   IS NULL` + rollback (v2, betulkan leak sequence v1).
4. **Exemption logic tak boleh dipintas request body** - DIBAIKI v2:
   `staffExempt` ditentukan DULU drpd DB, `req.BypassPayment` DIPAKSA
   `false` bila staffExempt - gate rank admin & bypass_reason kekal
   berkuat kuasa PENUH bila TIDAK staffExempt.
5. **Payment checkout lepas staff verified** - bukan bug, direkod;
   UI (Task Flutter) perlu papar status exempt dgn jelas.
