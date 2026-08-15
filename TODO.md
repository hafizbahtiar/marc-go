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
- [x] **ToyyibPay guna kes 1 — yuran pendaftaran ahli, SEKALI BAYAR,
      DIBINA DAN DIWIRING 2026-08-15.** Gate diletak pada langkah
      **kelulusan management** (`ApproveMember` → `setMemberStatus`,
      `internal/http/handlers/profile.go`), BUKAN semasa daftar — ahli
      `pending` boleh bayar bila-bila masa semasa menunggu (sebelum atau
      selepas management tengok permohonan). Ahli sedia ada
      (approved sebelum ciri ni wujud) **dikecualikan automatik** — no-op
      check `target.Status == status` yang dah sedia ada return awal
      SEBELUM gate baharu, jadi tiada peralihan sebenar berlaku untuk
      akaun tu, tiada semakan "bila akaun dicipta" diperlukan.

      **Fail baharu**: migration
      `20260815050000_create_registration_payments.sql` (jadual
      berasingan, padan pola `donations` — `id`, `user_id NOT NULL`,
      `amount_cents`, `currency`, `gateway CHECK IN ('toyyibpay')`,
      `gateway_ref`, `status CHECK IN ('pending','succeeded','failed')`,
      unique index `(gateway, gateway_ref)`); `queries/
      registration_payments.sql` (`CreateRegistrationPayment`,
      `UpdateRegistrationPaymentStatusByGatewayRef` — idempotency
      `status <> 'succeeded'` sama pola donations,
      `HasSucceededRegistrationPayment`); `internal/http/handlers/
      registration_payment.go` (`Checkout`/`Webhook`/`ReturnPage`).

      **Route**: `POST /registration-payments/checkout` (RequireAuth
      SAHAJA, sama group `/me` — bukan RequireApprovedStatus, ahli
      pending mesti boleh akses), `POST /registration-payments/webhook/
      toyyibpay` (awam, hardcode toyyibpay bukan `:gateway` sebab cuma
      satu gateway untuk ciri ni), `GET /registration-payments/return/
      toyyibpay` (awam, landing page sahaja — pengesahan sebenar jalan
      async via Webhook). `cmd/api/main.go` `callbackURL`/`returnURL`
      dikemaskini daripada placeholder `/dues/...` ke path sebenar.

      **Race webhook-vs-approve**: SELAMAT tanpa lock tambahan — dua
      laluan sentuh baris BERBEZA (`registration_payments` vs
      `profiles`), gate baca `HasSucceededRegistrationPayment` SEBELUM
      buka transaksi sendiri. Kes paling teruk (confirm webhook &
      approve serentak): salah satu menang ikut turutan read-committed
      biasa Postgres, tiada senario ahli diluluskan TANPA baris
      'succeeded' pernah wujud. Lihat komen penuh di
      `registration_payment.go:158-170`.

      **`REGISTRATION_FEE_CENTS`** — env baharu, default 1000 (RM10,
      PLACEHOLDER — nilai sebenar belum diputuskan management, tukar
      sebelum production).

      **Opus verify flow penuh (backend+Flutter) 2026-08-15**: tiada bug
      integriti kewangan atau bypass auth dijumpai — `ApproveProfile`
      satu-satunya laluan tulis `status='approved'`, webhook tak pernah
      percaya body callback (poll `getBillTransactions` sahaja), amount
      selalu server-side (`Checkout` tiada request body), Flutter tiada
      double-submit/state lapuk, validasi URL https-only ketat. SATU
      isu MEDIUM dijumpai dan **DIBAIKI**: `/registration-payments/
      checkout` dan `/registration-payments/webhook/toyyibpay` tiada
      rate limit (`router.go`) — ditambah `registration-payment-checkout`
      (6s/5, padan bucket `donation`) dan `registration-payment-webhook`
      (2s/20, lebih longgar sebab dipanggil ToyyibPay sendiri, tapi tiap
      panggilan buat outbound poll 15s-timeout jadi lebih mahal per-
      request drpd webhook Stripe). `feed_page.dart` butang bayar
      ditukar `!isRejected` → `status == 'pending'` eksplisit (drift-
      proof kalau status baharu ditambah kelak). Komen lapuk dalam
      `toyyibpay.go` (masih kata "belum wired"/placeholder) dibetulkan.

      **Belum siap sebelum production**: `TOYYIBPAY_SECRET_KEY`/
      `_CATEGORY_CODE` masih sandbox dalam `.env` tempatan (`Enabled()`
      akan pulang `false` di deploy sehingga diisi akaun produksi);
      `REGISTRATION_FEE_CENTS` masih placeholder; kes "bil berjaya
      dibayar" `getBillTransactions` masih tak disahkan (lihat bawah);
      `HasSucceededRegistrationPayment` tak semak `amount_cents` — kalau
      fi dinaikkan kelak, baris lama yang lebih murah tetap lulus gate
      (risiko rendah, `billPriceSetting=1` dah kunci amount di sisi
      ToyyibPay); **belum ada ujian hujung-ke-hujung sebenar pada
      peranti** — flow daftar→bayar→approve tak pernah dijalankan betul-
      betul.

      Gateway kod itu sendiri (`internal/payment/toyyibpay.go`)
      **sengaja tak disentuh** semasa wiring ni — dah disahkan sandbox
      sebelum ni, tiada bug dijumpai semasa integrasi. `VerifyWebhook`
      **sengaja** tak percaya body callback: ToyyibPay tiada sah `hash`
      yang boleh dipercayai (dua sumber sekunder bagi formula
      bercanggah, kajian penuh di `marc_flutter/PAYMENT-TOYYIB.md`) —
      status disahkan semula via poll `getBillTransactions`.
      **Disahkan hujung-ke-hujung terhadap sandbox `dev.toyyibpay.com`
      sebenar 2026-08-15** (bukan cuma unit test) — `createBill` dan poll
      `getBillTransactions` dua-dua dipanggil betul-betul dengan akaun
      sandbox sebenar. Dua bug DIJUMPAI dan DIBAIKI hasil verifikasi ni
      (kod komuniti yang jadi asas awal silap/tak lengkap):
      - `billTo` **wajib** — createBill pulang `{"status":"error","msg":
        "billTo parameter is empty"}` kalau tiada. Dokumentasi komuniti
        tak sebut ini. Kod sekarang ambil `params.Metadata["billTo"]`
        (fallback "Ahli MARC" kalau kosong); `billEmail`/`billPhone`
        turut boleh dihantar via Metadata (`billEmail`/`billPhone` keys),
        pilihan.
      - `getBillTransactions` bila TIADA transaksi lagi pulang teks
        BIASA `"No data found!"` — BUKAN array JSON kosong `[]`. Kod
        sekarang semak literal ni sebelum `json.Unmarshal`, pulang
        `ErrIgnoredEvent` (bukan ralat parse).
      - `createBill` respons **array satu objek dengan `BillCode`**
        disahkan BETUL (padanan andaian asal) — `CreatePayment` diuji
        sebenar, dapat bill code + redirect URL sah, `RedirectURL`
        (`https://dev.toyyibpay.com/{billcode}`) dibuka boleh diakses.
      - **Masih TAK disahkan**: bentuk respons `getBillTransactions` bila
        bil BERJAYA DIBAYAR (`billpaymentStatus="1"`) — verifikasi setakat
        ni cuma capai kes "belum dibayar". Perlu selesaikan satu bayaran
        ujian sebenar (via simulator bank sandbox) untuk sahkan medan
        `billpaymentStatus`/`billpaymentInvoiceNo` betul sebelum
        production.

      **Keputusan produk 2026-08-15**: sekali bayar (BUKAN berulang) —
      soalan "tahunan/bulanan?" dah tak relevan, ni yuran pendaftaran
      SATU KALI sahaja semasa daftar ahli baharu, padanan konsep dengan
      bayaran sekali `donations` (Stripe) yang dah wujud, bukan konsep
      "subscription" ToyyibPay pun tak sokong native (lihat
      `marc_flutter/PAYMENT-TOYYIB.md`).

      **Keputusan produk 2026-08-15 (lanjutan)**: gate diletak pada
      langkah kelulusan management (bukan pada `POST /auth/register`) —
      ahli boleh bayar bila-bila semasa `pending`. Ahli sedia ada
      dikecualikan automatik (jatuh keluar daripada struktur kod, bukan
      semakan eksplisit "bila akaun dicipta"). Skema: jadual berasingan
      `registration_payments`, padan pola `donations`. Butiran penuh +
      fail yang terlibat: lihat entri ToyyibPay di atas.

      Masih belum diputuskan:
      - **`REGISTRATION_FEE_CENTS` sebenar** — default kod 1000 (RM10)
        ialah PLACEHOLDER teknikal, bukan angka yang management dah
        setuju.
      - **`marc_flutter`**: skrin bayar dalam aliran daftar/skrin pending
        belum dibina — backend `Checkout`/`Webhook` sedia tapi tiada UI
        panggil.
      - **Yuran aktiviti** (guna kes 2) — **DIBINA DAN DISAHKAN 2026-08-15**,
        lihat entri "Yuran aktiviti tidak berfungsi" di bawah (bahagian
        Modul Aktiviti) untuk butiran penuh.

