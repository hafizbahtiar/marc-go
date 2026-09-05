# Format Nombor Ahli Baharu - Design Spec

**Status:** Draft - belum diimplementasi. Ditulis 2026-09-03, susulan
terus drpd ciri [[2026-09-02-staff-id-verification-design]] (backend
Task 1-9 siap, staged, belum commit). Ciri ni HANYA ubah CARA `member_id`
DIJANA (`generateMemberID`) - tiada perubahan skema DB (`member_id`
sudah nullable + unique index sedia ada drpd ciri staff-id).

## Keperluan asal (pemilik produk)

Format lama: `MARC{YYYY}/{MM}/{seq:04d}` (cth `MARC2026/09/0001`).
Format baharu yang diminta: `MARC-{staff_id}/{tahun}-{seq}` (contoh
diberi: `MARC-0110/2014-8`).

1. Ahli sedia ada (`member_id` yang DAH dijana) - **biarkan**, jangan
   regenerate/reformat retroaktif. Admin/superadmin boleh **direct
   edit** member_id bila-bila (pembetulan manual).
2. Cater race condition - dua verifikasi serentak tak boleh dapat
   `member_id` sama, "standard" (padan corak sedia ada).
3. Sequence ahli biasa: **4 digit zero-padded**, cth `0001` (contoh asal
   pemilik produk `-8` cuma ilustrasi ringkas, `0001` diesahkan sbg
   format sebenar via Q&A).
4. `superadmin` dan `tester` **tak perlu sequence 4-digit** - guna kod
   huruf: `SA` (superadmin), `T` (tester). Role masa depan "penaung"
   akan guna `P`.

## Keputusan disahkan (Q&A 2026-09-03)

- **Q1 - bila kod SA/T dijana:** HANYA bila `member_id` MASIH `NULL`
  pada titik ia perlu dijana (iaitu semasa `VerifyStaffID` berjaya).
  Kalau ahli dah ada `member_id` numerik sedia ada (kes biasa: daftar
  ahli biasa → diverify → dapat numerik → **kemudian** role ditukar ke
  superadmin/tester via skrin "Tukar Role" sedia ada), `member_id`
  numerik tu **KEKAL**, TIADA regenerasi bila role ditukar. Ini padan
  terus keperluan #1 "yang existing, biarkan" - member_id cuma pernah
  ditulis SEKALI (dalam `VerifyStaffID`), tiada laluan tulis kedua
  drpd tukar role.

  **Kesan praktikal**: untuk akaun superadmin/tester dapat member_id
  format istimewa, role kena ditukar SEBELUM `VerifyStaffID` dipanggil
  (cth: daftar akaun uji → tukar role ke `tester` semasa masih
  `pending`/belum verified → THEN verify staff id → dapat `MARC-.../T1`).
  Kalau proses biasa organisasi ialah "daftar → approve → baru tukar
  role", member_id istimewa SA/T ni jarang/tak pernah tercetus secara
  praktikal - itu OK, ia laluan sedia untuk kes yang perlu, bukan
  jaminan setiap superadmin/tester akan ada kod istimewa.

- **Q2 - keunikan kod T/SA bila lebih drpd satu akaun:**
  - `tester`: **PERLU nombor tambahan** - `T1`, `T2`, `T3`, ... (sequence
    atomic berasingan drpd sequence ahli biasa, key `member_seq:tester`).
  - `superadmin`: **TIADA nombor** - literal `SA` sahaja. Keputusan
    produk: "superadmin boleh ada 1 dalam 1 masa" - ni **polisi
    organisasi, BUKAN dikuatkuasakan kod**. Kalau (secara luar jangka)
    wujud >1 superadmin serentak, kedua-duanya papar `MARC-{staff_id}/
    {tahun}-SA` - member_id KESELURUHAN tetap unique (staff_id tiap
    akaun tetap unique, `profiles.member_id` unique index sedia ada
    tetap berkuat kuasa), cuma bahagian "SA" kelihatan sama secara
    visual. Ini DITERIMA sengaja - tiada semakan "sedia ada superadmin
    lain?" ditambah, elak kerumitan/race tambahan utk kes yang jarang
    berlaku & bukan risiko keselamatan/kewangan.
  - `penaung` (role masa depan, BELUM wujud dlm `roles`): direka
    SAMA pola dgn `tester` - `P1`, `P2`, ... (bukan literal tunggal
    macam superadmin), sequence key `member_seq:penaung` - TIADA kod
    ditulis utk role ni sekarang (role tak wujud), cuma direkod di sini
    supaya bila role tu dicipta kelak, penambahan cuma SATU entri baris
    dlm peta kod (lihat "Reka bentuk" di bawah), bukan reka semula.

