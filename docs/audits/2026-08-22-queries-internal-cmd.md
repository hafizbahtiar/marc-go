# Audit — `queries/` + `internal/` + `cmd/` (2026-08-22)

Pusingan ketiga. Dua pusingan sebelumnya (2026-08-14 modul aktiviti,
2026-08-15 `internal/` menyeluruh) direkod terus dalam `TODO.md` sebagai
L1–L27. Pusingan ini pertama kali disimpan sebagai laporan berasingan
kerana ia merangkumi kod yang dibina **selepas** audit terakhir dan
sebahagian penemuannya perlukan ruang untuk menerangkan laluan kegagalan.

Status setiap item dijejaki dalam `TODO.md` sebagai **L28–L36**. Fail ini
ialah buktinya; `TODO.md` ialah senarai kerjanya.

## Skop dan kaedah

Ketiga-tiga direktori dibaca sepenuhnya — 24 fail dalam `queries/`, 41
migration, semua handler/middleware/subpackage `internal/`, dan
`cmd/api/main.go`. Termasuk kod yang tiada dalam audit 2026-08-15: yuran
aktiviti, `paymentreconcile`, `activitysweep`, `activitylifecycle`,
`paymentlog`, resit PDF, permintaan pemadaman akaun.

Semasa audit:

```
go build ./...   bersih
go vet ./...     bersih
gofmt -l .       kosong
go test ./...    semua lulus (ujian live SKIP — tiada env)
```

Maknanya setiap penemuan di bawah **lulus compiler DAN lulus ujian sedia
ada**. Tiada satu pun akan ditangkap oleh CI seperti ia dikonfigur
sekarang.

## Bar keterukan

Sama seperti pusingan 2026-08-15: **CRITICAL** = boleh dieksploit
sekarang, oleh aktor tanpa/rendah keistimewaan, dengan laluan jelas ke
kebocoran data / eskalasi keistimewaan / kerugian kewangan / bypass auth
penuh.

**Tiada penemuan capai bar itu.** Tiada apa-apa dibaiki terus. L28 dan L29
dinilai HIGH kerana kedua-duanya kehilangan data atau duit **secara
senyap, tanpa jejak** — bukan kerana ia boleh dicetuskan oleh penyerang.

| ID | Keterukan | Ringkasan | Status |
|---|---|---|---|
| L28 | HIGH | reaper boleh padam gambar post yang masih hidup | ✅ dibaiki 2026-08-22 |
| L29 | HIGH | bil ToyyibPay wujud sebelum baris DB — bayaran boleh hilang | ✅ dibaiki penuh 2026-08-22 (migration + susunan dibalikkan) |
| L30 | MEDIUM | backlog `paymentreconcile` membesar selama-lamanya | ✅ dibaiki 2026-08-22 (tingkap 7 hari + limit 200) |
| L31 | MEDIUM | `WriteTimeout` 15s < operasi 30s yang ia hoskan | 🟡 ditampung (90s); pembaikan penuh terbuka |
| L32 | MEDIUM | tiada laluan tukar/reset kata laluan langsung | ⬜ terbuka — perlu reka bentuk |
| L33 | LOW | `/me/payments` tak pulangkan derma; resit derma tak dicapai | ⬜ terbuka — perlu perubahan Flutter |
| L34 | LOW | respons `PATCH /comments/:id` hilang `author` | ✅ dibaiki 2026-08-22 |
| L35 | LOW | like comment tak hantar notifikasi | ⬜ terbuka — perlu keputusan produk |
| L36 | MEDIUM | enam pakej tiada ujian, termasuk modul duit | ✅ dibaiki 2026-08-22 (keenam-enam) |

Status terkini dijejaki dalam `TODO.md`; jadual ni snapshot pada akhir
pusingan pembaikan pertama (2026-08-22).

### Apa yang dibaiki dalam pusingan pertama

Dipilih atas kriteria "boleh proceed tanpa migration laluan-duit, tanpa
keputusan produk, tanpa perubahan Flutter serentak":

