# MARC Backend — TODO

Kerja yang **belum siap** sahaja. Sejarah penuh (keputusan, gotcha, hasil
verifikasi setiap stage) ada dalam git log — cari ikut nombor stage.
Struktur kod: [`ARCHITECTURE.md`](./ARCHITECTURE.md).
Schema & migration: [`DATABASE.md`](./DATABASE.md).

Stage 0–15 siap: auth custom, RBAC, posts/comments/likes, kelulusan ahli,
push, upload R2, donation Stripe, jejak audit, pembersih storan, polisi
simpanan. Modul aktiviti (backend penuh) siap — jurangnya direkod di bawah.

---

## Perlu tindakan kau (bukan kod)

- [ ] **Deploy environment `production` Railway** — `staging` sahaja live.
- [ ] **Migrate data lama dari Supabase** (2 profiles, 4 roles).
- [ ] **MATIKAN Public Development URL r2.dev di Cloudflare.** Kod dah
      guna presigned GET (lihat bawah), tapi disahkan 2026-08-09: objek
      MASIH boleh diambil tanpa tandatangan melalui
      `https://pub-....r2.dev/posts/...` (status 200). Selagi toggle tu
      hidup, perubahan kod tak menutup apa-apa.
      Cloudflare → R2 → bucket → Settings → Public Development URL →
      **Disable**. Selepas tu `R2_PUBLIC_URL` boleh dikosongkan.
- [ ] **Rotate kunci test Stripe** yang sempat masuk git (commit `c170391`,
      dah di-amend sebelum push — tapi rotate tetap lebih selamat).
- [ ] **Sambungkan Redis ke marc-go**: tambah pemboleh ubah rujukan
      `REDIS_URL = ${{Redis.REDIS_URL}}` pada perkhidmatan marc-go.
      Perkhidmatan Redis wujud tapi app tak nampak. Lihat bahagian Redis
      di bawah untuk sama ada ia berbaloi buat masa ni.

## Stage 9 — Postgres RLS (defense-in-depth)

Keputusan: YA, sebagai lapisan KEDUA atas app-level check sedia ada — bukan
ganti. Belum start; skopnya lebih besar daripada nampak.

**Blocker**: Railway `DATABASE_URL` connect sebagai role `postgres`
(superuser → **auto bypass RLS**). Tanpa selesaikan ni dulu, RLS jadi hiasan.

- [ ] Cipta DB role khas app (`NOSUPERUSER`, tiada `BYPASSRLS`), `GRANT`
      secukupnya, tukar `DATABASE_URL` (dev + staging + prod)
- [ ] Migration `ENABLE ROW LEVEL SECURITY` + policy untuk `profiles`,
      `device_tokens`, `refresh_tokens`, `email_verification_tokens`,
      `posts`, `comments`
- [ ] **Impact luas**: perlu `SET LOCAL app.current_user_id` setiap
      transaksi authenticated (policy rujuk `current_setting(...)`) — sentuh
      semua handler yang query terus atas `pool`; perlu helper elak
      boilerplate
- [ ] Test: cuba bypass app-level check secara sengaja, RLS mesti tahan

## Payment — sambungan (Stripe slice dah siap)

Lihat `marc_flutter/PAYMENT-STRIPE.md` untuk apa yang dah jalan.

- [ ] **FPX: daftar SSM → BRN → aktifkan semula Stripe.** FPX
      `available: false` pada akaun (disahkan via API 2026-08-09) — bukan
      toggle yang terlepas. Stripe memerlukan **BRN** untuk memproses caj
      FPX dan menerima payout; akaun ni `business_type: individual` tanpa
      BRN dan tiada keupayaan `fpx_payments`. Langkah penuh (SSM → BRN →
      MY TIN → pengaktifan → kunci baharu → daftar semula webhook) ada
      dalam `marc_flutter/PAYMENT-STRIPE.md`. Semuanya pentadbiran, bukan
      kod. Sehingga itu: DuitNow QR + kad (+ GrabPay).
      - [ ] Selepas berdaftar: semak semula framing "sumbangan peribadi
            kepada pembangun" — duit akan masuk akaun PERNIAGAAN, jadi
            empat tempat teks tu perlu dikemas kini
      - [ ] Selepas berdaftar: semak mandat e-Invois LHDN
- [ ] **Akaun Stripe belum aktif untuk live**: `charges_enabled: false`,
      `card_payments: inactive`, tiada `currently_due` — Stripe masih
      memproses. Mod test tak terjejas.
- [ ] **Threshold RM500 belum wired** — `selectGateway` sentiasa pulang
      Stripe. SociaBuzz (<RM500) belum research: ada API/webhook rasmi untuk
      verify pembayaran, atau manual sahaja?
- [ ] **ToyyibPay (yuran ahli)** belum start. Keputusan produk dulu:
      - Sekali bayar atau berulang (tahunan/bulanan)?
      - Bila gate yuran mula berkuat kuasa untuk ahli sedia ada?
      - Gate diletak dalam middleware mana (padanan `RequireVerifiedEmail`)?

## Modul Aktiviti — jurang yang tinggal