- **Q3 - skop sequence ahli biasa:** **GLOBAL, TAK PERNAH RESET**.
  Berbeza drpd sistem lama (`auth:{tahun}:{bulan}`, reset tiap bulan) -
  satu counter berterusan merentas semua tahun, key tetap
  `member_seq:ahli`. `{tahun}` dlm format cuma LABEL bila member_id
  dijana (konsisten dgn keputusan `staff-id-verification-design.md`
  Q1 - "bila rasmi jadi ahli", bukan skop counter).

  **Andaian TAMBAHAN (belum ditanya eksplisit, disyorkan konsisten)**:
  sequence `tester`/`penaung` turut GLOBAL tak reset (sama sebab dgn
  ahli) - tiada sebab dibezakan. **Tanda untuk semakan Opus/pemilik
  produk** kalau ini silap andaian.

## Reka bentuk

### Format akhir

```
MARC-{staff_id}/{tahun}-{kod}
```

- `staff_id`: nilai **EFEKTIF** yang ditulis ke DB semasa panggilan
  `VerifyStaffID` yang SAMA - iaitu `override` (kalau caller hantar
  pembetulan serentak dgn verify) atau `target.StaffID` sedia ada
  (kalau tiada override). **Keputusan (ditambah semasa semakan Opus -
  draf awal cuma sebut "staff_id ahli tu sendiri" tanpa nyatakan kes
  override eksplisit)**: guna nilai EFEKTIF, BUKAN `target.StaffID`
  yang mungkin dah lapuk - kalau override diberi tapi member_id
  dijana drpd `target.StaffID` lama, member_id akan bawa staff_id
  yang tak lagi padan dgn apa yang sebenarnya ditulis ke profil. Diselit
  **VERBATIM, TIADA transformasi/uppercase/padding** - apa staff_id tu,
  itu yang masuk.

  ⚠️ **Kes tepi (defense-in-depth, BUKAN bug production yang boleh
  dicapai hari ini - disahkan semasa semakan Opus pusingan 2)**: draf
  awal spec ni dakwa ahli backfill boleh diverify dgn staff_id
  placeholder (`user_id`) sedangkan `member_id` masih NULL, jadi UUID
  penuh akan terselit permanent dlm member_id. **Dakwaan ni SALAH**:
  SEMUA baris yang wujud SEBELUM migration ciri staff-id-verification
  (`20260902100000_add_staff_id.sql`) MEMANG dah ada `member_id` SEDIA
  ADA (format lama) - flow pendaftaran LAMA jana `member_id` time
  daftar utk SEMUA ahli (pending atau approved sekalipun), sebelum
  ciri staff-id-verification wujud langsung. `member_id` cuma mula
  `NULL` utk pendaftaran BAHARU SELEPAS migration tu - dan pendaftaran
  baharu MESTI isi `staff_id` sebenar (wajib, tak boleh kosong) time
  daftar, jadi kombinasi "staff_id == user_id sendiri DAN member_id
  masih NULL" **TAK BOLEH berlaku** drpd sebarang laluan
  migration/pendaftaran sebenar hari ini.

  **Keputusan**: KEKALKAN satu semakan pertahanan-berlapis (defense-
  in-depth) dlm `VerifyStaffID` - tolak (400, "staff_id ahli ni masih
  placeholder - sila isi nombor staff sebenar semasa sahkan") kalau
  nilai EFEKTIF staff_id (override atau `target.StaffID`) SAMA PERSIS
  dgn `target.UserID.String()` DAN tiada override diberi. Ini kekal
  berbaloi (satu baris kod, sifar kos) sbg jaring keselamatan kalau
  migration data masa depan/edit manual DB tersilap wujudkan kombinasi
  ni semula - tapi JANGAN anggap/dokumenkan ni sbg pembetulan bug hidup
  semasa, sebab ia BUKAN. Paksa management isi override sebenar utk kes
  hipotesis ni sahaja,
  bukan sekatan umum. Ahli yang staff_id dia MEMANG kebetulan sama
  drpd user_id (secara teori mustahil scr rawak, UUID vs nombor staff
  organisasi) tak akan pernah terjejas sebenar.
- `tahun`: tahun SEMASA (`time.Now()` zon Asia/Kuala_Lumpur, padan
  `generateMemberID` sedia ada) - bila member_id DIJANA, bukan bila
  akaun didaftar.
- `kod`: bergantung role ahli PADA TITIK GENERATE (lihat peta di bawah).

### Peta kod ikut role

| Role key      | Kod                          | Sequence key          | Contoh                    |
|---------------|-------------------------------|------------------------|---------------------------|
| `superadmin`  | `SA` (literal, tiada nombor)  | *(tiada - tak dilukis)*| `MARC-0110/2026-SA`       |
| `tester`      | `T{n}`                        | `member_seq:tester`    | `MARC-0110/2026-T1`       |
| `penaung`*    | `P{n}` *(role belum wujud)*   | `member_seq:penaung`   | `MARC-0110/2026-P1`       |
| lain-lain     | `{seq:04d}` (4 digit)         | `member_seq:ahli`      | `MARC-0110/2026-0001`     |

*"lain-lain"* meliputi `ahli`, `supervisor`, `manager`, `admin` -
SEMUA role selain superadmin/tester/penaung guna laluan numerik biasa
(mereka semua "ahli berdaftar" pada asasnya, cuma beza kuasa
pentadbiran - tiada sebab member_id mereka berbeza drpd ahli biasa).

Reka bentuk kod: satu `map[string]memberIDCodeStrategy` (atau `switch`
kecil) dlm `generateMemberID` - tambah role baharu (`penaung`) kelak
cuma SATU entri baris, bukan cabang baharu dlm handler caller.

### Fungsi `generateMemberID` - tandatangan baharu

```go
// generateMemberID(ctx, q, staffID, roleKey string) (string, error)
```

Tambah dua parameter (`staffID`, `roleKey`) berbanding sedia ada
(`generateMemberID(ctx, q)`). **SATU-SATUNYA caller sedia ada**
ialah `VerifyStaffID` (`profile.go`, ciri staff-id-verification,
staged belum commit) - caller tu SUDAH ada kedua-dua nilai
(`target.StaffID`, `target.RoleKey` atau setara - semak field sebenar
pada `Profile`/join role sedia ada di situ) sebelum panggil fungsi ni,
jadi tiada query tambahan diperlukan di caller.

### Race condition ("standard", padan corak sedia ada)

TIADA mekanisme baharu diperlukan - `NextSequence` (jadual `sequences`,
upsert atomic) SUDAH selamat utk pelbagai key konkurrensi (sedia
digunakan `auth:{tahun}:{bulan}` sebelum ni). Key baharu
(`member_seq:ahli`, `member_seq:tester`, `member_seq:penaung`) ialah
row BERBEZA dlm jadual sama, setiap satu atomic secara berasingan -
tiada race BARU diperkenalkan, cuma key string berbeza drpd sistem
lama. `generateMemberID` KEKAL dipanggil DALAM transaksi `VerifyStaffID`
yang sama (padan reka bentuk sedia ada `staff-id-verification-design.md`
- rollback pada `ErrNoRows` race verify-vs-verify buang sequence yg
terlanjur diambil, sama macam sekarang).

**Nota (disahkan Opus terhadap `NextSequence` sebenar)**: `NextSequence`
guna `INSERT ... ON CONFLICT (key) DO UPDATE ... RETURNING` - di bawah
`READ COMMITTED`, laluan `ON CONFLICT` ambil row lock pada baris `key`
tu, jadi dua `VerifyStaffID` role SAMA yang berlaku SERENTAK akan
bersiri (serialize) pada commit, BUKAN gagal/race sebenar - selamat,
tapi bermakna verifikasi konkurrensi role sama akan beratur menunggu
transaksi terdahulu commit dulu (kesan prestasi, bukan kesan
ketepatan). Pada skala kelab ni (bilangan verifikasi serentak amat
rendah), ini boleh diterima.

### Pembetulan manual (`PATCH /members/:id/member-id`, baharu)

Padan struktur `PATCH /members/:id/staff-id` (ciri staff-id-verification,
Task 6) SEPENUHNYA - rank gate **admin/superadmin (>=80)**, `:id` =
`user_id`.

**Beza penting drpd `staff-id` correction**: endpoint ni **HANYA boleh
edit member_id yang SUDAH wujud** (`member_id IS NOT NULL`) - BUKAN
laluan utk "assign member_id awal" (yang akan pintas gate verifikasi
staff-id sepenuhnya, lubang keselamatan besar - ahli boleh dapat
nombor ahli rasmi tanpa verify staff langsung). Kalau target
`member_id IS NULL`, pulang **409** "ahli ni belum ada nombor ahli -
sahkan nombor staff dulu (`POST /members/:id/verify-staff-id`)".

Body: `{"member_id": "..."}` - wajib, tak kosong selepas trim, had
panjang **128 aksara** (lebih panjang drpd `staff_id` punya 64 sebab
format penuh embed staff_id + label lain). **TIADA validasi format** -
admin boleh tulis apa-apa (padan sengaja dgn `staff_id`, kes pembetulan
manual/legacy tak semestinya ikut format baharu).

Konflik unique (`profiles.member_id` unique index sedia ada) → 409,
**dibezakan** drpd 409 "member_id belum wujud" (dua sebab 409 BERBEZA,
mesej BERBEZA - handler kena semak `pgx.ErrNoRows` DULU sebelum semak
unique-violation, supaya dua kes ni tak bercampur).

Audit: `audit.EntityMemberIDCorrection` (baharu, const string biasa -
`audit.Entity` bukan jenis bertype, ikut corak const sedia ada), rekod
`Old`/`New` sama pola `EntityStaffIDCorrection`.

**⚠️ Gap keselamatan ditemui semasa semakan Opus (terpakai jugak pada
`CorrectStaffID` sedia ada yg staged, BUKAN cuma endpoint baharu ni)**:
tiada sekatan rank/self-target pada `CorrectStaffID` mahupun (draf
awal) `CorrectMemberID` - admin (rank 80) boleh betulkan `staff_id`/
`member_id` **SUPERADMIN** (rank 100) atau AKAUN SENDIRI, tak macam
`VerifyStaffID` yang dah ada self-lockout guard
(`targetID == callerID`, profile.go:1533). **Pembetulan**: KEDUA-DUA
`CorrectStaffID` (patch susulan kpd kod sedia ada) DAN `CorrectMemberID`
(endpoint baharu) kena tambah:
1. Self-lockout: tolak 400 kalau `targetID == callerID` (padan
   `VerifyStaffID`).
2. Sekatan rank: tolak 403 kalau `caller.RoleRank <= target.RoleRank`
   (**`<=`, bukan `<`** - tolak rank SETARA jugak, bukan cuma lebih
   tinggi, padan house style sebenar). **Pembetulan drpd draf awal**:
   exemplar yg betul BUKAN `RejectMember` (tu semakan `RoleCategory`,
   bukan bandingan rank) - corak sebenar ialah `UpdateMemberRole`/
   `UpdateMemberActive` (profile.go, ~baris 856 & 965), yang KEDUA-DUA
   guna mesej tetap `"tidak boleh edit ahli setaraf/lebih tinggi drpd
   anda"`. **PENTING**: `CorrectStaffID` sedia ada TIADA pemboleh ubah
   `caller` (cuma `callerID uuid.UUID` drpd `middleware.UserID(c)`) -
   WAJIB tambah panggilan `h.queries.GetProfileByUserID(ctx, callerID)`
   dulu (padan corak `UpdateMemberRole`), bukan andai `caller` dah
   sedia wujud.

### Ambiguiti unique-violation dalam `VerifyStaffID` (nota, bukan bug
baharu - risiko sedia wujud sejak ciri staff-id-verification)