## Modul Aktiviti — jurang yang tinggal

Backend modul aktiviti siap (6 jadual aktiviti + 2 migration `notifications`,
`internal/certificate`, muat naik PDF ke R2 sisi pelayan, handler untuk
aktiviti/sesi/pendaftaran/kehadiran/sijil/pengesahan awam/push). Yang di
bawah ini **tidak** dibina, dan sebahagiannya sengaja.

### Sengaja dikecualikan daripada skop (spec reka bentuk)

- [x] **Yuran aktiviti — DIBINA DAN DIWIRING 2026-08-15.**
      `activities.fee_cents` disambung penuh ke `ToyyibPayGateway`
      (instance KEDUA, `"toyyibpay-activity"` — `callbackURL`/`returnURL`
      dikunci semasa `NewToyyibPayGateway` dibina, jadi tak boleh kongsi
      instance dengan yuran pendaftaran ahli). Sekatan `fee_cents != 0`
      dibuang; `registerTx` tulis `payment_status='pending'` (bukan
      `not_required`) untuk aktiviti berbayar, kapasiti tetap direserve
      serta-merta (transaksi kunci sedia ada tak berubah). Checkout
      **berasingan** daripada daftar — `POST /activities/:id/registration`
      dulu (cipta baris), lepas tu `POST /activities/:id/registration/
      checkout` (mulakan bayaran untuk baris yang SUDAH wujud). Klausa
      kelayakan sijil (`fee_cents = 0 or payment_status = 'paid'`) sudah
      betul sejak awal — tiada perubahan diperlukan di situ.

      **Sapuan latar baharu** (`internal/activitysweep`, package
      berasingan drpd reaper/retention — lihat komen pakej untuk sebab)
      bebaskan slot kapasiti yang ditinggalkan — DUA tingkat cutoff
      (bukan satu, selepas Opus verify dedah race, lihat bawah):
      belum-cuba-checkout (`payment_ref` NULL) dibatal lepas 45 minit;
      DAH-cuba-checkout (`payment_ref` wujud, bil ToyyibPay sebenar)
      dibatal lepas **24 jam** — jauh lebih panjang sengaja, elak race
      dengan webhook lewat.

      **Opus verify 2026-08-15 jumpa 4 isu, semua DIBAIKI**:
      - **HIGH** — race sweep-vs-webhook: baris dibatal (slot hilang)
        SEBELUM webhook confirm bayaran tiba → ahli bayar, senyap tiada
        rekod. Dibaiki: cutoff dua-tingkat (24 jam untuk bil sedia ada)
        + `UpdateRegistrationPaymentStatusByPaymentRef` sengaja TIADA
        guard `status<>cancelled` supaya kes cancelled+paid tetap
        tertulis (bukan `pgx.ErrNoRows` senyap) DAN handler log `ERROR`
        eksplisit bila ini berlaku — perlukan semakan manual (padanan
        proses refund manual sedia ada), belum automasi pulih.
      - **MEDIUM** — sapuan tulis ganti `cancelled_at` pada baris yang
        DAH dibatal setiap 15 minit selama-lamanya. Dibaiki: guard
        `status <> 'cancelled'` ditambah pada kedua-dua query sapuan.
      - **LOW** — checkout berulang tulis ganti `payment_ref`, bil lama
        jadi yatim (webhook tak jumpa padanan kalau bayaran sampai ke
        situ). Sengaja TAK disekat (block penuh akan kunci ahli yang
        bil pertama tamat tempoh daripada cuba lagi, lebih teruk) — log
        sahaja untuk kelihatan dalam pemantauan.
      - **UX (bukan bug, tapi disyorkan Opus)** — halaman detail
        aktiviti tak dedahkan `payment_status` pendaftaran (respons
        backend tak bawa medan tu), jadi ahli yang daftar aktiviti
        berbayar tak nampak apa-apa isyarat kena bayar sehingga mereka
        navigasi ke "Aktiviti Saya" — pada masa tu mungkin dah terlepas.
        Dibaiki (minimum, bukan repair penuh): mesej kejayaan daftar
        kini sebut eksplisit "aktiviti ini berbayar, sila selesaikan di
        Aktiviti Saya" bila `feeCents > 0` (`activity_detail_page.dart`).
        Repair penuh (dedahkan `payment_status` terus pada respons
        detail) masih belum dibuat — halaman detail masih tak boleh
        bezakan "dah bayar" drpd "belum bayar", cuma tahu "perlu bayar".

      **Flutter**: `my_activities_page.dart` sahaja yang papar status
      bayaran + butang "Bayar Yuran Aktiviti" (satu-satunya tempat data
      `payment_status` sedia — via `GET /me/activities`). Halaman detail
      aktiviti tak boleh (lihat UX di atas).
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
- **L28 — access token JWT stateless kekal sah sampai 15 minit lepas
  demote/tukar role.** `setMemberStatus` (`profile.go:712`) revoke
  refresh token dalam transaksi, `UpdateMemberRole` tak revoke apa-apa —
  tapi access token yang dah dikeluarkan kekal sah sampai `exp` tanpa
  kira. **Keputusan 2026-08-15**: diterima sebagai risiko, bukan
  dibaiki. Fix sebenar (semak status/role pada SETIAP request, cth
  lajur `token_valid_after` pada `profiles` disemak dalam `RequireAuth`)
  bermakna satu query DB tambahan pada SETIAP permintaan berautentikasi
  di seluruh app — kos berterusan untuk kes yang jarang (admin demote/
  tukar role ahli) dan tetingkap dedah dihadkan 15 minit (TTL access
  token sedia ada). Nisbah kos-faedah tak berbaloi drpd bina token-
  revocation infra penuh. Semak semula kalau `AccessTokenTTL` pernah
  dipanjangkan jauh drpd 15 minit.
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
      `verifyImage` laluan sebenar. **Dibaiki (pusingan 1)**: `Range`
      ditukar ke `bytes=0-65535`.

      **Verifikasi 2026-08-15 dedah pusingan 1 tak cukup untuk JPEG.**
      64KB cukup untuk PNG (IHDR di bait 8-33) tapi JPEG boleh tolak
      penanda SOF0/SOF2 (bawa lebar/tinggi) lepas berbilang segmen APPn
      (EXIF/ICC/XMP, sehingga 64KB setiap satu) yang sengaja dipadatkan —
      dibuktikan dengan bukti-konsep: JPEG 8000×8000 (750KB, bawah had
      5MB) dengan APP1 65KB decode gagal (`unexpected EOF`),
      `verifyDimensions` gagal-terbuka `return nil`, had dimensi terus tak
      terpakai. Ahli approved+verified masih boleh hantar decode-bomb.
      **Dibaiki (pusingan 2)**: `Range` dan had `io.ReadAll` ditukar ke
      `MaxImageSizeBytes` (5MB) — padan terus had saiz yang dah
      dikuatkuasakan di tempat lain (`VerifyImageSize`), jadi SOF0
      sentiasa dalam julat baca untuk MANA-MANA fail yang lulus had saiz
      sedia ada. Kos: bacaan R2 naik drpd 64KB tetap ke saiz fail sebenar
      (max 5MB) — diterima sebab R2→compute egress percuma (Cloudflare),
      jalan sekali semasa verify muat naik (bukan setiap tatal feed), dan
      gambar client selalunya jauh lebih kecil (client dah kecilkan ke
      2048px). Ujian unit (`dimensions_test.go`) lulus; **belum disahkan
      via live test** — `TestR2LivePermissions` wujud tapi tak uji kes
      JPEG APPn-padded khusus, pertimbang tambah kalau nak bukti hujung-
      ke-hujung terhadap R2 sebenar.
