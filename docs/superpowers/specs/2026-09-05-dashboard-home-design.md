# Dashboard sebagai Home - Design Spec

**Tarikh:** 2026-09-05
**Repo terlibat:** `marc_go` (endpoint baharu) + `marc_flutter` (skrin & navigasi)
**Status:** spec siap, kod belum ditulis

---

## Keperluan asal (pemilik produk)

Client mahu halaman utama app jadi **dashboard**, bukan feed post. Feed
kekal wujud tetapi berpindah ke tab lain.

Keputusan yang disahkan dalam sesi brainstorming 2026-09-05:

| Soalan | Keputusan |
|---|---|
| Dashboard untuk siapa | **Role-aware** - ahli nampak ringkasan peribadi, admin nampak statistik organisasi di atasnya |
| Kedudukan feed | **5 tab**: Utama, Hebahan, Aktiviti, Notifikasi, Profil |
| Sumber data | **Endpoint baharu `GET /dashboard`** (bukan komposisi client-side) |
| Kad ahli | Aktiviti saya akan datang, aktiviti terbuka, status keahlian & bayaran, sijil + notifikasi belum baca, jumlah ahli |
| Kad admin | Pending approval, ringkasan kutipan, statistik ahli, statistik aktiviti |
| Ambang role blok admin | **admin (rank 80) ke atas sahaja** |
| Bentuk endpoint | Satu endpoint, payload role-shaped (pendekatan A) |

Pendekatan B (dua endpoint: `/dashboard` + `/admin/stats`) ditolak buat
masa ini kerana satu skrin = satu panggilan = satu keadaan loading. Kalau
blok admin nanti jadi berat, ia boleh dipecahkan ke B tanpa mengubah kad
ahli - itu sebabnya blok admin diasingkan sebagai objek `admin` tersendiri
dalam payload dan bukan medan bertaburan di aras atas.

---

## ⚠️ Kesan yang perlu disedari sebelum mula

### 1. FeedPage ialah rumah kanonik ahli BELUM diluluskan