Sejak endpoint pembetulan wujud (`CorrectMemberID`), admin boleh tetapkan
member_id SESIAPA secara manual ke nilai apa-apa - ini bermakna
`VerifyStaffID` (yang turut generate+tulis `member_id` baharu dlm
transaksi yang sama) kini **secara teori** boleh terserempak konflik
unique pada `member_id` (bukan cuma `staff_id`) kalau admin dah manual
set member_id lain ke nilai yang kebetulan sama dgn yang baru digenerate
(peluang sangat rendah - `NextSequence` atomic per-role, tapi bukan
mustahil selepas endpoint manual wujud). Handler unique-violation sedia
ada dlm `VerifyStaffID` (profile.go, sekitar baris 1629) HANYA pulang
mesej "nombor staff ini sudah digunakan" - tak bezakan constraint
`staff_id` drpd `member_id`. **Cadangan**: Task 1 kena semak
`pgErr.ConstraintName` (bukan cuma `isUniqueViolation` generik) dan
pulang mesej yg padan constraint sebenar yg gagal, elak salah diagnos
"staff_id duplicate" sedangkan sebenarnya "member_id manual yg
bertindih".

### Aksara `staff_id` dlm format - keputusan selepas semakan Opus

`staff_id` SENGAJA tiada validasi format (keputusan ciri staff-id-
verification, kekal) - ciri ni embed `staff_id` terus ke dlm
`member_id`. **Pembetulan drpd draf awal (disahkan Opus terhadap kod
sebenar)**: dakwaan asal "member_id muncul dlm nama fail resit" ADALAH
SALAH - `receipt.Filename` (`internal/receipt/receipt.go:44-55`) bina
nama fail drpd `gateway_ref`/fallback id yg disanitize
(`[^A-Za-z0-9_-]+`), member_id cuma masuk **teks badan PDF**, bukan
nama fail. Satu-satunya penggunaan STRUKTUR (bukan paparan semata)
ialah medan `billTo` ToyyibPay (`registration_payment.go`,
`activity_registration_payment.go`) - medan borang gateway pembayaran,
toleran kpd hampir semua aksara.