- [x] **L17 — tiada had panjang pada medan teks bebas** (post/comment
      content, `display_name`, `phone`, activity `title`/`location_name`,
      `onesignal_id`) — hanya had body 1MB global. `display_name` 1MB
      terbenam dalam setiap post/comment feed (`posts_common.go:263-278`)
      **dan** disalin ke `activity_certificates.recipient_name`, dipaparkan
      pada halaman verify sijil **awam tanpa auth**. `donations.go:48-56`
      satu-satunya tempat yang ada had — sepatutnya jadi corak untuk yang
      lain.

      **Verifikasi 2026-08-15**: had ditegakkan, `gin validator` guna
      rune count (bukan byte). Satu bug baharu dijumpai dalam pelaksanaan
      manual `optional[T]` PATCH — lihat **L23** di bawah. Turut dedah
      medan yang masih terbuka (bukan dalam skop L17 asal): lihat **L24**.
- [x] **L18 — spam notification/push tanpa had melalui like berulang.**
      `LikePost` (`queries/likes.sql`) `on conflict do nothing`, tapi
      handler post panggil `notifyOwner` tanpa syarat selepas SETIAP
      panggilan (tak boleh bezakan insert baharu vs conflict). Tiada rate
      limit pada `POST /posts/:id/like`. Gelung endpoint ni = harassment
      bersasar (push OneSignal berulang) + pertumbuhan jadual
      `notifications` tanpa had. `CommentHandler.Like` betul (tak
      notify). Fix: `LikePost` jadi `:execrows`/`returning`, notify hanya
      bila baris benar-benar masuk.

      **Verifikasi 2026-08-15**: `likes.sql.go` disahkan sync dengan
      `queries/likes.sql` (`sqlc generate` tiada drift), `posts.go` satu-
      satunya caller `LikePost` seluruh repo, `CommentHandler.Like` tak
      tersentuh. Corak sama masih terbuka pada comment create — lihat
      **L25**.