Backend modul aktiviti siap (6 jadual aktiviti + 2 migration `notifications`,
`internal/certificate`, muat naik PDF ke R2 sisi pelayan, handler untuk
aktiviti/sesi/pendaftaran/kehadiran/sijil/pengesahan awam/push). Yang di
bawah ini **tidak** dibina, dan sebahagiannya sengaja.

### Sengaja dikecualikan daripada skop (spec reka bentuk)

- [ ] **Yuran aktiviti tidak berfungsi.** `activities.fee_cents` dan
      `activity_registrations.payment_status`/`payment_ref` wujud dalam
      schema sebagai cangkuk — **tiada gateway disambungkan langsung**.
      `Register` sentiasa tulis `payment_status = 'not_required'` tanpa
      syarat. Hanya aktiviti **percuma** yang berfungsi.
      **Perangkap:** API *menerima* `fee_cents > 0` (borang Flutter tidak
      mendedahkannya, tetapi `POST`/`PATCH /activities` mendedahkan). Bila
      itu berlaku, pendaftaran tetap percuma dan senyap — DAN klausa
      kelayakan sijil `(a.fee_cents = 0 or r.payment_status = 'paid')`
      menjadi palsu untuk **semua** pendaftar, jadi tiada seorang pun layak
      menerima sijil dan tiada ralat menjelaskan sebabnya. Sehingga gateway
      mendarat, kekalkan `fee_cents = 0`. Bergantung pada keputusan gateway
      yang sama dengan yuran ahli (lihat bahagian Payment).
- [ ] **Check-in `self_scan` dan `code`.** Kekangan `method` dalam
      `activity_attendances` menerima keempat-empat nilai, tetapi hanya
      `manual` dan `scan` ada pelaksanaan (lihat komen
      `activity_attendance.go:39`). `self_scan` memerlukan **token
      berputar** — `checkin_token` sekarang statik, jadi satu tangkapan
      skrin QR boleh diedarkan dan sesiapa boleh tanda dirinya hadir.
      Jangan buka self-scan tanpa token berputar.
- [ ] **Sijil pencapaian (johan/naib johan) dan sijil peranan** (jurulatih,
      pengadil). `activity_certificates` **tiada lajur jenis** langsung —
      menambahnya perlukan migration, bukan sekadar UI. Penyertaan sahaja
      buat masa ini.

### Perlukan kerja berjadual (scheduler yang belum wujud)

- [ ] **Peringatan H-1 tidak dibina.** Spec menjanjikannya dalam senarai
      push (diterbitkan / H-1 / sijil sedia / dibatalkan); tiga yang lain
      ada, H-1 tiada. Ia perlukan pencetus berasaskan masa, dan codebase ni
      tiada scheduler. Dua goroutine latar yang ada (`reaper` 15 minit,
      `retention` harian) ialah sapuan idempoten yang berjalan pada SETIAP
      instance — menggantungkan penghantaran push pada salah satu bermakna
      N replika hantar N peringatan kepada orang yang sama. Perlu
      penyahduaan (baris "reminder dihantar") sebelum sapuan boleh jadi
      tuan rumah.
- [ ] **Aktiviti tidak pernah beralih ke `completed` secara automatik.**
      `statusCompleted` wujud dan diterima oleh penapis senarai, tetapi
      tiada kod di mana-mana yang MENULIS nilai itu — cari
      `statusCompleted` dalam `handlers/` dan tiga padanan semuanya ialah
      pengisytiharan/senarai penapis. Aktiviti kekal `published` selamanya
      selepas sesi terakhir tamat. Kesan: tab "Lepas" di Flutter bergantung
      sepenuhnya pada perbandingan tarikh, dan pengurus tak ada cara
      menandakan aktiviti sebagai selesai kecuali `PATCH` status manual.
      Pilihan: sapuan berjadual, atau peralihan pada penerbitan sijil.

### Keselamatan

Item keselamatan modul ini disatukan dalam bahagian **Security** di bawah,
supaya tiada dua versi yang boleh menyimpang. Ringkasan:

- **L12** `checkin_token` dalam respons senarai peserta — Low sekarang,
  **Medium sebaik `self_scan` dibina**; tutup sebelum itu.
- **L13** `auditActor` memegang sambungan kolam kedua — seluruh repo.
- **L14** ujian kebenaran langkau dalam CI — PR yang membuang semakan
  `authz.IsManagement` lulus hijau.
- **L15** ahli boleh memusnahkan bukti kehadirannya sendiri tanpa jejak.

### Disahkan oleh mesin, belum oleh mata manusia

- [ ] **`sample-sijil.pdf` belum pernah dilihat manusia.** Ujian
      `internal/certificate` membuktikan PDF dijana, muat, dan setiap medan
      boleh dienkod cp1252 — ia **tidak** membuktikan susun atur itu
      kelihatan betul. Buka fail contoh dan lihat sebelum sijil pertama
      dihantar kepada ahli sebenar.
- [ ] **Larian menyeluruh manual belum dibuat**: cipta → terbit → daftar →
      tanda hadir → terbit sijil → muat turun PDF → imbas QR pada PDF →
      halaman pengesahan. Termasuk membuka halaman pengesahan dalam
      pelayar penyamaran untuk membuktikan ia benar-benar awam tanpa sesi.