- **L28** — pembaikan penuh, dua lapisan, + migration indeks +
  tiga ujian (dua daripadanya disahkan GAGAL terhadap query lama).
- **L34** — pembaikan penuh; `LikeCount`/`LikedByMe` didapati mempunyai
  kecacatan yang sama pada laluan yang sama dan dibaiki sekali.
- **L31** — tampung (`WriteTimeout` 15s → 90s).
- **L29** — tampung (bil yatim kini direkod dengan `GatewayRef` supaya
  boleh dicari; kejadiannya tidak dihalang).

### Pusingan kedua (2026-08-22, kemudian hari)

- **L30** — pembaikan penuh: tingkap atas 7 hari + `batchSize` 200 +
  guard `status <> 'cancelled'` pada query aktiviti.
- **L12, L13** — dua item lama daripada audit 2026-08-15 yang masih
  terbuka, ditutup dalam batch yang sama (lihat `TODO.md`).

### Pusingan ketiga (2026-08-22) — L36, liputan ujian

Keenam-enam pakej yang `no test files` ditutup. Tiada lagi pakej dalam
`internal/` tanpa ujian.

Pengajaran yang berbaloi direkod: **ujian mutasi dijalankan pada tiga
guard paling kritikal, dan DUA daripada tiga penegasan asal ternyata
vacuous** — ia lulus sama ada logiknya betul atau rosak.

| Guard | Mutasi | Keputusan awal |
|---|---|---|
| Cutoff `activitysweep` | samakan 45m dan 24j | ✅ ditangkap |
| Tingkap atas `maxAge` | longgarkan ke ~114 tahun | ❌ **tak ditangkap** |
| Dedup peringatan | buang guard Go `affected == 0` | ❌ **tak ditangkap** |

Puncanya berbeza dan kedua-duanya bernilai diingat:

1. **Penegasan yang merujuk dirinya sendiri.** Ujian menyemai baris pada
   `maxAge + 24h` — umur benih diterbitkan daripada pemalar yang diuji,
   jadi menaikkan pemalar turut menaikkan umur benih dan baris itu kekal
   di luar tingkap tak kira apa. Pembaikan: umur MUTLAK, plus semakan
   awal yang gagal kalau pemalar menghampirinya.

2. **Menguji lapisan yang salah.** Dedup peringatan berlaku pada DUA
   lapisan: tapisan `ListActivitiesNeedingReminder` dan guard `where
   reminder_sent_at is null` pada UPDATE. Menjalankan `RunOnce` dua kali
   hanya menguji yang pertama — pusingan kedua tak pernah melihat baris
   itu langsung. Guard race sebenar hanya penting apabila DUA replika
   menyenaraikan sebelum salah satu menanda, yang ujian satu-proses tak
   boleh hasilkan. Pembaikan: panggil `MarkActivityReminderSent` dua kali
   TERUS dan tegaskan panggilan kedua mengena sifar baris.

Selepas dibetulkan, ketiga-tiga mutasi menggagalkan ujian yang
sepatutnya. **Ujian yang tak pernah dilihat gagal bukan ujian** —
menjalankan mutasi ke atasnya ialah sebahagian kerja, bukan tambahan.

### Pusingan keempat (2026-08-22) — L29 penuh, dan L14

**L29** dibaiki sepenuhnya: migration menjadikan `gateway_ref` nullable
dengan indeks unik separa, dan susunan `Checkout` dibalikkan supaya baris
DB sentiasa mendahului bil gateway. Urutan berbahaya (bil hidup, baris
tiada) kini mustahil; tetingkap baki yang lebih sempit dinyatakan dalam
`TODO.md` dan bukan dilaporkan sebagai selesai.

**L14** — CI kini menjalankan Postgres 18 + Redis 8 sebenar. Ini menutup
gandingan yang berjalan sepanjang keseluruhan audit: setiap pusingan
sebelum ini menulis ujian yang tidak pernah berjalan pada PR.

| | Sebelum | Selepas |
|---|---|---|
| PASS | 89 | 202 |
| SKIP | 122 | 9 (semuanya R2) |