- [x] **L19 — draft activity boleh dibaca ahli biasa melalui ID.**
      `ActivityHandler.List` gate `status=draft` di belakang
      `requireManagement` (`activities.go:352-359`) tapi
      `ActivityHandler.Get` (`activities.go:451`) tiada semakan status
      langsung — `GetActivityByID` tapis `deleted_at is null` sahaja.
      Ahli lulus dengan UUID aktiviti boleh baca draf penuh (tajuk,
      kapasiti, yuran, sesi, bilangan pendaftaran). Terhad oleh
      ketidaktekaan UUIDv4, tapi peraturan kebenaran yang `List`
      kuatkuasakan langsung tak wujud pada laluan by-id. **Sebenarnya
      sudah dibaiki dalam kod sedia ada sebelum audit ni** (bukan hasil
      `aa0e6c5`) — `activities.go:463-473` semak `statusDraft` +
      `authz.IsManagement`, pulang 404. Disahkan semula 2026-08-15, masih
      betul.
- [x] **L20 — `/auth/refresh` dan `/auth/logout` tiada rate limit.**
      Satu-satunya laluan auth tanpa auth yang tak kena `authRateLimiter`
      (register/login/verify semua kena). Bukan risiko teka token (opaque
      256-bit), tapi laluan miss `Refresh` buat SHA-256 + `UPDATE
      ... RETURNING` + `GetRefreshTokenByHash` kedua + mungkin `DELETE
      ... where family_id` (`auth.go:277-296`) — write-path amplifier
      Postgres tanpa had kadar.

      **Verifikasi 2026-08-15 dedah kesan sampingan sebenar** — lihat
      **L26**.