**Keputusan (bukan cadangan lagi)**:
- **Tolak `/` pada `staff_id`** (3 tapak: register, verify override,
  correct) - kekal keputusan asal, tapi kini alasan yg BETUL: bukan
  sebab nama fail (tu tak berlaku), tapi sebab `/` dlm format BAHARU
  ni ialah PEMISAH STRUKTUR (`MARC-{staff_id}/{tahun}-{kod}`) - staff_id
  yg ada `/` buat bilangan `/` dlm member_id tak konsisten, mengelirukan
  scr visual bila manusia cuba baca/eja member_id (cth via telefon,
  borang kertas). Kosmetik, bukan bug fungsian - tapi cukup keliru utk
  wajar satu sekatan mudah.
- **`-` (dash) TIDAK disekat** - walau kini turut jadi pemisah struktur
  dlm format baharu (`MARC-`/`{tahun}-{kod}`). Sebab: (1) nombor staff
  organisasi SERING mengandungi dash (cth "EMP-001") - sekat `-` block
  format staff ID yang sangat lazim, kos lebih tinggi drpd manfaat; (2)
  disahkan (grep kod sebenar) **TIADA satu tempat pun** dlm codebase ni
  yg parse member_id dgn split ikut `-` - risiko tu kosmetik SEMATA,
  selamat scr fungsian hari ini. **Diterima sengaja sbg risiko sisa** -
  kalau kelak ada keperluan parse member_id secara program (cth
  extract tahun/kod drpd member_id utk laporan), semak semula keputusan
  ni dulu.