Ditambah bersama satu langkah **tripwire** yang menggagalkan job kalau
mana-mana ujian melapor SKIP atas sebab env var DB hilang. Tanpanya,
menamakan semula satu env var akan mengembalikan CI kepada 89/122 sambil
kekal hijau — persis mod kegagalan yang L14 wujud untuk hapuskan.
Disahkan dua arah: senyap bila perkhidmatan hadir, menyala bila satu env
var dibuang.

Turut dilakukan: **empat item lama ditutup selepas disahkan sudah siap
tetapi tak pernah ditanda** — L24 (had panjang medan), L25 (rate limit
comment/post/me), L26 (baldi auth berasingan), L27b (zon waktu resit).
L27a ditanda semula sebagai *risiko diterima* dan bukan kerja tertunggak.
Item basi menyebabkan orang menyiasat semula perkara yang sama, jadi
menutupnya ialah kerja, bukan kemasan.

---

## L28 — reaper boleh padam gambar post yang MASIH hidup

**Keterukan:** HIGH (kehilangan data senyap, tidak boleh dipulihkan)

### Laluan kegagalan

`queries/uploads.sql` — komen dan query tidak sepadan:

```sql
-- name: ListStalePendingUploads :many
-- Pending upload yang tak pernah dilekatkan pada mana-mana post.
select * from pending_uploads
where created_at < $1
order by created_at
limit $2;
```

Komen mendakwa satu invarian ("tak pernah dilekatkan"); query tidak
menyemaknya. Ia bergantung **sepenuhnya** pada baris dikeluarkan daripada
`pending_uploads` semasa post dicipta. Laluan itu, dalam `posts.go`, buang
ralatnya:

```go
// Best-effort — kegagalan padam row ni tak patut gagalkan post
// yang dah berjaya dicipta (row lingering harmless, cuma
// tracking stale untuk key yang dah attached).
_ = q.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID})
```

Baris yang tinggal itu **bukan** harmless. Enam jam kemudian
(`reaper.abandonedAfter`):

1. `sweepAbandonedUploads` membaca baris itu daripada
   `ListStalePendingUploads`
2. ia memanggil `EnqueueDeletedUpload(reason: "upload_abandoned")`
3. `drainDeleteQueue` memanggil `r2.DeleteImage` pada kunci itu

Hasilnya: objek R2 bagi gambar post yang **sedang dipaparkan** dipadam.
Baris `post_images` kekal (tiada apa yang menyentuhnya), jadi
`buildPostResponses` terus menandatangani URL untuk objek yang sudah tiada
— feed memaparkan imej rosak, kekal, tanpa ralat di mana-mana.

### Kenapa ia boleh berlaku

`DeletePendingUpload` dipanggil pada `q` — `Queries` yang terikat pada
transaksi cipta post yang sama. Kegagalannya bukan hipotesis kosong:
deadlock, pemutusan sambungan, atau pembatalan statement semuanya
menghasilkan ralat di sini sambil membiarkan `CreatePost` /
`CreatePostImage` yang sudah berjaya untuk dikomit.

Perhatikan juga bahawa komen itu bercanggah dengan dirinya sendiri: ia
membenarkan pengabaian ralat kerana baris yang tinggal "harmless", tetapi
komen pada `EnqueueDeletedUpload` di tempat lain dalam fail yang sama
menerangkan dengan tepat bahawa penggiliran bermakna pemadaman.

### Kontras: laluan avatar buat BETUL

`profile.go` `applyAvatar` melakukan operasi yang setara dan **menyemak**
ralatnya, menggagalkan transaksi:

```go
if err := q.DeletePendingUpload(ctx, ...); err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
    return sqlc.Profile{}, err
}
```

Jadi ini ketidakkonsistenan antara dua laluan dalam pangkalan kod yang
sama, bukan corak yang disengajakan.

### Cadangan

Kedua-duanya, bukan salah satu:

1. Jadikan query itu benar-benar bermaksud apa yang komennya dakwa:
   ```sql
   where created_at < $1
     and not exists (
       select 1 from post_images pi where pi.r2_key = pending_uploads.r2_key
     )
   ```
   Ini juga melindungi daripada mana-mana laluan tulis MASA HADAPAN yang
   terlupa mengeluarkan barisnya.