- [x] **L22 — resit donation PDF cetak tarikh salah.** `donations.go:248`
      set `paidAt := time.Now()` semasa proses webhook, bukan baca
      timestamp PaymentIntent Stripe sebenar. Stripe retry/kelewatan
      penghantaran webhook = tarikh salah pada dokumen kewangan yang
      dihantar email kepada penderma, tak boleh dibetulkan selepas fakta.

      **Verifikasi 2026-08-15**: plumbing `WebhookEvent.PaidAt` betul,
      satu-satunya tapak binaan (`stripe.go:117`), sampai PDF+emel dengan
      betul. Dua residual LOW dijumpai — lihat **L27**.

### Audit lanjutan — verifikasi + penemuan baharu (2026-08-15)

Pusingan kedua: Opus disuruh sahkan L16-L20/L22 di atas SEKALI GUS cari
baharu, dengan arahan eksplisit fix terus HANYA kalau CRITICAL (boleh
eksploit sekarang, oleh aktor tanpa/rendah keistimewaan, laluan jelas ke
kebocoran data/eskalasi/kerugian kewangan/auth bypass penuh). Tiada satu
pun penemuan capai bar tu — L16 (pusingan 2, JPEG) dibaiki terus sebab
ia HIGH security gap yang jelas walau bawah bar CRITICAL ketat; yang lain
direkod sahaja.