`marc_flutter/lib/features/posts/feed_page.dart:58-140` memapar
`PendingStatusView` (dengan butang bayar yuran pendaftaran dan "Semak
semula") serta `_EmailNotVerifiedView`. `ApprovalGate`
(`lib/shared/ui/widgets/approval_gate.dart:120`) menghantar user dari tab
lain ke sana dengan butang **"Pergi ke Utama"**.

Menurunkan Feed ke tab kedua **tanpa memindahkan gate ini** akan
mendaratkan ahli pending pada dashboard yang backend 403-kan. Jadi gate
pending/emel MESTI dipindahkan ke `DashboardPage` sebagai sebahagian kerja
ini, bukan kerja susulan.

### 2. Bergantung pada kerja Staff ID yang sedang dibina

Spec `2026-09-02-staff-id-verification-design.md` (kod belum siap pada
tarikh spec ini) menambah `outstanding_registration_fee` pada
`/me/payments` dan menjadikan staff verified syarat approve + exempt
yuran. Kad "Status keahlian & bayaran" **guna semula** helper yang kerja
itu hasilkan; ia TIDAK mengira semula logik "berhutang atau tidak" dalam
handler dashboard, kerana dua tempat yang mengira benda sama akan
menyimpang.

**Kesan penjadualan:** task dashboard yang menyentuh
`outstanding_registration_fee_cents` bergantung pada kerja Staff ID
mendarat dahulu. Task lain tidak.

### 3. Derma tidak kelihatan kepada admin

`DATABASE.md` merekod bahawa data derma dalam `/admin/payments` dikunci
**superadmin sahaja**, bukan admin. Kalau `revenue_this_month.total_cents`
mencampur derma, seorang admin akan melihat agregat yang mengandungi data
yang dia tidak dibenarkan lihat di skrin lain.

**Keputusan:** `donation_cents` = `null` untuk admin, diisi untuk
superadmin sahaja; `total_cents` hanya menjumlahkan apa yang caller layak
lihat. Kad memapar nota kecil "tidak termasuk derma" apabila
`donation_cents` null.

---

## Perubahan API (marc_go)

### Route

```go
// internal/http/router.go - bersama group `approved`
approved.GET("/dashboard", dashboardHandler.Get)
```

Group `approved` (`RequireAuth` + `RequireApprovedStatus`), **bukan**
`verified`, sebaris dengan `/activities` dan `/me/activities`. Mengira
notifikasi sendiri tidak mendedahkan kandungan, dan client sudah memapar
skrin "sahkan emel" sebelum sempat memanggil endpoint ini.

Ahli `pending`/`rejected` menerima **403** - ini dijangka; client tidak
memanggil endpoint dalam keadaan itu (lihat bahagian client di bawah).

### Response

```jsonc
{
  "member": {
    "upcoming_registrations": [        // maks 3, isih starts_at menaik
      { "id": "uuid", "activity_id": "uuid", "title": "…",
        "starts_at": "2026-09-10T09:00:00Z", "ends_at": "…",
        "category_name": "Bengkel", "payment_status": "paid" }
    ],
    "open_activities": [               // maks 3, belum didaftar caller
      { "id": "uuid", "title": "…", "starts_at": "…",
        "category_name": "…", "fee_cents": 2000, "currency": "MYR",
        "registration_count": 12 }
    ],
    "membership": {
      "status": "approved",
      "member_id": "MARC-12345/2026-KL",     // null kalau belum diisu
      "staff_id_verified": true,
      "outstanding_registration_fee_cents": null   // int kalau berhutang
    },
    "unread_notifications": 4,
    "certificates_total": 7,
    "total_members": 312
  },
  "admin": null
}
```

Blok `admin` (hanya apabila `authz.IsAtLeastRole(ctx, q, uid, "admin")`
bernilai true; `null` untuk semua yang lain):

```jsonc
"admin": {
  "pending_approvals": 5,
  "revenue_this_month": {
    "currency": "MYR",
    "registration_cents": 30000,
    "activity_cents": 12500,
    "donation_cents": null,        // superadmin sahaja; null untuk admin
    "total_cents": 42500           // jumlah apa yang caller layak lihat
  },
  "member_stats": {
    "active": 312, "pending": 5, "new_this_month": 18,
    "by_department": [ { "code": "KL", "name": "Kuala Lumpur", "count": 90 } ]
  },
  "activity_stats": {
    "upcoming": 4,
    "registrations_this_month": 87,
    "attendance_rate": 0.72        // 0..1; null kalau pembahagi sifar
  }
}
```

Semua nama medan snake_case, konsisten dengan seluruh API.

`by_department` dihadkan kepada **6 bahagian teratas mengikut count**;
selebihnya digabung sebagai baris `{"code": null, "name": "Lain-lain"}`.
Tanpa had, sebuah organisasi dengan 40 bahagian menghantar 40 baris untuk
sebuah carta yang hanya memuatkan segelintir.

### Query baharu - `queries/dashboard.sql`

Semua kiraan berindeks, dijalankan **berturut-turut** dalam satu handler
(bukan satu SQL gergasi, bukan juga goroutine selari - beban tiap satu
kecil dan urutan bersiri menjadikan penggunaan pool boleh diramal):

| Query | Untuk | Nota |
|---|---|---|
| `ListMyUpcomingRegistrations` | kad aktiviti saya | corak `ListMyRegistrations` + `a.ends_at >= now()`, `limit 3` |
| `ListOpenActivitiesForMe` | kad aktiviti terbuka | `status='published'`, `ends_at >= now()`, `not exists` terhadap `activity_registrations` caller, `limit 3` |
| `CountUnreadNotifications` | kiraan ringkas | `read_at is null` - tiada query sedia ada, mesti baharu |
| `CountMyCertificates` | kiraan ringkas | `activity_certificates` caller, tidak dibatalkan |
| `CountApprovedMembers` | kiraan ringkas (SEMUA ahli nampak) | `status='approved'` dan aktif |
| `CountPendingMembers` | admin | `status='pending'` |
| `SumRevenueThisMonth` | admin | tiga jadual, lihat di bawah |
| `CountNewMembersThisMonth` | admin | `approved_at` dalam bulan semasa |
| `MemberStatsByDepartment` | admin | group by department, isih desc |
| `ActivityStatsThisMonth` | admin | upcoming, pendaftaran, kadar kehadiran |

**Definisi `attendance_rate`** (elak dua tafsiran): bilangan baris
`activity_attendances` yang direkod untuk sesi yang **bermula dalam bulan
semasa dan sudah tamat**, dibahagi dengan bilangan pendaftaran aktif
(`status <> 'cancelled'`) pada aktiviti bagi sesi-sesi itu. Sesi yang
belum tamat dikecualikan supaya kadar tidak nampak rendah palsu sepanjang
bulan berjalan. Pembahagi sifar → `null`, bukan `0`.

Untuk ahli biasa hanya 5 query pertama dijalankan.

**`SumRevenueThisMonth` merentas TIGA jadual** (lihat DATABASE.md, "Tiga
jadual bayaran, satu jadual log"): `registration_payments.amount_cents`
(`status='succeeded'`), `activity_registrations.fee_cents_paid`
(pendaftaran berbayar), dan `donations.amount_cents`
(`status='succeeded'`). Ketiga-tiga menggunakan amaun **snapshot** pada
baris bayaran, BUKAN `activities.fee_cents` hidup - sebab yang sama
seperti resit (yuran boleh ditukar selepas ahli bayar). Bahagian derma
hanya dijalankan untuk superadmin.

"Bulan semasa" = bulan kalendar dalam zon waktu server (`now()`), sama
seperti seluruh backend. Tiada parameter julat tarikh - kalau nanti perlu
pilih bulan, itu ciri berasingan.

### Handler

`internal/http/handlers/dashboard.go`, satu fungsi `Get`:

1. Ambil `user_id` daripada context auth.
2. Jalankan 5 query ahli, bina `member`.
3. `authz.IsAtLeastRole(…, "admin")` - kalau false, `admin: null`, tamat.
4. Kalau true: jalankan query admin. `authz.IsAtLeastRole(…, "superadmin")`
   menentukan sama ada bahagian derma dijalankan dan diisi.

Tiada caching pada pusingan pertama. Kalau metrik menunjukkan endpoint ini
panas, tempat betul untuk cache ialah blok `admin` (TTL pendek), bukan
blok `member` yang peribadi.

---

## Perubahan client (marc_flutter)

### Navigasi

`lib/app/nav_shell.dart` - 5 destinasi:

| Indeks | Label | Ikon | Route |
|---|---|---|---|
| 0 | Utama | `home` | `/dashboard` |
| 1 | Hebahan | `campaign` | `/feed` |
| 2 | Aktiviti | `event` | `/activities` |
| 3 | Notifikasi | `notifications` | `/notifications` |
| 4 | Profil | `person` | `/profile` |

NavigationBar Material 3 direka untuk 3-5 destinasi, jadi 5 kekal dalam
spec. Label "Hebahan" (bukan "Feed") konsisten dengan bahasa UI sedia ada.

`lib/app/router.dart`:

- `StatefulShellBranch` baharu di **posisi 0** dengan
  `GoRoute('/dashboard')`; branch `/feed` bergeser ke indeks 1.
  **Urutan branch MESTI sepadan dengan urutan `destinations`** - itu
  satu-satunya pengikat antara dua fail, dan ia senyap bila salah.
- Tiga rujukan `'/feed'` bertukar jadi `'/dashboard'`:
  - `router.dart:78` - redirect selepas log masuk
  - `router.dart:142` - fallback `/checkout` bila `extra` hilang/salah jenis
  - `approval_gate.dart:120` - butang "Pergi ke Utama"
- Route `/feed` sendiri **kekal** - deep link sedia ada tidak putus.

### Gate pending/emel berpindah ke Dashboard

`DashboardPage` mengulang corak `feed_page.dart:58-140`:

1. `isInitialLoading` (loading DAN belum pernah ada nilai) → spinner.
   Semakan ini WAJIB: tanpanya, `status` null semasa muat pertama tidak
   dapat dibezakan daripada null semasa ralat, dan dashboard penuh
   terpapar sekejap kepada ahli pending (bug sebenar 2026-08-24 pada Feed).
2. `status != 'approved'` → `PendingStatusView` (widget kongsi sedia ada).
3. `status == 'approved' && emailVerified == false` → paparan sahkan emel.
4. Fail-open kalau `/me` gagal selepas cubaan pertama - jangan sekat ahli
   approved kerana gangguan rangkaian sekejap.

`_EmailNotVerifiedView` **diekstrak** daripada `feed_page.dart` ke
`lib/shared/ui/widgets/` supaya Dashboard dan Feed berkongsi satu sumber.
Dua salinan borang "hantar semula emel pengesahan" akan menyimpang.

FeedPage **mengekalkan** gatenya sendiri - ia masih boleh dicapai terus
sebagai tab dan sebagai deep link.

### Fail baharu - `lib/features/dashboard/`

| Fail | Isi |
|---|---|
| `dashboard_models.dart` | kelas biasa + `fromJson` tulis tangan, corak `post_models.dart` |
| `dashboard_providers.dart` | `dashboardProvider` (`FutureProvider<DashboardData>`) |
| `dashboard_page.dart` | susun atur skrin sahaja |
| `widgets/*.dart` | satu fail per kad |

**Penghuraian defensif.** Setiap medan nested dibaca dengan andaian ia
mungkin hilang: `json['admin'] == null` → `null`, setiap kiraan
`as int? ?? 0`, setiap senarai `?? const []`. Payload dashboard ialah yang
paling kerap akan berubah bentuk antara keluaran, dan app lama di telefon
ahli mesti terus berfungsi apabila backend menambah medan.

**`dashboardProvider`** `ref.watch` status profil dan hanya memanggil
`/dashboard` apabila `status == 'approved'`; jika tidak ia pulang awal
tanpa menyentuh rangkaian, supaya 403 yang boleh diramal tidak memenuhi
log ralat.

### Susunan skrin (atas → bawah)

1. App bar: salam + nama papar.
2. Blok admin **kalau `admin != null`**: pending approval (kad amaran bila
   > 0), kutipan bulan ini, statistik ahli, statistik aktiviti.
3. Status keahlian & bayaran - **naik ke atas sekali** apabila
   `outstanding_registration_fee_cents != null`.
4. Aktiviti saya akan datang (maks 3) → pautan "Lihat semua"
   ke `/my-activities`.
5. Aktiviti terbuka (maks 3) → pautan ke tab Aktiviti.
6. Baris kiraan ringkas: notifikasi belum baca · sijil · jumlah ahli -
   setiap satu pintasan ke `/notifications`, `/my-certificates`,
   `/members`.

`RefreshIndicator` seluruh skrin → `ref.invalidate(dashboardProvider)`.

### Keadaan ralat

Satu panggilan bermakna satu keadaan gagal. Supaya kegagalan itu tidak
mengosongkan seluruh home:

- `loading` → **skeleton** berbentuk kad yang sama, bukan spinner tengah
  skrin. Home dibuka berpuluh kali sehari; spinner penuh terasa lebih
  perlahan daripada keadaan sebenar.
- `error` → kad ralat ringkas dengan butang "Cuba lagi", diletak **di
  atas** baris pintasan navigasi yang masih berguna tanpa data, supaya
  user tidak buntu.

---

## Ujian

### marc_go - `internal/http/handlers/dashboard_live_test.go`

Corak `*_live_test.go` sedia ada (Postgres sebenar):

1. Ahli biasa approved → 200, `admin` ialah `null`, `member` lengkap.
2. Admin → 200, blok `admin` ada, `revenue_this_month.donation_cents` ialah
   `null`, dan `total_cents` **tidak** termasuk derma.
3. Superadmin → 200, `donation_cents` berisi dan termasuk dalam
   `total_cents`.
4. User `pending` → 403 (daripada `RequireApprovedStatus`).
5. `open_activities` tidak mengandungi aktiviti yang caller sudah daftar.
6. `SumRevenueThisMonth` mengabaikan bayaran `pending`/`failed` dan
   bayaran bulan lepas.

### marc_flutter - `test/features/dashboard/`

1. Ahli biasa (payload `admin: null`) - blok admin tidak wujud dalam tree.
2. Payload dengan `admin` - kad pending approval, kutipan, statistik ahli
   dan aktiviti terpapar.
3. Profil `status: 'pending'` → `PendingStatusView`, dan `/dashboard`
   tidak pernah dipanggil.
4. `DashboardData.fromJson` menelan (a) payload tanpa kunci `admin`,
   (b) payload dengan medan tambahan yang tidak dikenali,
   (c) senarai kosong.
5. Ujian navigasi: `/dashboard` ialah branch 0 dan `/feed` branch 1 -
   pengikat rapuh antara `nav_shell.dart` dan `router.dart`.

Ujian sedia ada yang menyebut `/feed` (`post_card_test.dart:34`,
`notifications_page_test.dart:123`) disemak - kedua-duanya mendaftar route
`/feed` sendiri dan tidak bergantung pada redirect, jadi ia sepatutnya
kekal lulus; sahkan, jangan andaikan.

---

## Di luar skop

- Kad boleh susun semula / sembunyi oleh user.
- Carta (blok admin ialah angka + senarai, bukan graf) - tambah kemudian
  kalau client minta.
- Julat tarikh boleh pilih untuk statistik admin.
- Cache sisi server.
- Peringkat pertengahan untuk supervisor/manager - mereka melihat
  dashboard ahli biasa buat masa ini.