2. Tukar `_ =` kepada semakan ralat yang menggagalkan transaksi. Rollback
   mengekalkan baris `pending_uploads` — tepat seperti niat asal komen itu
   ("client boleh retry POST /posts dengan r2_keys yang sama").

---

## L29 — bil ToyyibPay dicipta SEBELUM baris DB; bayaran boleh hilang tanpa jejak

**Keterukan:** HIGH (kerugian kewangan senyap)

### Laluan kegagalan

`registration_payment.go` `Checkout`, mengikut turutan:

```go
result, err := h.gw.CreatePayment(ctx, payment.CreateParams{...})
// ← bil SAH kini wujud di ToyyibPay. Ahli boleh bayar mulai saat ini.
if err != nil { … }

regPayment, err := h.queries.CreateRegistrationPayment(ctx, ...)
if err != nil {
    log.Printf("create registration payment row: %v", err)
    c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
    return
}
```

Antara dua panggilan itu terdapat tetingkap di mana bil boleh dibayar
sedangkan tiada apa-apa dalam DB merujuknya. Kalau INSERT gagal, ahli
melihat 500 — tetapi klien sudah menerima, atau boleh menerima,
`RedirectURL`nya pada percubaan sebelumnya, dan bil itu kekal boleh
dibayar sehingga ia luput di pihak ToyyibPay.

### Kenapa tiada apa yang menangkapnya kemudian

Kedua-dua mekanisme pemulihan bergantung pada baris DB yang tidak wujud:

- **Webhook.** `UpdateRegistrationPaymentStatusByGatewayRef` mengena 0
  baris → `pgx.ErrNoRows`. Handler melayannya sebagai replay biasa dan
  **menyenyapkannya sepenuhnya**:
  ```go
  if !errors.Is(err, pgx.ErrNoRows) {
      log.Printf("update registration payment status …")
  }
  ```
  Layanan ini betul untuk replay sebenar; ia juga menelan kes ini.
- **Reconcile.** `reconcileRegistrationPayments` melelar baris
  `registration_payments`. Tiada baris = tiada apa untuk direkonsiliasi.

### Jejak yang tinggal tidak mencukupi

`payment_logs` menerima satu baris `EventCheckoutFailed` — tetapi
binaannya pada laluan ralat itu **tidak** membawa `GatewayRef`, kerana
`result` tidak digunakan di sana. Jadi bil yatim itu tidak boleh dicari
walaupun daripada log yang sengaja dibina untuk diagnosis insiden
bayaran.

### Corak yang sama di tempat lain

`activity_registration_payment.go` mempunyai bentuk yang serupa:
`CreatePayment` berjaya, kemudian `SetRegistrationPaymentRef` gagal.
Kesannya sedikit kurang teruk kerana baris pendaftaran sudah wujud dan
`activitysweep` akhirnya akan membatalkannya — tetapi `payment_ref` tidak
pernah ditulis, jadi webhook bayaran tetap tidak akan menemui apa-apa.

### Cadangan

Terbalikkan turutan supaya DB sentiasa mendahului gateway:

1. `CreateRegistrationPayment` dengan `status='pending'` dan `gateway_ref`
   kosong
2. `CreatePayment`
3. `UPDATE … set gateway_ref = $1 where id = $2`

Kalau langkah 3 gagal, baris itu wujud dan boleh dilihat; kalau langkah 2
gagal, baris `pending` tanpa ref disapu oleh reconcile/retention seperti
mana-mana percubaan terbengkalai.

Minimum mutlak kalau susunan semula ditangguhkan: pada kegagalan INSERT,
rekod `paymentlog.Entry{GatewayRef: result.GatewayRef, …}` supaya bil
yatim boleh dikesan.

---

## L30 — backlog `paymentreconcile` membesar selama-lamanya

**Keterukan:** MEDIUM (kos berulang + kadar API gateway, bukan ketepatan)