- [x] **L23 — regresi PATCH activity title/location: byte vs rune count
      (MEDIUM, DIBAIKI).** `activities.go:918/922` guna `len()` (BAIT)
      untuk had manual PATCH `optional[T]`, sedangkan POST (`max=200`/
      `max=300`) guna gin validator yang kira RUNE. Title dicipta via POST
      dengan 200 aksara Melayu/emoji/CJK (sampai 800 bait) sah — tapi
      SETIAP `PATCH /activities/:id` seterusnya 400, TERMASUK PATCH yang
      tak sentuh title langsung (`merge` salin balik `before.Title`).
      Baris jadi tak boleh di-PATCH selama-lamanya. Ini regresi `aa0e6c5`
      sendiri, bukan gap lama. Fix: `utf8.RuneCountInString`.
- [ ] **L24 — medan lain masih tiada had panjang** (di luar skop L17
      asal): `activityRequest.Description`/`LocationAddress`
      (`activities.go:526,528`, POST DAN PATCH), `Cancel.Reason`
      (`activities.go:1053` — **turut disuntik terus ke body push
      notification yang di-fanout ke setiap pendaftar**,
      `activities.go:1125`), `revokeCertificateRequest.Reason`
      (`activity_certificates.go:550`), `markAttendanceRequest.Reason`
      (`activity_attendance.go:220`).