### Minor yang sengaja ditangguhkan

Tiada satu pun menyekat penggunaan; semuanya direkod supaya tidak
"ditemui semula" sebagai pepijat baharu.

- [ ] Ralat mesej pendaftaran terbalik: mendaftar SEBELUM
      `registration_opens_at` memulangkan `errRegistrationClosed`
      ("pendaftaran telah ditutup"), dan `errActivityNotOpen` ("aktiviti
      belum dibuka") turut dipakai untuk `cancelled`/`completed`. Klien
      Flutter mengklasifikasikan sendiri (enum `RegistrationBlocker`), jadi
      teksnya lebih tepat daripada pelayan — pelayan yang perlu dibetulkan.
- [ ] Semakan tetingkap pendaftaran guna `time.Now()` aplikasi, bukan
      `now()` DB — terdedah kepada clock skew antara replika.
- [ ] `method` tidak disilang-semak dengan pengenal yang dihantar:
      `method="manual"` bersama `checkin_token` diterima. Rekod kaedah jadi
      tidak boleh dipercayai untuk audit.
- [ ] `PATCH /activities/:id` dengan `registration_closes_at` =
      `"0001-01-01T00:00:00Z"` beri 500, bukan 400 (perangkap zero-time;
      wujud juga pada `POST`).
- [ ] Pendaftaran yang dibatalkan beri 404 melalui `checkin_token` tetapi
      409 melalui `registration_id` — dan tiga keadaan 404 berbeza hanya
      dibezakan oleh teks Melayu, jadi klien terpaksa string-match.
- [ ] `reason` diterima dan **dibuang senyap** bila `amend=false`.
      Pengesahan "sebab pindaan diperlukan" hanya berjalan dalam cabang
      `if req.Amend`, jadi caller yang menghantar `reason` tanpa `amend`
      mendapat 200 dan percaya sebabnya direkod — sedangkan tiada apa
      ditulis. Sama ada tolak dengan 400, atau rekodkannya.
- [ ] **`auditActor` dipanggil sambil memegang sambungan transaksi** —
      dipindahkan ke **L13** dalam bahagian Security, sebab kesannya
      ketersediaan (kehabisan pool) dan ia menyentuh `posts.go` serta
      `profile.go`, bukan modul aktiviti sahaja.
- [ ] `certificate.formatTarikh` tiada guard zero-time — `time.Time{}`
      dicetak sebagai **"1 Januari 1"** pada PDF sijil. Ini output yang
      DILIHAT ahli, bukan log dalaman. Tiada laluan sedia ada yang
      menghasilkan zero-time (`activity_date` NOT NULL), tapi tiada apa
      yang menghalangnya juga.
- [ ] `SetActivityCertificatesIssuedAt` tulis `now()` walaupun 0 sijil
      diterbitkan — lajur itu merekod cubaan terakhir, bukan penerbitan
      sebenar.
- [ ] Fasa 2 penerbitan sijil (jana PDF + naik R2) berjalan **inline pada
      permintaan HTTP**. Boleh disambung semula (gilir dalam DB), jadi ia
      selamat — tapi 200 ahli bermakna satu permintaan yang panjang.
      `storage.PutObject` juga pegang badan penuh dalam memori dan tiada
      timeout per-panggilan; jangan fan-out goroutine tanpa had bila ini
      dipindahkan ke pekerja latar.
- [ ] Laluan 429 had kadar pada endpoint pengesahan awam tidak diuji, dan
      `doVerify` cipta enjin had kadar baharu setiap permintaan (bocor
      goroutine `cleanupLoop` dalam ujian).
- [ ] Baris `notifications` dan penghantaran push boleh menyimpang
      (konsisten dengan `notifyOwner` sedia ada), dan bajet 2 minit untuk
      pemberitahuan pukal meliputi SELURUH gelung, bukan per-penerima.
- [ ] `activities.go` 999 baris. Penyemak putuskan JANGAN pecahkan buat
      masa ni (`profile.go` sedia ada 748) — direkod supaya keputusan itu
      jelas sengaja.

## Had gambar (siap 2026-08-09)

Client hadkan kepada 2048px + kualiti JPEG 95 sebelum naik. Backend
kuatkuasakan SEMULA pada `MaxImageDimension` 4096 melalui
`image.DecodeConfig` atas header 64KB (bukan muat turun penuh) — presigned
URL membenarkan client menaikkan apa-apa terus ke R2, jadi semakan sisi
client bukan sempadan keselamatan. Had longgar sedikit (4096 lwn 2048)
sebab client sepatutnya dah kecilkan; apa-apa jauh di atas tu bukan lagi
kecuaian.

- [ ] WEBP tak diukur — decoder bukan dalam pustaka standard. Had bait
      (5MB) masih terpakai. Tambah `golang.org/x/image/webp` kalau WEBP
      jadi biasa.

## Gambar profil — backend ✅ (Flutter belum)

- `profiles.avatar_r2_key` (migration `20260809200000`), simpan KUNCI bukan
  URL — domain awam bucket akan berubah sebelum produksi.
- `PATCH /me` terima `avatar_r2_key`. Pointer bertiga keadaan: tak dihantar
  = biar, `""` = buang, kunci = ganti.
- Semakan pemilikan (`IsPendingUploadOwnedByUser`) sebelum menerima kunci —
  tanpa ni sesiapa boleh tetapkan kunci orang lain sebagai avatar sendiri.
- Had dimensi SENDIRI: `MaxAvatarDimension` 1024 (client hantar 512).
  Tiada sebab simpan 2048px untuk bulatan 28–80dp.
- Avatar lama digilirkan ke `deleted_uploads` dengan reason
  `avatar_replaced`, dalam transaksi yang sama + catatan audit.
- `avatar_url` dalam `profileResponse`, `memberResponse`, dan
  `author` bagi post + comment (elak N+1 lookup dalam feed).

Diuji lawan Postgres sebenar: kunci bukan milik caller ditolak dan tak
mengubah apa-apa; avatar lama betul-betul digilirkan.

- [ ] Reaper: belum ada ujian live khusus yang membuktikan avatar yatim
      dituntut (laluan sama macam gambar post, jadi kemungkinan besar okay).

## Pembetulan: memadam user pernah MUSTAHIL

Ditemui oleh ujian semasa kerja avatar. `audit_logs.actor_id` ada
`on delete set null` supaya jejak kekal bila akaun dipadam — tetapi "set
null" itu satu UPDATE, dan trigger append-only menolak semua UPDATE
kecuali bentuk redaksi PII. Jadi `delete from users` gagal terus.

Migration `20260809210000` meluaskan peraturan: `actor_id`, `ip_address`
dan `user_agent` boleh DIKOSONGKAN kepada NULL, tiada apa lagi boleh
berubah, dan ketiga-tiganya tak boleh ditulis ganti dengan nilai lain.
Sejarah kekal tak boleh ditulis semula. Ditutup dengan ujian.

## URL R2 ditandatangani (presigned GET) ✅ kod — tetapan Cloudflare belum

`PublicURL` diganti `SignedURL(ctx, key)` di SEMUA tapak: gambar post,
avatar dalam profil, senarai ahli, dan penulis post/comment. URL sah
**2 jam**, dijana melalui endpoint S3, jadi ia berfungsi pada bucket
PERSENDIRIAN.

**Masalah yang hampir terlepas: kestabilan URL.** Presigned URL
mengandungi `X-Amz-Date`. Menandatangani semula setiap permintaan
menghasilkan rentetan URL berbeza setiap kali — dan cache imej pada
peranti dikunci ikut URL. Tanpa apa-apa, setiap tatalan feed akan memuat
turun semula SETIAP gambar, memusnahkan cache klien dan menghentam
bucket yang dikadar-hadkan. Penyelesaian: cache URL yang ditandatangani
selama **1 jam** (separuh tempoh sah, supaya klien tak pernah menerima
URL yang hampir luput).

Cache guna **Redis** bila ada — ini kegunaan Redis KEDUA yang sah, dan
satu yang aku terlepas dalam analisis awal. Tanpa cache kongsi, setiap
replika menandatangani URL sendiri dan klien terlepas cache setiap kali
ia mencapai instance berlainan. Jatuh balik kepada cache dalam-memori
bila Redis tiada.

Disahkan lawan R2 sebenar: URL ditandatangani pulang 200; URL S3 yang
SAMA tanpa query tandatangan ditolak.

- [ ] **Belum selesai sehingga r2.dev dimatikan** — lihat item pertama
      dalam "Perlu tindakan kau". Objek masih 200 melalui r2.dev.
- [ ] Semak had kadar r2.dev tak lagi relevan selepas dimatikan; endpoint
      S3 ada had berbeza.

## Redis — modul mana yang patut guna, dan mana yang TIDAK

Redis disambung (`internal/redisclient`, pilihan, no-op bila `REDIS_URL`
kosong) dan **digunakan oleh had kadar** — satu-satunya modul yang
mendapat faedah.

**Blocker**: perkhidmatan Redis wujud di Railway tapi `marc-go` tiada
pemboleh ubah rujukan. Tambah pada perkhidmatan marc-go:
`REDIS_URL = ${{Redis.REDIS_URL}}`.

Prinsip pemandu: **tiada apa dalam app ni yang menyimpan KEBENARAN dalam
Redis.** Redis di sini pengganda skala, bukan simpanan. Kalau Redis
hilang, kita hilang penyelarasan antara instance — bukan data.

| Modul | Guna Redis? | Sebab |
|---|---|---|
| `http/middleware` rate limit | **YA — satu-satunya kes sebenar** | Token bucket dalam memori proses. Dgn N replika, had 5/min jadi 5×N/min |
| `auth` refresh token | **TIDAK — lebih teruk** | `DELETE ... RETURNING` beri single-use atomik + tahan restart. Redis buang kedua-duanya |
| `audit` | **TIDAK — memecahkan jaminan** | Catatan ditulis DALAM transaksi mutasi. Redis tak boleh sertai transaksi Postgres |
| `reaper` gilir padam | **TIDAK** | Gilir ialah jadual Postgres: tahan restart, dan enqueue berlaku dlm transaksi yang sama dgn padam post. Gilir Redis hilang kerja bila proses mati |
| `payment` idempotensi webhook | **TIDAK — dah diselesaikan lebih baik** | `UpdateDonationStatusByGatewayRef` ada `and status <> 'succeeded'`, jadi Postgres dah jamin tepat-sekali. `SETNX` Redis lebih lemah |
| `retention` | **TIDAK** | Sapuan harian, DELETE/UPDATE SQL tulen |
| `storage` (R2) | **TIDAK** | Presign stateless; `pending_uploads` dlm Postgres |
| `email`, `receipt`, `push`, `onesignal` | **TIDAK** | Stateless, panggilan HTTP keluar |
| `authz` cache role | **TIDAK — risiko keselamatan** | Satu query berindeks. Cache basi bermakna ahli yang DAH diturunkan pangkat masih bertindak sebagai management sehingga TTL tamat |
| Cache feed / kiraan notifikasi | **BELUM** | Pagination cursor atas lajur berindeks. Pada ratusan ahli, Postgres tak berpeluh. Optimize bila ada nombor yang menunjukkan masalah |

### Had kadar teragih ✅

`middleware.RateLimiter` — token bucket dalam skrip **Lua** (bukan
INCR/EXPIRE): baca-kira-tulis mesti atomik, dan Lua mengekalkan semantik
isi-semula `rate.Limiter` yang sama. Kaunter tetingkap-tetap akan
membenarkan 2x had di sempadan tetingkap.

- Baldi **dinamakan** (`auth`, `upload`, `donation`). Tanpa nama,
  ketiga-tiganya berkongsi kunci Redis yang sama dan saling menghabiskan
  kuota. Versi setempat tak ada masalah ni sebab setiap panggilan cipta
  map sendiri — jadi ia jenis pepijat yang muncul HANYA selepas Redis
  dihidupkan.
- **Gagal-terbuka**: Redis mati → jatuh balik kepada baldi setempat, bukan
  tolak semua. Redis yang tumbang tak boleh mengunci ahli keluar daripada
  log masuk. Timeout 200ms supaya Redis perlahan tak menahan permintaan.
- Tanpa `REDIS_URL`, tingkah laku sama macam sebelum ni (per-instance).

Diuji lawan Redis sebenar (`REDIS_TEST_URL=... go test
./internal/http/middleware/`): burst dikuatkuasakan, **dua middleware
berasingan berkongsi baldi yang sama** (inti sebenar), nama mengasingkan
baldi, dan token diisi semula ikut masa. Plus laluan jatuh-balik diuji
tanpa Redis dan dengan Redis yang tak dapat dicapai.

Pasang Redis tempatan untuk ujian: `brew install redis`, kemudian
`redis-server --port 6399 --daemonize yes`.

### Sengaja TANPA kunci teragih

`reaper` dan `retention` dua-dua berjalan pada SETIAP instance. Ia
selamat tanpa kunci Redis sebab kedua-duanya idempoten: `DeleteObject`
R2 idempoten, dan gilir padam guna batu nisan. Menambah kunci teragih
akan menambah kebergantungan + mod kegagalan baharu untuk masalah yang
reka bentuk ni memang dah elak. **Jangan "betulkan" ini dengan Redis.**

## Jejak audit — jurang yang tinggal

- [ ] Audit untuk `create` post/comment. Volum tinggi, faedah rendah (entiti
      sendiri dah simpan author + created_at) — keputusan produk, belum buat.
- [ ] Comment tiada gambar buat masa ni. Kalau ditambah, ia mesti masuk
      gilir `deleted_uploads` yang sama macam post.

## Security

### Low yang sengaja dibiar

- [ ] **L10** trusted-proxy (`100.64.0.0/10` + RFC1918) betul untuk Railway,
      tapi kalau pindah platform lain kena semak semula.
- [ ] **L11** tiada CORS config — okay sekarang (client mobile sahaja).
      Perlu bila ada web client.

### Modul Aktiviti (disemak 2026-08-14)

Keluar daripada semakan menyeluruh modul aktiviti. Severiti di bawah ialah
severiti **hari ini** — L12 khususnya naik apabila satu ciri lain mendarat,
jadi baca syarat kenaikan itu, bukan label sahaja.

- [ ] **L12 — `checkin_token` dihantar dalam respons senarai peserta.**
      **Low sekarang → Medium sebaik `self_scan` dibina. Tutup SEBELUM itu.**

      `queries/activity_registrations.sql` (`ListRegistrationsByActivity`)
      guna `select r.*`, jadi `ListRegistrationsByActivityRow` membawa
      `CheckinToken string \`json:"checkin_token"\``
      (`internal/db/sqlc/activity_registrations.sql.go:248`), dan handler
      memulangkan baris itu mentah-mentah
      (`internal/http/handlers/activity_registrations.go:240`).

      Kenapa Low sekarang: endpoint ini management sahaja, dan management
      memang sudah boleh menanda sesiapa hadir melalui `registration_id`.
      Token itu tidak memberi mereka kuasa yang mereka tiada. Pendedahannya
      bersifat sampingan — log, laporan ranap, cache proksi, tangkapan skrin
      pada peranti pengurus.

      Kenapa ia naik dengan `self_scan`: pada hari ahli boleh check-in
      sendiri dengan mengimbas, token itu menjadi **kelayakan pembawa**.
      Sesiapa yang pernah nampak senarai peserta boleh menanda ahli lain
      hadir untuk sesi yang mereka tidak hadiri — dan kehadiran itulah yang
      menentukan siapa dapat sijil.

      Pembaikan: senaraikan lajur secara eksplisit dalam query (buang
      `r.*`), atau petakan kepada struct respons seperti yang dibuat oleh
      endpoint pengesahan awam. Jangan tunggu sampai `self_scan` bermula.

- [ ] **L13 — `auditActor` mengambil sambungan kolam KEDUA sambil transaksi
      memegang yang pertama.** Low–Medium, ketersediaan, **seluruh repo**.

      `internal/http/handlers/audit_helpers.go` membaca profil melalui
      `h.queries` (terikat kolam) sedangkan pemanggil sedang memegang
      transaksi. `pgxpool` tanpa konfigurasi memberi
      `MaxConns = max(4, NumCPU)`, jadi pada kotak 2-vCPU empat tulisan
      pengurusan serentak boleh berbuntu.

      Tapak: `activities.go`, `activity_attendance.go` — dan **sudah sedia
      ada** dalam `posts.go` dan `profile.go` sebelum kerja ini. Baiki
      sekali gus seluruh modul, bukan setakat laluan aktiviti.

      Meringankan: endpoint ini pentadbiran sahaja dan berkonkurensi rendah,
      dan timeout klien melepaskan sambungan. Pembaikan: nilai `auditActor`
      **sebelum** `Begin` — kebanyakan tapak panggilan baharu sudah begitu.

- [ ] **L14 — ujian kebenaran LANGKAU dalam CI.** Medium sebagai jurang
      jaminan, bukan kelemahan hidup.

      `.github/workflows/ci.yml` menjalankan `go test ./... -v` tanpa blok
      `services:` dan tanpa Postgres, jadi setiap `*_live_test.go` dalam
      repo melapor SKIP pada setiap pull request. Antaranya ialah penegasan
      403-untuk-bukan-management pada **setiap** endpoint pengurusan modul
      ini.

      Maknanya: PR yang membuang satu semakan `authz.IsManagement` lulus
      hijau. Tiada apa pada laluan CI yang membuktikan kebenaran masih
      berkuat kuasa.

      Penegasan set-medan PII bagi endpoint pengesahan awam sengaja
      dipindahkan ke ujian unit tanpa DB (`activity_certificates_test.go`)
      atas sebab yang sama persis — ujian kebenaran tidak.

      Pembaikan: tambah perkhidmatan Postgres pada `ci.yml` (keputusan infra
      kau), atau pindahkan penegasan authz bernilai tertinggi ke ujian yang
      tidak perlukan DB.

- [ ] **L15 — ahli boleh memusnahkan bukti kehadirannya sendiri, tanpa
      jejak.** Low (mencederakan diri), tetapi tidak boleh dipulihkan.

      `CancelRegistration` (`queries/activity_registrations.sql`) tiada
      pengawal langsung atas status, tetingkap masa, atau kehadiran sedia
      ada — ia menukar `status='cancelled'` tanpa syarat. Pembatalan
      pendaftaran juga **sengaja tidak diaudit** (keputusan spec: volum
      tinggi, baris itu sendiri simpan `cancelled_at`).

      Kesan: ahli yang hadir setiap sesi menekan "Batal pendaftaran" pada
      aktiviti yang sudah tamat. `ListEligibleForCertificate` menuntut
      `r.status = 'registered'`, jadi dia hilang daripada kelayakan secara
      senyap; tetingkap pendaftaran sudah tutup jadi tidak boleh daftar
      semula; tiada catatan audit menjelaskan apa berlaku. Baris kehadiran
      kekal tetapi menjadi yatim.

      Pembaikan: tolak pembatalan (409) sebaik ada baris kehadiran, atau
      sebaik `now() > activities.starts_at`. Klien juga sepatutnya
      melumpuhkan butang itu untuk aktiviti `completed`/`cancelled` —
      dicatat sebagai jurang dalam `marc_flutter/TODO.md`.

### Risiko diterima — bukan kerja tertunggak

- **L21 — Register bocor kewujudan akaun, lawan pertahanan timing
  Login.** `auth.go:140` pulang `409 "email ini sudah berdaftar"` terus,
  sedangkan Login sengaja guna bcrypt hash palsu tetap supaya tak boleh
  bezakan akaun wujud/tak wujud (`auth.go:28-34`). **Keputusan
  2026-08-15**: diterima sebagai risiko, bukan dibaiki. Mendedahkan
  kewujudan akaun pada pendaftaran ialah UX standard industri (GitHub,
  Google buat sama) — fix sebenar (respons generik tak kira wujud/tidak)
  akan pecahkan kontrak yang `marc_flutter` bergantung (mesej ralat
  spesifik pada skrin daftar) untuk manfaat keselamatan yang kecil,
  memandangkan rate limit 5/min/IP sedia ada dah hadkan enumerasi kepada
  perlahan, bukan block sepenuhnya. Semak semula kalau `authRateLimit`
  pernah dilonggarkan.
- **Halaman pengesahan sijil awam mendedahkan nama ahli.** Sengaja; lihat
  bahagian "Pendedahan privasi yang disedari" dalam spec reka bentuk.
  Perlindungan yang sudah ada dan **mesti kekal**: token legap 32 aksara
  (bukan nombor siri berjujukan, jadi tidak boleh dienumerasi), baldi had
  kadar bernama `verify` yang berasingan daripada `auth`, respons ialah
  struct enam medan yang dibina khas dan bukan baris DB, dan `404` yang
  serupa bait-demi-bait untuk token tidak dikenali dan token cacat supaya
  tiada oracle. Ujian unit tanpa DB menguatkuasakan set medan itu — kalau
  ia gagal, seseorang sedang menambah medan ke halaman awam.

### Audit menyeluruh `internal/` (2026-08-15)

Audit Opus berskop penuh atas semua subpackage `internal/`, bukan setakat
modul aktiviti. Penemuan baharu sahaja — item yang bertindih dengan L10–L15
di atas tidak diulang.

- [x] **L16 — had dimensi imej tidak pernah berkuat kuasa (HIGH, DIBAIKI).**
      `internal/storage/r2.go` minta `Range: "bytes=0-11"` (12 bait) tapi
      `verifyDimensions` perlukan ~33+ bait untuk `image.DecodeConfig`
      berjaya baca header PNG — jadi ia SENTIASA gagal, dan laluan ralat
      sengaja `return nil` ("jangan tolak sebab tak dapat ukur"). Komen di
      atas kod (`r2.go:236`) malah dah kata "64KB, bukan 12 bait macam
      dulu" — fix separuh jalan, komen dikemaskini tapi `Range` string
      terlepas. Kesan: `MaxImageDimension`/`MaxAvatarDimension` tidak
      pernah dikuatkuasakan pada upload sebenar — ahli boleh PUT PNG
      20000×20000px (bawah had 5MB bait), setiap peranti yang scroll feed
      cuba nyahkod ~1.6GB piksel. Ujian `dimensions_test.go` hijau sebab ia
      panggil `verifyDimensions` terus dengan header penuh, bukan
      `verifyImage` laluan sebenar. **Dibaiki**: `Range` ditukar ke
      `bytes=0-65535`.
- [ ] **L17 — tiada had panjang pada medan teks bebas** (post/comment
      content, `display_name`, `phone`, activity `title`/`location_name`,
      `onesignal_id`) — hanya had body 1MB global. `display_name` 1MB
      terbenam dalam setiap post/comment feed (`posts_common.go:263-278`)
      **dan** disalin ke `activity_certificates.recipient_name`, dipaparkan
      pada halaman verify sijil **awam tanpa auth**. `donations.go:48-56`
      satu-satunya tempat yang ada had — sepatutnya jadi corak untuk yang
      lain.
- [ ] **L18 — spam notification/push tanpa had melalui like berulang.**
      `LikePost` (`queries/likes.sql`) `on conflict do nothing`, tapi
      handler post panggil `notifyOwner` tanpa syarat selepas SETIAP
      panggilan (tak boleh bezakan insert baharu vs conflict). Tiada rate
      limit pada `POST /posts/:id/like`. Gelung endpoint ni = harassment
      bersasar (push OneSignal berulang) + pertumbuhan jadual
      `notifications` tanpa had. `CommentHandler.Like` betul (tak
      notify). Fix: `LikePost` jadi `:execrows`/`returning`, notify hanya
      bila baris benar-benar masuk.
- [ ] **L19 — draft activity boleh dibaca ahli biasa melalui ID.**
      `ActivityHandler.List` gate `status=draft` di belakang
      `requireManagement` (`activities.go:352-359`) tapi
      `ActivityHandler.Get` (`activities.go:451`) tiada semakan status
      langsung — `GetActivityByID` tapis `deleted_at is null` sahaja.
      Ahli lulus dengan UUID aktiviti boleh baca draf penuh (tajuk,
      kapasiti, yuran, sesi, bilangan pendaftaran). Terhad oleh
      ketidaktekaan UUIDv4, tapi peraturan kebenaran yang `List`
      kuatkuasakan langsung tak wujud pada laluan by-id.
- [ ] **L20 — `/auth/refresh` dan `/auth/logout` tiada rate limit.**
      Satu-satunya laluan auth tanpa auth yang tak kena `authRateLimiter`
      (register/login/verify semua kena). Bukan risiko teka token (opaque
      256-bit), tapi laluan miss `Refresh` buat SHA-256 + `UPDATE
      ... RETURNING` + `GetRefreshTokenByHash` kedua + mungkin `DELETE
      ... where family_id` (`auth.go:277-296`) — write-path amplifier
      Postgres tanpa had kadar.
- [ ] **L22 — resit donation PDF cetak tarikh salah.** `donations.go:248`
      set `paidAt := time.Now()` semasa proses webhook, bukan baca
      timestamp PaymentIntent Stripe sebenar. Stripe retry/kelewatan
      penghantaran webhook = tarikh salah pada dokumen kewangan yang
      dihantar email kepada penderma, tak boleh dibetulkan selepas fakta.

**Informational (bukan kerja tertunggak, tapi corak sama L12):**
`queries/comments.sql` dan `queries/posts.sql` select `u.email as
author_email` ke setiap baris feed. Dibuang di response mapping sekarang
(`memberResponse`/`posts_common.go`), tapi corak sama dengan L12 — sekali
ada `c.JSON(rows)` mentah, direktori emel ahli bocor.

**Disahkan bersih**: `internal/authz`, `internal/auth` (jwt/password/token),
`internal/payment` (webhook verification, idempotency `status <>
'succeeded'`), `internal/audit`, `internal/retention`, `internal/reaper`,
`internal/redisclient`, `internal/email`, `internal/onesignal`,
`internal/push`, `middleware/{bodylimit,logging,verified,auth}`, laluan
sijil issue/revoke/verify/download (`Download` 404-bukan-403 pada
kepemilikan betul), tiada SQL concatenation, tiada mass-assignment via
`bind.go`, bucket rate-limit Lua (`middleware/ratelimit.go`) kukuh.

### Sudah ditutup dalam pusingan ini

Direkod supaya tidak diburu semula sebagai penemuan baharu:

- Penerbitan sijil kini mengambil `LockActivityForRegistration` dan
  menjalankan keempat-empat bacaannya di dalam transaksi. Sebelum ini dua
  pengurus yang menekan Terbitkan serentak boleh membakar nombor siri, dan
  sijil boleh diterbitkan kepada seseorang yang baru sahaja dijadikan tidak
  layak — tidak boleh diterbitkan semula selepas ditarik, kerana unik
  `(activity_id, user_id)` bukan separa.
- `fee_cents != 0` kini ditolak `400` pada `POST` dan `PATCH /activities`.
  Sebelum ini aktiviti berbayar didaftar percuma **dan** klausa kelayakan
  `(a.fee_cents = 0 or r.payment_status = 'paid')` menjadi palsu untuk
  setiap pendaftar — sifar orang layak, tiada ralat di mana-mana.

## Ujian

Ujian lawan infra sebenar, semua di-skip secara lalai:

```bash
# R2 (kebenaran token + baca balik ikut URL awam)
R2_LIVE_TEST=1 go test ./internal/storage/ -run TestR2LivePermissions -v

# Reaper (padam objek R2 betul-betul hilang)
R2_LIVE_TEST=1 REAPER_TEST_DB="postgres://localhost:5432/marc_reaper?sslmode=disable" \
  go test ./internal/reaper/ -v

# Polisi simpanan (trigger append-only + redaksi PII)
RETENTION_TEST_DB="postgres://localhost:5432/marc_retention?sslmode=disable" \
  go test ./internal/retention/ -v

# Handler (audit approve/reject, keterlihatan emel ahli)
HANDLER_TEST_DB="postgres://localhost:5432/marc_handler?sslmode=disable" \
  go test ./internal/http/handlers/ -v

# Modul aktiviti (jatuh balik ke HANDLER_TEST_DB kalau tak diset)
ACTIVITY_TEST_DB="postgres://localhost:5432/marc_handler?sslmode=disable" \
  go test ./internal/http/handlers/ -v -race

# Muat naik R2 sisi pelayan (PDF sijil)
R2_LIVE_TEST=1 go test ./internal/storage/ -run TestR2PutObjectLive -v
```

Guna DB **buangan** untuk yang perlukan Postgres — bukan DB dev kau.

- [ ] Handler test yang tinggal masih manual — belum ada suite integrasi
      penuh untuk posts/comments.
- [ ] **Semua `*_live_test.go` LANGKAU dalam CI.** `.github/workflows/ci.yml`
      jalankan `go test ./...` tanpa perkhidmatan Postgres dan tanpa env
      ujian, jadi setiap ujian live dalam repo — bukan modul aktiviti
      sahaja — lapor SKIP pada setiap pull request. Maknanya: **binaan
      hijau tidak bermakna ujian live lulus.** Ini ditemui semasa kerja
      sijil, di mana ujian privasi (`TestVerifyTidakMendedahkanPII`) ialah
      satu-satunya tripwire yang menghalang medan PII baharu daripada
      bocor melalui halaman pengesahan awam — ia langkau, jadi ia menjaga
      apa-apa pun. Ubat separa: assertion set-medan itu dipecahkan menjadi
      ujian unit TANPA DB (`activity_certificates_test.go`) supaya ia
      berjalan pada setiap PR. Yang selebihnya masih langkau.
      Keputusan sebenar (perkhidmatan Postgres dalam `ci.yml`) ialah
      keputusan infra kau — `ci.yml` sengaja tidak disentuh.
      **Sudut keselamatannya ada pada L14**: antara yang langkau itu ialah
      penegasan 403-untuk-bukan-management pada setiap endpoint pengurusan,
      jadi PR yang membuang satu semakan `authz.IsManagement` lulus hijau.