Ketiga-tiga query sumber mempunyai had umur **bawah** sahaja. Tiada had
atas, tiada `LIMIT`:

| Query | Predikat |
|---|---|
| `ListPendingRegistrationPaymentsOlderThan` | `status = 'pending' and created_at < $1` |
| `ListPendingDonationsOlderThan` | `status = 'pending' and created_at < $1` |
| `ListPendingActivityRegistrationsOlderThan` | `payment_status = 'pending' and payment_ref is not null and registered_at < $1` |

Masalahnya ialah tiada satu pun daripada baris ini pernah **meninggalkan**
keadaan `pending` apabila pembayar sekadar berhenti:

- **ToyyibPay.** Bil yang tidak dibayar mengembalikan `No data found!`.
  `CheckStatus` memetakannya kepada `"pending"` dengan sengaja ("pembayar
  belum selesai/belum cuba bayar — pending, bukan ralat"). Selamanya.
- **Stripe.** PaymentIntent yang ditinggalkan kekal
  `requires_payment_method`, yang `CheckStatus` petakan kepada
  `"pending"`. Selamanya.
- **Yuran aktiviti.** `CancelStaleUnpaidBills` menetapkan
  `status = 'cancelled'` tetapi **membiarkan `payment_status = 'pending'`**,
  dan query di atas tidak menapis `status <> 'cancelled'`. Jadi baris yang
  sudah mati secara muktamad terus disemak.

### Kesan

Setiap 30 minit, `RunOnce` mengeluarkan satu permintaan HTTP keluar bagi
**setiap baris terkumpul sejak hari pertama**. Bebanan ini monotonik: ia
tidak pernah berkurang, hanya bertambah dengan setiap checkout yang
ditinggalkan. Pada tahun kedua operasi kelab ia menjadi ratusan hingga
ribuan panggilan setiap pusingan, terhadap dua API pihak ketiga, untuk
bayaran yang tiada siapa akan selesaikan.

Pencetus manual (`POST /admin/payments/reconcile`) mewarisi masalah yang
sama dan berjalan secara segerak dalam permintaan HTTP — jadi ia turut
akan melanggar `WriteTimeout` (lihat L31) sebaik backlog cukup besar.

### Cadangan

- Tambah tingkap atas: `and created_at > now() - interval '7 days'`.
  Bayaran yang lebih tua daripada itu bukan lagi kerja rekonsiliasi; ia
  kerja pembersihan.
- Tambah `limit` supaya satu pusingan mempunyai kos maksimum yang
  diketahui.
- Tambah `and status <> 'cancelled'` pada query aktiviti.

---

## L31 — `WriteTimeout` (15s) lebih pendek daripada operasi yang ia hoskan (30s)

**Keterukan:** MEDIUM (kekeliruan operasi; data kekal betul)

`cmd/api/main.go`:

```go
server := &http.Server{
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       15 * time.Second,
    WriteTimeout:      15 * time.Second,
    IdleTimeout:       60 * time.Second,
}
```

Terhadap:

| Operasi | Had | Fail |
|---|---|---|
| Muat naik PDF sijil, **setiap fail** | 30s | `handlers/activity_certificates.go` |
| Muat naik PDF resit | 30s | `handlers/payments.go` |
| Poll ToyyibPay | 15s | `payment/toyyibpay.go` |

### Kesan terburuk: penerbitan sijil

`fillPendingCertificateFiles` melelar penerima secara **berjujukan** —
dengan sengaja, kerana `PutObject` menyimpan keseluruhan PDF dalam memori
dan goroutine tanpa had bermakna ratusan PDF dipegang serentak. Untuk
aktiviti 50–200 orang, keseluruhannya jauh melebihi 15 saat walaupun
setiap muat naik pantas.

Go **tidak** membatalkan `Request.Context()` apabila write deadline
berlalu — ia menetapkan deadline pada `ResponseWriter`. Jadi handler terus
berjalan hingga habis dan datanya betul: sijil dicipta, fail dimuat naik,
`r2_key` ditulis, dan panggilan berikutnya menyambung dengan betul. Yang
gagal hanyalah **respons**.

Kesannya operasi, bukan korupsi: pengurus melihat sambungan putus setiap
kali, tiada cara membezakan "gagal" daripada "berjaya tetapi terlalu lama
untuk dilaporkan", dan akibatnya berkemungkinan menekan Terbitkan berulang
kali — yang selamat (endpoint idempoten mengikut reka bentuk) tetapi tidak
memberitahunya apa-apa.

### Kesan kedua: webhook ToyyibPay

`VerifyWebhook` ToyyibPay membuat panggilan rangkaian keluar dengan had
15s sebelum sebarang kerja DB. Jumlahnya melebihi `WriteTimeout` sebaik
ToyyibPay perlahan — ToyyibPay menerima sambungan putus dan mencuba semula.

Ini kes konkrit bagi amaran generik yang sudah direkod dalam `TODO.md`
("Nota reka bentuk keselamatan — yuran pendaftaran ToyyibPay"): *"pastikan
endpoint webhook tetap pulang cepat kepada ToyyibPay … supaya ToyyibPay
tak retry berulang atas timeout"*. Amaran itu ditulis sebelum ciri dibina;
ini pengesahan bahawa ia berlaku.

### Cadangan

Jangka pendek: naikkan `WriteTimeout` kepada 60–90s. Ia timeout global,
jadi ini melonggarkan perlindungan pada setiap route — boleh diterima
memandangkan `MaxBodySize` dan had kadar menangani vektor yang berbeza.

Lebih baik: jadikan fasa 2 penerbitan sijil kerja latar dan pulangkan 202
serta-merta dengan kiraan kemajuan. Endpoint itu sudah pun boleh disambung
semula dan idempoten — separuh reka bentuknya sudah wujud.

---

## L32 — tiada laluan tukar/reset kata laluan langsung

**Keterukan:** MEDIUM (gap fungsi)

Grep seluruh `internal/`, `cmd/`, `queries/` untuk
`password.?reset|forgot|lupa.kata`: sifar padanan. Tiada
`PATCH /me/password` juga — satu-satunya penggunaan kata laluan dalam
pangkalan kod ialah `registerRequest.Password`, `loginRequest.Password`,
dan `dummyPasswordHash`.

Ahli yang lupa kata laluannya **tiada laluan pemulihan dalam app**. Ia
memerlukan staff mengemas kini `users.password_hash` secara manual melalui
akses DB terus. Untuk app yang menggerbang keahlian di sebalik kelulusan
pengurusan dan yuran sebenar, ini bukan nice-to-have.

Berkaitan, pada permukaan yang sama:

- `min=6` pada kata laluan pendaftaran adalah longgar.
- Tiada lockout per-**akaun**. `authRateLimiter` mengunci ikut
  `c.ClientIP()` sahaja (5 percubaan / minit), jadi brute-force teragih
  merentas banyak IP terhadap satu akaun tidak menemui halangan. L26
  (2026-08-15) sudah merekod dimensi lain masalah pengekunci-IP ini.

Infrastruktur untuk membaikinya sudah ada sepenuhnya: `email.Client`,
corak token legap + hash SHA-256, TTL, dan jadual
`email_verification_tokens` yang boleh dicerminkan hampir baris demi baris.

---

## L33 — `/me/payments` tak pulangkan derma; endpoint resit derma tak boleh dicapai

**Keterukan:** LOW (gap fungsi)

`payments.go` `Mine` mengembalikan tepat dua senarai:

```go
c.JSON(http.StatusOK, gin.H{
    "registration_fee": registrationFee,
    "activity_fees":    activityFees,
})
```

`queries/donations.sql` tidak mempunyai query senarai berskop pengguna
langsung — hanya `GetMyDonationByID`.

Tetapi route `GET /me/payments/donation/:id/receipt` wujud dan memerlukan
`donations.id`. Tiada permukaan API yang mendedahkan id itu kepada
pemiliknya, jadi endpoint tersebut mati secara praktikal: satu-satunya
cara ahli boleh mencapainya ialah dengan meneka UUID.

Ahli yang **log masuk** semasa menderma mendapat `user_id` dikaitkan
(`OptionalAuth`), jadi datanya ada — cuma tiada jalan keluar.

Pembaikan: tambah `ListMyDonations` berskop `user_id` (padanan
`ListMyRegistrationPayments`) dan seksyen ketiga dalam `Mine`. Derma
tanpa nama (`user_id` null) betul untuk dikecualikan — mereka tiada akaun
untuk menuntut baris itu, dan emel resit yang dihantar semasa webhook
ialah satu-satunya jejak mereka mengikut reka bentuk.

---

## L34 — respons `PATCH /comments/:id` hilang `author`

**Keterukan:** LOW

`comments.go` `Update` membina respons tanpa medan `Author`:

```go
c.JSON(http.StatusOK, commentResponse{
    ID:              updated.ID.String(),
    ParentCommentID: nullableUUIDString(updated.ParentCommentID),
    Content:         updated.Content,
    CreatedAt:       formatTime(updated.CreatedAt),
    EditedAt:        formatTimeNullable(updated.EditedAt),
})
```

`Create` dan `List` kedua-duanya mengisinya. `authorResponse` ialah struct
nilai, jadi ia bersiri sebagai `{"member_id":"", "display_name":null,
"avatar_url":null}` dan bukan tiada — klien yang menulis ganti komen dalam
senarai daripada respons ini akan melihat nama dan avatar penulis lenyap
sehingga muat semula.

`Update` sudah memuatkan `existing` (untuk jejak audit) dan `userID` ada
dalam skop, jadi profil boleh dibaca dengan corak yang sama seperti
`Create`.

---

## L35 — like comment tak hantar notifikasi

**Keterukan:** LOW (mungkin disengajakan — perlu pengesahan)

`posts.go` `Like` memanggil `notifyOwner` selepas insert yang berjaya.
`comments.go` `CommentHandler.Like` tidak memanggil apa-apa, dan tiada
komen yang menyatakan ia disengajakan.

**Perhatian penting sebelum "membaiki" ini:** L18 (2026-08-15) merekod
"`CommentHandler.Like` betul (tak notify)" — tetapi ayat itu dalam konteks
*spam like berulang*, di mana ketiadaan notifikasi ialah yang menjadikan
laluan comment selamat. Ia bukan pernyataan bahawa komen tidak layak
menerima notifikasi.

Jadi ini perlu keputusan produk, bukan pembaikan langsung. Kalau
notifikasi ditambah, ia mesti mengambil guard `:execrows` yang sama yang
L18 minta untuk `LikePost` — kalau tidak ia membuka semula gelung spam
yang sama pada permukaan baharu.

---

## L36 — modul duit paling berisiko tiada ujian langsung

**Keterukan:** MEDIUM (liputan)

`go test ./...` melaporkan `no test files` untuk:

```
marc/internal/paymentreconcile
marc/internal/activitysweep
marc/internal/activitylifecycle
marc/internal/authz
marc/internal/auth
marc/internal/config
```

Tiga yang pertama ialah kerja latar yang menulis state perniagaan tanpa
manusia dalam gelung. `paymentreconcile` khususnya **menulis ganti status
bayaran secara automatik** berdasarkan jawapan gateway — ia diberi kuasa
untuk menukar `pending → succeeded` dan `pending → paid` tanpa pengesahan
sesiapa.

Ketiga-tiganya mendedahkan `RunOnce(ctx)` dengan komen yang secara
eksplisit menyatakan ia untuk membolehkan ujian ("Diekspos supaya boleh
dipanggil terus dalam ujian dan bukan menunggu ticker"). Cangkuknya ada;
ujiannya tidak pernah ditulis.

`internal/authz` ialah keseluruhan lapisan kebenaran — gantian app-level
bagi Postgres RLS yang belum wujud (Stage 9). Ketiadaan ujian di sini
bertindih dengan L14: ujian live yang **memang** menegaskan
403-untuk-bukan-management semuanya SKIP dalam CI, jadi tiada apa-apa
dalam saluran automatik yang menangkap penyingkiran semakan
`authz.IsManagement`.

> **Kemas kini 2026-08-22:** kedua-dua belah gandingan itu kini ditutup.
> L36 menulis ujian `authz`; L14 memberi CI perkhidmatan Postgres+Redis
> sebenar supaya ujian itu (dan setiap ujian live lain) benar-benar
> berjalan pada setiap PR. Penyingkiran semakan `authz.IsManagement`
> kini menggagalkan CI.

---

## Minor — direkod, bukan kerja tertunggak

- **`posts.go` `List` menyenyapkan `limit` tak sah** kepada default,
  sedangkan `activities.go` `List` mengembalikan 400 untuk kes yang sama —
  dan komennya menerangkan dengan tepat mengapa senyap itu salah
  (*"limit=500 diamkan jadi 20 ialah jenis perbezaan yang klien tak dapat
  lihat"*). Hujah itu terpakai sama pada kedua-dua laluan.
- **`donations.go` `selectGateway(amountCents)`** mengabaikan
  parameternya sepenuhnya. Ini placeholder yang disengajakan untuk
  ambang RM500 Stripe-lwn-SociaBuzz, tetapi tandatangan yang mengambil
  argumen yang tidak digunakan tidak boleh dibezakan daripada pepijat.
- **`paymentreconcile` mengira `MismatchesFixed++`** juga untuk kes
  `cancelled+paid` yang secara eksplisit **memerlukan campur tangan
  manual**. Ringkasan yang dikembalikan kepada pencetus manual jadi
  terlebih optimistik tepat pada kes yang paling perlukan perhatian.
- **`profile.go` `UpdateMe` tidak atomik** — ia menulis nama/telefon
  dalam satu operasi, kemudian avatar dalam transaksi berasingan. Kalau
  avatar ditolak, ahli menerima 400 sedangkan nama sudah tersimpan.
- **6–10 query DB setiap permintaan tulis** (2 gate middleware +
  `requireManagement` + `auditActor` + muat semula respons). Belum
  masalah pada skala kelab. `auditActor` memanggil `GetProfileByUserID`
  yang `requireManagement` baru sahaja baca — mudah digabungkan bila
  ia mula penting.

---

## Disahkan bersih

Diperiksa dalam pusingan ini tanpa penemuan baharu — direkod supaya tidak
diburu semula:

- **Rotasi refresh token.** `UPDATE … RETURNING` atom dengan guard
  `consumed_at is null`; pengesanan reuse melalui baris yang dikekalkan;
  grace window 5s yang membezakan retry rangkaian daripada kecurian.
- **Corak kunci pesanan.** `LockActivityForRegistration` diambil pada
  setiap laluan tulis aktiviti — daftar, PATCH, ganti sesi, check-in,
  unmark, terbit sijil. Tiada laluan yang tertinggal.
- **`audit.Record` dalam transaksi mutasi** pada setiap tapak panggilan
  tanpa kecuali; tiada satu pun best-effort.
- **Keyset pagination** pada setiap senarai (posts, activities,
  notifications, audit_logs, payment_logs) — tiada OFFSET di mana-mana.
- **`ListVisibleProfiles`** menapis keterlihatan dalam SQL, jadi baris
  yang tidak layak tidak pernah meninggalkan DB.
- **`verifyResponse`** sebagai sempadan eksplisit halaman pengesahan awam,
  dengan ujian tripwire tanpa DB yang benar-benar berjalan dalam CI.
- **`middleware.BlockTesterWrites`** gagal-tertutup pada ralat DB.
- **`extractBillCode`** ToyyibPay — pengendalian berbilang bentuk dan
  pengabaian ralat separa `ParseQuery` kedua-duanya betul dan
  didokumentasikan dengan sebabnya.
- **Pengasingan baldi had kadar bernama** — setiap baldi mempunyai nama
  unik, jadi tiada dua ciri berkongsi kuota Redis.
- **Tiada penggabungan rentetan SQL**, tiada mass-assignment melalui
  `bind.go`.