## Ralat & kes tepi

- **`generateMemberID` dipanggil dgn `roleKey` yg tak dikenali** (bukan
  salah satu superadmin/tester/penaung) → laluan default (numerik
  ahli biasa) - `switch`/map MESTI ada default case eksplisit, bukan
  panik/ralat.
- **`PATCH /members/:id/member-id` pada ahli yg member_id MASIH NULL**
  → 409, arah ke endpoint verify.
- **`PATCH /members/:id/member-id` konflik unique** → 409.
- **`PATCH /members/:id/member-id` oleh rank < 80** → 403.
- **Dua `VerifyStaffID` konkurrensi pada profile BERBEZA, role SAMA
  (cth dua tester diverify serentak)** → `NextSequence` atomic
  pastikan `T1`/`T2` (bukan `T1`/`T1`) - SELAMAT, sama jaminan dgn
  sequence ahli sedia ada.
- **Ahli sedia ada (format lama `MARC{YYYY}/{MM}/{seq:04d}`)** → TIDAK
  disentuh, TIADA migration/backfill diperlukan (member_id lama kekal
  di DB persis macam sedia ada - ciri ni cuma ubah kod GENERATE
  member_id BAHARU sahaja).

## Skop eksplisit LUAR ciri ni

- Tiada penguatkuasaan "superadmin cuma 1" secara kod - polisi
  organisasi sahaja (lihat Q2 di atas).
- Tiada UI/skrin Flutter baharu diperlukan - member_id dipaparkan
  di tempat sedia ada (profile, resit, senarai ahli) tanpa perubahan
  paparan, cuma nilai/format string berubah utk pendaftaran BAHARU.
- Role `penaung` TIDAK dicipta dlm sesi ni - cuma reka bentuk kod
  disediakan supaya penambahan role tu kelak semudah mungkin.