- [ ] **L25 — spam notification/push tanpa had melalui comment create**
      (corak sama L18, terbuka pada laluan berimpak lebih tinggi).
      `comments.go:99-103` panggil `notifyOwner` pada SETIAP
      `POST /posts/:id/comments`, route ni **tiada rate limiter**
      (`router.go:124`). Gelung = harassment bersasar + pertumbuhan
      `notifications`/`comments` tanpa had. `POST /posts` dan `PATCH /me`
      turut tak dihad kadar.
- [ ] **L26 — bucket rate-limit auth kini dikongsi 7 route, IP-only.**
      `authRateLimiter` (`router.go:69`) satu instance dikongsi
      register/login/refresh/logout/verify-email (confirm+request), had
      5 burst/1 setiap 12s, kunci `c.ClientIP()` sahaja. Risiko IP
      dikongsi (wifi kelab/CGNAT operator): ~20 ahli buka app serentak
      lepas idle = ~20 refresh serentak dalam satu burst, 15 kena 429
      daripada bucket yang login turut ambil. **Semak** apa `marc_flutter`
      buat dengan 429 pada `/auth/refresh` — kalau ia treat macam token
      tak sah, ini mass forced-logout. `/auth/logout` boleh 429 juga,
      tinggalkan refresh token hidup. Turut lemahkan andaian L21 (5/min
      diandaikan khusus untuk enumerasi Register). Cadangan: bucket
      bernama berasingan (lebih longgar) untuk refresh/logout.
- [ ] **L27 — dua residual tarikh LOW pada resit donation.**
      (a) `pi.Created` (`stripe.go:117`) ialah masa PaymentIntent
      **dicipta**, bukan masa bayar sebenar — untuk DuitNow QR/FPX
      pembayar mungkin bayar minit kemudian; `LatestCharge.Created` lebih
      tepat. (b) `donations.go:348` format `paidAt` dalam server-local
      (UTC) manakala PDF lampiran cetak MYT — badan emel dan lampirannya
      sendiri kini lari 8 jam.

### Nota reka bentuk keselamatan — yuran pendaftaran ToyyibPay (belum dibina, rujukan awal)

Bukan kelemahan hidup (ciri belum wujud) — direkod SEKARANG supaya tak
terlepas pandang bila skema/gate sebenar dibina (lihat bahagian Payment
untuk keputusan produk yang masih terbuka):

- **Status `approved` MESTI tak boleh diberi tanpa bayaran disahkan.**
  Kalau gate diletak selepas kelulusan management (opsyen dalam bahagian
  Payment), pastikan tiada laluan (termasuk endpoint pentadbiran sedia
  ada, `setMemberStatus`/`UpdateMemberRole`) boleh set `status='approved'`
  terus tanpa semak medan bayaran — atau management akan boleh (secara
  tak sengaja) meluluskan ahli yang belum bayar melalui laluan lama yang
  tak tahu pasal syarat baharu ni.
- **Race: webhook/poll confirm vs proses kelulusan serentak.** Kalau
  bayaran disahkan (async, poll `getBillTransactions`) SEMASA management
  tengah proses kelulusan manual pada masa yang sama, pastikan kedua-dua
  laluan tulis medan bayaran/status dalam transaksi yang kunci baris
  (padanan pola `LockActivityForRegistration` sedia ada), bukan dua
  `UPDATE` bebas yang boleh berlumba.
- **`VerifyWebhook` ToyyibPay buat panggilan rangkaian keluar** (poll
  `getBillTransactions`, lihat bahagian Payment) — endpoint webhook jadi
  bergantung latency ToyyibPay sendiri. Pastikan endpoint webhook tetap
  pulang cepat kepada ToyyibPay (guna goroutine/queue kalau perlu) supaya
  ToyyibPay tak retry berulang atas timeout, bukan proses segala-galanya
  synchronous dalam satu request HTTP.

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
