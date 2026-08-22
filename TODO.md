# MARC Backend — TODO

Kerja yang **belum siap** sahaja. Sejarah penuh (keputusan, gotcha, hasil
verifikasi setiap stage) ada dalam git log — cari ikut nombor stage.
Struktur kod: [`ARCHITECTURE.md`](./ARCHITECTURE.md).
Schema & migration: [`DATABASE.md`](./DATABASE.md).
Spec, plan, laporan audit penuh: [`docs/`](./docs/).

Stage 0–15 siap: auth custom, RBAC, posts/comments/likes, kelulusan ahli,
push, upload R2, donation Stripe, jejak audit, pembersih storan, polisi
simpanan. Modul aktiviti (backend penuh) siap — jurangnya direkod di bawah.

---

## Permintaan pemadaman akaun (Google Play Console) — v1 REQUEST-sahaja, 2026-08-19

Google Play Console mewajibkan app yang sokong penciptaan akaun sediakan
cara ahli **meminta** pemadaman akaun + data berkaitan. Dibina bawah
tekanan deadline publish — skop sengaja diminimumkan.

**Dibina**:
- Jadual `account_deletion_requests` (migration
  `20260819130000_create_account_deletion_requests.sql`) — `user_id`
  unik → `users(id)`, `status` (`pending`/`completed`), `requested_at`,
  `completed_at`.
- `POST /me/deletion-request` (`ProfileHandler.RequestAccountDeletion`,
  `profile.go`) — grup `protected` (ahli mana-mana status, termasuk
  `pending`, boleh minta). Idempoten: panggilan berulang pulang 200 dgn
  data permintaan SEDIA ADA, bukan ralat (padanan pola
  `AddBlockedEmailDomain`/`ApproveProfile`). Tulis catatan audit
  (`audit.EntityAccountDeletionRequest`) sekali sahaja — bila baris
  BAHARU dicipta, bukan pada panggilan ulang.
- Rate limiter `account-deletion-request` (3s/10, padanan pola
  `profileUpdateRateLimiter`).

- [ ] **TIADA auto-purge/cascading delete lagi** — ni GAP SEBENAR, bukan
      "belum sempat". Sengaja tak dibina dalam sprint ni: reka bentuk +
      uji pemadaman post/komen/like/derma/bayaran/pendaftaran aktiviti
      merentas jadual dgn selamat (FK, R2 orphan, refund/rekod kewangan
      yg kena kekal utk audit) perlukan lebih masa drpd yang ada. Buat
      masa ni staff kena tindak baris `pending` dalam
      `account_deletion_requests` SECARA MANUAL (akses DB terus) —
      padam/anonim profil & data berkaitan ikut budi bicara, lepas tu
      set `status='completed'`, `completed_at=now()`. Fast-follow
      diperlukan: proses purge automatik (atau checklist manual rasmi)
      sebelum jumlah permintaan jadi banyak utk staff urus tangan.

---

## Onboard ahli lama (kertas/manual) — BRAINSTORM BELUM SIAP, tiada spec/plan lagi

Ahli sedia ada (~ratusan) yang dah daftar, dah bayar, dan dah dapat
no. ahli SEBELUM app ni wujud — proses lama 100% manual/kertas. Client
ada senarai digital (spreadsheet) rekod ni. Nak integrate mereka ke app
tanpa suruh mereka bayar semula atau tukar no. ahli mereka. Sesi
brainstorm (2026-08-16) terhenti di pertengahan reka bentuk data model
— rujuk juga `../marc_flutter/TODO.md` bahagian sama utk status sisi
Flutter.

**Keputusan disahkan (Q&A dgn pemilik produk):**
- Skala: ~ratusan ahli lama.
- Format no. ahli lama: **belum pasti, client belum tanya lagi**. Reka
  bentuk MESTI agnostik format — no. ahli lama disimpan sebagai
  rentetan legap, TIADA parsing/validasi struktur/regex format.
- Ahli lama LANGSUNG skip yuran pendaftaran ToyyibPay (`registration_
  payments`/gate di `ApproveMember`, lihat bahagian "Payment —
  sambungan" di bawah) — dah bayar manual sebelum ni.
- Cara claim: medan "No. Ahli Lama" PILIHAN semasa daftar (bukan kod
  jemputan drpd import, bukan padanan manual management selepas
  daftar). App cari padanan dlm senarai diimport secara real-time;
  management sahkan padanan tu betul semasa langkah kelulusan
  (`ApproveMember`) sedia ada — bukan langkah/skrin kelulusan baharu.
- Import senarai lama: **one-off sahaja**, client hantar satu senarai
  muktamad — script/migration sekali guna, BUKAN endpoint/UI import
  berulang.
- No. ahli tak dijumpai dlm senarai diimport → **sekat submission
  pendaftaran**, ralat jelas kpd user. User boleh cuba lagi ATAU
  kosongkan medan & daftar sebagai ahli baharu biasa (laluan sedia ada,
  bayar yuran macam biasa).

**Reka bentuk data model dicadang (BELUM disahkan, belum ditulis
migration):**

```sql
-- Jadual berasingan, TIDAK gabung terus ke profiles — import senyap
-- sehingga seseorang betul-betul claim, elak profil hantu wujud
-- sebelum ada orang sebenar daftar.
create table legacy_members (
  id uuid primary key default gen_random_uuid(),
  legacy_no text not null unique,  -- rentetan legap, format apa-apa pun
  full_name text not null,
  phone text,
  paid_note text,                   -- teks bebas drpd sheet (jumlah/tarikh/no resit kalau ada)
  claimed_by_user_id uuid unique references users(id),
  claimed_at timestamptz
);

alter table profiles
  add column legacy_member_id uuid references legacy_members(id),
  add column registration_fee_exempt boolean not null default false;
```

`profiles.member_id` (unique text sedia ada) diset kpd **legacy_no
verbatim** untuk profil claim jenis ni — BUKAN format app
`MARC{YYYY}/{MM}/{seq}` (`generateMemberID`, `auth.go:254`). Ini
padankan keperluan "guna nombor ahli lama, bukan format kita" terus —
mana-mana UI yang dah papar `member_id` (resit, senarai ahli, profil)
tak perlu ubah, tak perlu tahu konsep "legacy" pun wujud.

**Titik sambungan gate yuran**: `setMemberStatus`/`ApproveMember`
(`profile.go:644`) kini semak `HasSucceededRegistrationPayment` sebelum
lulus (Stage ToyyibPay, siap 2026-08-15). Untuk ahli lama, gate yg sama
patut terima `profiles.registration_fee_exempt = true` sebagai laluan
ALTERNATIF (OR), bukan gate baharu berasingan — padan pola exemption
ahli SEDIA ADA sebelum ciri yuran wujud (no-op check `target.Status ==
status` yg dah ada).

**Belum diputuskan / belum dibincang langsung:**
- Endpoint/payload pendaftaran sebenar (medan `legacy_member_no`
  pilihan pada `POST /auth/register`?) — belum direka.
- Response bila padanan berjaya/gagal semasa daftar (real-time semak
  sblm submit, atau semak lepas submit dgn ralat 422?).
- Kes pertindihan (dua akaun cuba claim `legacy_no` sama serentak) —
  unique constraint `claimed_by_user_id`/`legacy_no` bagi perlindungan
  DB-level asas, tapi flow UX/ralat belum direka.
- Macam mana skrin kelulusan management (`Members` `?status=pending`,
  `profile.go:354`) papar butiran padanan lama (nama/telefon/paid_note
  dari `legacy_members`) untuk semakan admin sblm lulus.
- Skrip import sebenar (format CSV/Excel sumber, cara jalan sekali,
  validasi sblm insert).

## Web Flutter (`app.marc.hafizbahtiar.com`) — CORS API BELUM WUJUD SECARA MENYELURUH (2026-08-16)

`marc_flutter` sedang digarap ke web (fasa 1: bina+deploy, lihat
`../marc_flutter/TODO.md`) — sasar `app.marc.hafizbahtiar.com` di
Railway. **Ditemui semasa semakan**: `middleware.CORS` (`internal/http/
middleware/cors.go`) kini cuma dipasang pada DUA laluan sempit —
`POST /auth/verify-email/confirm` (`router.go:99-101`) dan cert-verify
awam (`router.go:270`) — `r.Use(...)` global (`router.go:76`) TIADA
CORS langsung. Ini OK setakat ni sebab semua client dulu app native
(mobile HTTP client tak tertakluk sekatan CORS pelayar). Bila web mula
panggil API drpd origin browser sebenar, **hampir SEMUA endpoint lain**
(login, feed, profile, dsb) akan disekat CORS pelayar — ini BUKAN "tambah
satu origin dlm senarai", ini kerja wiring CORS merentas seluruh
`r.Use(...)` (atau grup-grup utama) yg belum dibuat langsung.

- [ ] **Reka bentuk + wiring CORS global belum start.** Perlu keputusan:
      CORS pada `r.Use(...)` teras (semua laluan) vs per-grup, dan
      `corsAllowedOrigins` (config sedia ada, `cfg.CORSAllowedOrigins`)
      perlu masuk `https://app.marc.hafizbahtiar.com` (dan
      staging/local kalau web run tempatan jugak).
- [ ] **Keputusan API_BASE_URL fasa 1 web belum dibuat** (staging atau
      prod) — tentukan environment mana perlu CORS origin ni dulu,
      lihat `../marc_flutter/TODO.md`.

## Perlu tindakan kau (bukan kod)

- [ ] **Deploy environment `production` Railway** — `staging` sahaja live.
- [ ] **Migrate data lama dari Supabase** (2 profiles, 4 roles).
- [x] **MATIKAN Public Development URL r2.dev di Cloudflare — DIBUAT
      2026-08-15** (terus di Cloudflare dashboard, bukan kod). `R2_PUBLIC_URL`
      boleh dikosongkan di env sekarang (belum disahkan sama ada dah
      dibuat).
- [ ] **Rotate kunci test Stripe** yang sempat masuk git (commit `c170391`,
      dah di-amend sebelum push — tapi rotate tetap lebih selamat).
- [ ] **Sambungkan Redis ke marc-go**: tambah pemboleh ubah rujukan
      `REDIS_URL = ${{Redis.REDIS_URL}}` pada perkhidmatan marc-go.
      Perkhidmatan Redis wujud tapi app tak nampak. Lihat bahagian Redis
      di bawah untuk sama ada ia berbaloi buat masa ni.
- [ ] **Cipta 3 akaun prod utama** (keputusan produk 2026-08-15) —
      DAFTAR macam biasa (`/auth/register` menerusi app, atau Flutter),
      sahkan email, lepas tu SATU superadmin sedia ada (atau kau sendiri
      terus dlm psql sebelum ada superadmin lain) tukar role menerusi
      skrin "Tukar role" management (`members_page.dart` → profile
      `Ahli Pending`/senarai Ahli, cari akaun, tukar role). **JANGAN**
      daftar terus dgn migration/SQL seed — password perlu dipilih
      pemilik akaun sebenar, dan bcrypt hash tak patut dijana manual.
      - [ ] `hafizbahtiar98@gmail.com` → role `superadmin`
      - [ ] `google@yopmail.com` → role `tester` (akaun review Google Play)
      - [ ] `apple@yopmail.com` → role `tester` (akaun review App Store)

      **PERHATIAN keselamatan**: dua akaun tester guna domain
      `yopmail.com` — domain emel PELUPUSAN (disposable), yang item
      **"Sekat pendaftaran emel pelupusan"** di bawah akan block kalau
      diimplement SEBELUM 3 akaun ni dicipta. Cipta akaun ni DAHULU
      (atau tambah pengecualian eksplisit utk dua alamat ni) sebelum
      sekatan disposable-email diaktifkan — jangan kunci diri sendiri
      keluar drpd akaun tester yang kau perlukan utk app store review.

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

## Skrin bayaran (2026-08-15) — sejarah + penapisan derma superadmin

- [x] **`GET /me/payments` + `GET /admin/payments` DIBINA 2026-08-15.**
      Dahulu status bayaran cuma terselit dalam respons lain (`/me`,
      `/me/activities`) — tiada satu page pun untuk lihat sejarah.
      `/me/payments` (route `protected`, BUKAN `approved` — ahli `pending`
      yang dah bayar yuran pendaftaran mesti boleh tengok sejarah sendiri
      sebelum diluluskan, sama rasional dengan checkout sendiri) pulangkan
      `registration_fee[]` + `activity_fees[]` (dua query baharu
      `ListMyRegistrationPayments`/`ListMyActivityPayments`).
      `/admin/payments` (management sahaja, `authz.IsManagement`) papar
      `payment_logs` (jadual sedia ada sejak Stage bayaran, sebelum ni
      TAK PERNAH diwiring ke endpoint) dengan pagination keyset
      `before_id` + tapisan `module`. `paymentLogItem` sengaja TIADA
      `raw_payload` — medan tu simpan payload gateway MENTAH (boleh bawa
      PII pembayar), tak didedahkan menerusi API. Flutter:
      `lib/features/payments/` (dua page baharu + provider), pautan dari
      Profile page (dikumpul semula ke kad "Kewangan"/"Pengurusan").

      **Opus verify 2026-08-15**: tiada bug auth/IDOR. 3 isu kecil
      dibaiki: (1) `/me/payments` asalnya `approved` — pindah ke
      `protected`; (2) kes currency tak konsisten (`"myr"` vs `"MYR"`
      antara modul) — Flutter `.toUpperCase()`; (3) admin page hardcode
      "RM" untuk semua modul (`payment_logs` tiada lajur currency) —
      diterima sengaja, seluruh app MYR sahaja.

- [x] **Modul `donation` disekat kepada superadmin sahaja dalam
      `/admin/payments` (keputusan produk 2026-08-15).** Management biasa
      (supervisor/manager/admin) TAK nampak baris donation walau tapisan
      "Semua" dipilih — dikuatkuasakan di Go (`payments.go` `ListAll`,
      `authz.IsAtLeastRole(..., "superadmin")`), bukan sekadar sembunyi cip
      Flutter: `?module=donation` terus oleh bukan-superadmin tetap 403.
      `ListRecentPaymentLogs` diubah drpd `sqlc.narg('module')` tunggal ke
      `sqlc.arg('modules')::text[]` (`= any(...)`) — handler kira senarai
      modul dibenarkan, SQL buta pada sebab kebenaran.

- [x] **Resit PDF muat turun (dalam app) — DIBINA 2026-08-15.** Sebelum
      ni, yuran pendaftaran/aktiviti TAK PERNAH hantar resit langsung
      (cuma donation ada, via emel semasa webhook) — ahli tiada cara
      dapatkan bukti bayaran selain tangkap skrin. Keputusan produk: ahli
      boleh tengok + muat turun resit BILA-BILA (bukan cuma sekali via
      emel), walau resit dah dihantar emel sebelum tu.

      **Reka bentuk**: PDF dijana SEMULA setiap panggilan (bukan
      disimpan/ditanda "sudah dijana" dalam DB) — resit deterministik
      drpd data yang dah tersimpan dlm `registration_payments`/
      `activity_registrations`/`donations`, jadi tiada keadaan tambahan
      utk diselaraskan. Muat naik R2 ke kunci STABIL
      (`receipts/{registration,activity,donation}/{id}.pdf`, tulis ganti
      setiap kali — idempoten), pulang URL bertandatangan (padanan corak
      `CertificateHandler.Download` — R2 sampaikan fail, backend bukan
      bottleneck lebar jalur, Flutter buka terus dgn `url_launcher`,
      tiada pakej muat turun/storan baharu diperlukan).

      **Fail**: `internal/receipt/receipt.go` (`FeePayment` struct +
      `GenerateFeePDF` — DIASINGKAN drpd `Donation`/`GeneratePDF` sengaja,
      label/nota berbeza — yuran ialah bayaran RASMI kelab, bukan
      sumbangan peribadi kpd pembangun; laluan donation lama tak
      disentuh); `internal/http/handlers/payments.go`
      (`RegistrationReceipt`/`ActivityReceipt`/`DonationReceipt`, hanya
      baris SENDIRI + status berjaya/dibayar sahaja — 409 kalau
      pending/gagal); query baharu `GetMyRegistrationPaymentByID`/
      `GetMyActivityFeeByID`/`GetMyDonationByID` (kesemuanya skop
      `user_id = caller`); route `GET /me/payments/{registration,
      activity,donation}/:id/receipt` (`protected`, padanan
      `/me/payments`); `lib/features/payments/` (butang muat turun per
      baris di `payment_history_page.dart`, `PaymentReceiptRepository`).

      **Had kadar**: `payment-receipt` (6s/5, padanan `uploadRateLimiter`
      — setiap panggilan PutObject R2, bukan sekadar bacaan DB).

      **Ketidaktepatan tarikh DITERIMA sengaja (bukan bug baharu)**:
      `registration_payments`/`activity_registrations` tiada lajur
      `paid_at` khusus (cuma `created_at`/`registered_at`, waktu baris
      DICIPTA semasa pending — bukan waktu bayaran DISAHKAN), dan resit
      donation guna `created_at` sebagai anggaran (`paid_at` Stripe
      sebenar cuma transient dalam webhook, tak pernah disimpan —
      TODO.md L22/L27 dah rekod ni). Resit yuran ikut anggaran yang sama
      — konsisten dgn gelagat sedia ada, bukan regresi baharu. Kalau
      ketepatan jadi penting kelak, tambah lajur `paid_at` pada
      ketiga-tiga jadual.

      **Belum dibuat**:
      - [ ] Tiada UI senarai "Sejarah Derma Saya" langsung — endpoint
            `DonationReceipt` DIBINA + diuji (`go test` lulus) tapi
            TAK BOLEH dicapai dari Flutter (tiada id sumber). Donation
            ahli log masuk pun tak muncul dalam `/me/payments` —
            keputusan sengaja sesi ni (skop asal 2 modul kelab sahaja,
            donation dah ada laluan emel). Kalau nak lengkapkan: perlu
            `ListMyDonations` query + tambah ke respons `/me/payments`
            + seksyen baharu `payment_history_page.dart`.
      - [ ] Opus verify belum dijalankan utk ciri resit ni khusus
            (dijadualkan lepas ni).

## RBAC — role `admin` & `tester` (2026-08-15)

Dua role baharu (migration `20260815070000_seed_admin_tester_roles.sql`),
keputusan produk:

- **`admin`** — tier baharu **antara manager(60) dan superadmin(100)**,
  rank **80**, category `management`. Lebih kuasa drpd manager tapi TAK
  automatik dapat semua kuasa superadmin — semakan yang secara eksplisit
  menuntut rank superadmin (cth modul donation di atas) kekal luar capaian
  admin. Semua gate `authz.IsManagement`/`authz.IsAtLeastRole("manager")`
  sedia ada terus terpakai tanpa ubah kod (rank 80 >= 60).
- **`tester`** — akaun review Google Play/App Store, rank **5** (bawah
  ahli=10, SENGAJA bukan sama — elak perlanggaran dlm perbandingan rank
  cth pemberian role), category `ahli`. Berkelakuan macam ahli biasa untuk
  SEMUA capaian (daftar, post, like, daftar aktiviti, lihat skrin) —
  reviewer perlu uji aliran app sebenar utk lulus review. **Sekatan
  tunggal**: `middleware.BlockTesterWrites` (query baharu
  `GetRoleKeyByUserID`) dipasang pada tiga route checkout bayaran
  (`/registration-payments/checkout`, `/activities/:id/registration/
  checkout`, `/donations/checkout`) — tolak 403 kalau role="tester", gagal
  TERTUTUP (500) kalau query role gagal. Tindakan pengurusan (luluskan
  ahli, tukar role, urus kategori, terbit/batal aktiviti) TAK perlu
  disekat berasingan — category `ahli` dah cukup, semua gate
  `authz.IsManagement` sedia ada tolak tester terus.

  **Kesan sampingan rank=5 (bukan bug, sengaja diterima)**:
  `visibleRankCeiling` (`profile.go`) beri tester ceiling nampak ahli
  (rank<=10) sahaja — LEBIH ketat drpd ahli sebenar (yang nampak sampai
  supervisor, rank<=50). Lebih restriktif, bukan kurang — tiada risiko
  keselamatan.

  **Belum dibuat**:
  - [ ] Cipta akaun `tester`/`admin` SEBENAR (migration cuma tambah row
        `roles`, bukan profil/user) — kena daftar user biasa lepas tu
        tukar role menerusi skrin "Tukar role" management (`members_page
        .dart`, pemilihan role dinamik guna `/roles` sedia ada, tiada kod
        tambahan perlu).
  - [ ] `marc_flutter`: tiada UI khas untuk role `admin`/`tester` — kedua-
        duanya guna skrin sedia ada (admin dapat semua akses management
        biasa secara automatik via `isManagement`; tester dapat akses
        ahli biasa). Kalau `admin` perlu skrin/kuasa BEZA drpd manager
        kelak (cth akses CRUD kategori yang skrg "manager ke atas"), semak
        `isManagerOrAboveProvider`/`requireManagerOrAbove` — rank 80 >= 60
        dah lulus secara automatik, tiada kerja tambahan.
  **Opus verify 2026-08-15**: backend selamat — tiada laluan tester
  boleh selesaikan bayaran sebenar (`BlockTesterWrites` disahkan didaftar
  SEBELUM handler pada ketiga-tiga route; cuma 3 titik `gw.CreatePayment`
  wujud seluruh repo, padan 3 route yang dikawal), rank `admin`(80) lulus
  tepat setiap `IsAtLeastRole("manager")` dan gagal tepat setiap
  `IsAtLeastRole("superadmin")`, `modules` array sekatan donation tiada
  laluan kosong/nil.

  **1 bug SEBENAR dijumpai dan DIBAIKI (Flutter, bukan keselamatan)**:
  `isSuperAdminProvider`/`isManagerOrAboveProvider` cari role SENDIRI
  dalam `rolesProvider` (`GET /roles`) — tapi backend `ListRoles`
  (`profile.go:521`) SENGAJA tolak keluar mana-mana role dengan
  `rank >= caller.RoleRank` (skop endpoint tu: pemilih "tukar role",
  cuma boleh assign ke BAWAH). Kesan: superadmin tulen TAK PERNAH jumpa
  dirinya dalam senarai sendiri (topRank sentiasa ditolak, utk SESIAPA),
  jadi cip "Derma" tak pernah muncul walau utk superadmin sebenar; sama
  utk manager tulen (rank sendiri == rank dicari, ditapis keluar).
  Dibaiki: jalan pintas `profile.roleKey == 'manager'`/`'superadmin'`
  SEBELUM cuba carian dinamik (`manage_providers.dart`,
  `payment_providers.dart`) — carian dinamik kekal untuk kes ATAS
  ambang (admin semak "manager ke atas", superadmin semak diri sendiri
  dah short-circuit).

## Sekat pendaftaran emel pelupusan (disposable email) — DIBINA 2026-08-15

Ditemui semasa sediakan akaun tester (`google@yopmail.com`/
`apple@yopmail.com`) — istilah industri: **"disposable email"** /
"temporary email" / "throwaway email" (BM: emel pelupusan/sekali-guna).
Sebelum ni `/auth/register` terima MANA-MANA alamat berformat sah,
termasuk domain yang sengaja wujud untuk pendaftaran sekali-guna/spam.

**Kenapa penting**: `email_verified` (Stage 8) cuma buktikan "seseorang
boleh terima SATU emel", bukan "identiti sebenar/kekal" — alamat
pelupusan lulus proses tu dengan sempurna. Akaun guna emel pelupusan =
tiada cara hubungi ahli tu lagi selepas domain tamat/reset (kebanyakan
mati dlm beberapa jam-hari).

**Dibina** (dua lapisan, keputusan produk 2026-08-15):

1. **Senarai statik terbenam** — `internal/disposableemail/domains.txt`
   (`//go:embed`, ~8,200 domain drpd
   `github.com/disposable-email-domains/disposable-email-domains`,
   `map[string]bool` O(1)). PERTAHANAN UTAMA.
2. **Jadual DB `blocked_email_domains`** (migration
   `20260815090000_create_blocked_email_domains.sql`) — tambahan MANUAL
   management, endpoint CRUD `GET/POST /admin/blocked-email-domains`,
   `DELETE /admin/blocked-email-domains/:domain` (management sahaja,
   `internal/http/handlers/blocked_email_domains.go`).
3. Semakan di `POST /auth/register` (`auth.go`, sebelum sebarang kerja
   DB) — tolak 400 "sila guna alamat emel kekal, bukan emel pelupusan/
   sekali-guna" kalau domain wujud dlm senarai statik ATAU jadual DB.
4. **Pengecualian dua akaun tester** — allowlist ALAMAT PENUH (bukan
   domain) dlm `internal/disposableemail/disposableemail.go`
   (`allowedEmails`), keputusan eksplisit (bukan "cipta akaun dulu"):
   sekatan boleh aktif bila-bila tanpa bergantung turutan deploy.
   `randomperson@yopmail.com` masih disekat — cuma dua alamat tepat tu
   yang dipintas.

**Diuji**: `internal/disposableemail/disposableemail_test.go` (unit,
tiada DB — allowlist menang atas domain pelupusan, domain lain pada
yopmail tetap disekat, senarai statik berjaya embed >1000 domain). Flutter
tak perlu ubah apa-apa — `extractErrorMessage` (auth_service.dart) dah
hantar terus mesej ralat backend ke `MySnackBar.error`.

**Opus verify 2026-08-15**: 2 bug SEBENAR dijumpai dan DIBAIKI.
- **Medium** — `Create` (`blocked_email_domains.go`) salah anggap
  tingkah laku `on conflict do nothing` + `:one`: pgx pulang
  `pgx.ErrNoRows` (BUKAN struct sifar nilai + nil error) bila
  `RETURNING` kosong, jadi tambah domain yang dah wujud sentiasa 500,
  bukan 201 idempoten macam disangka. Dibaiki: `errors.Is(err,
  pgx.ErrNoRows)` (padanan `ApproveProfile`, profile.go).
- **Sama penting** — laluan jadual DB (`IsEmailDomainBlocked`, auth.go)
  TAK PERNAH runding dgn allowlist tester: management tambah
  "yopmail.com" ke `blocked_email_domains` akan senyap kunci keluar
  `google@yopmail.com`/`apple@yopmail.com` walau `allowedEmails` kata
  sepatutnya dibenarkan — TEPAT senario "kunci diri sendiri keluar drpd
  akaun review app store" yang keputusan produk asal cuba elak. Dibaiki:
  export `disposableemail.IsAllowed`, auth.go semak semula sebelum
  panggil DB. Ujian baharu (`TestIsAllowed`) tutup gap ni.

- [x] **Skrin Flutter DIBINA 2026-08-15** — `lib/features/admin/
      blocked_email_domains_page.dart` (senarai + tambah/buang, mengikut
      corak `activity_categories_page.dart`). **Gate dinaikkan ke
      SUPERADMIN sahaja** (bukan management umum) — permintaan eksplisit
      pengguna: "superadmin saja ada access untuk root system macam ni".
      Backend (`requireManagement` → `requireSuperAdmin`,
      `blocked_email_domains.go`) dan Flutter (kad "Sistem" berasingan
      drpd "Pengurusan" dalam profile page, gate
      `isSuperAdminProvider`) dua-dua dikemas kini. `isSuperAdminProvider`
      DIALIH drpd `payment_providers.dart` ke `profile_providers.dart`
      (skop am — beberapa ciri root-system perlukan siling yg sama).

**Belum dibuat**:
- [ ] Semakan cuma pada `/auth/register` — kalau ada laluan cipta akaun
      lain kelak (cth import pukal), kena panggil semakan yg sama.

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
- [x] **Check-in `self_scan` — DIBINA 2026-08-16.** `code` kekal belum
      dibina (tiada laluan klien). Reka bentuk keselamatan: TODO ni dulu
      kata self_scan "memerlukan token berputar" (checkin_token statik
      = kelayakan pembawa boleh diedarkan). Dielakkan SEPENUHNYA dengan
      TIDAK guna checkin_token/registration_id langsung utk self_scan —
      identiti datang drpd JWT pemanggil (`middleware.UserID`), bukan
      drpd apa-apa dalam body permintaan. QR yang diimbas ahli
      (`SessionCheckinQrPage`, dipaparkan management di venue) cuma
      mengekod `marc-checkin:{activityId}:{sessionId}` — data AWAM
      "sesi apa", bukan kelayakan peribadi; tangkapan skrinnya tak
      berguna kepada sesiapa. `AttendanceHandler.Mark`
      (`activity_attendance.go`) kini bercabang: `manual`/`scan` kekal
      pengurusan sahaja (`requireManagement`), `self_scan` TIADA gate
      management (itulah maksud "self") tapi tolak eksplisit kalau
      `registration_id`/`checkin_token`/`amend` dihantar sekali (elak
      laluan ni jadi "cara kedua" tanda org lain/pindaan tanpa gate).
      Semua semakan LAIN (tetingkap masa 2 jam, pendaftaran dibatalkan,
      kunci baris aktiviti, audit) kekal SAMA drpd `manual`/`scan` —
      `markAttendanceTx` tak berubah langsung. Query baharu
      `GetRegistrationByActivityAndUser` (sedia ada, guna semula).

      **Flutter**: `SessionCheckinQrPage` (papar QR venue, management,
      `registrations_page.dart` → ikon QR pada AppBar bila sesi
      dipilih) + `SelfCheckinScannerPage` (ahli imbas, tiada parameter
      route — aktiviti/sesi drpd kandungan QR, akses via ikon QR pada
      `my_activities_page.dart`). `ScanResult`/`ScanResultKind`/
      `ScanDebouncer` dialih drpd `manage/scan_result.dart` ke
      `activities/scan_result.dart` (dikongsi dua laluan sekarang, bukan
      pengurusan sahaja). `selfCheckIn` baharu pada `ActivityRepository`
      (`activity_providers.dart`, BUKAN `manage_providers.dart` —
      accessible ahli biasa).

      **Kesan pada L12 (checkin_token dlm respons senarai peserta)**:
      TODO tu dulu ramalkan L12 naik Low→Medium "sebaik self_scan
      dibina". Ramalan tu berdasarkan self_scan akan GUNA SEMULA
      checkin_token sbg kelayakan pembawa (corak biasa dlm app lain) —
      reka bentuk sebenar di atas TAK buat macam tu, jadi L12 KEKAL Low.
      Tiada perubahan diperlukan pada L12.

      **PERHATIAN reka bentuk (Opus verify 2026-08-16, bukan bug —
      kena faham, bukan kena baiki)**: self_scan TIADA bukti kehadiran
      fizikal. QR venue tak bawa "proof-of-possession" — endpoint ambil
      activity/session drpd URL, bukan drpd apa-apa yang buktikan
      pengimbas betul-betul ADA kat venue. Mana-mana ahli berdaftar +
      diluluskan boleh panggil endpoint terus (tanpa QR pun) selagi dlm
      tetingkap masa (±2 jam) — session id boleh nampak drpd
      `GET /activities/:id`. Kawalan sebenar cuma DUA: pendaftaran +
      tetingkap masa, BUKAN kehadiran fizikal. Kalau kelak ada keperluan
      "bukti betul-betul hadir" (cth sijil bernilai tinggi), self_scan
      SAHAJA tak cukup — pertimbang GPS/geofence atau kembali ke
      pengimbas pengurusan utk kes tu.
- [ ] **Sijil pencapaian (johan/naib johan) dan sijil peranan** (jurulatih,
      pengadil). `activity_certificates` **tiada lajur jenis** langsung —
      menambahnya perlukan migration, bukan sekadar UI. Penyertaan sahaja
      buat masa ini.

### Perlukan kerja berjadual (scheduler yang belum wujud)

- [x] **Peringatan H-1 — DIBINA 2026-08-16.** Pakej baharu
      `internal/activitylifecycle` (jalan setiap 1 jam, wired
      `cmd/api/main.go`). Aktiviti `status='published'` yang bermula
      dlm ~24 jam (`starts_at <= now() + 24h`) dan belum pernah dihantar
      (`reminder_sent_at is null`) dapat push sekali sahaja kepada semua
      berdaftar. **Penyahduaan merentas replika**: lajur baharu
      `activities.reminder_sent_at` (migration
      `20260815100000_activity_lifecycle_jobs.sql`), ditanda SEBELUM
      push dihantar (bukan selepas — kalau push gagal separuh jalan,
      lebih selamat satu aktiviti terlepas sebahagian penerima drpd N
      replika banjiri semua org dgn push berganda), guard
      `MarkActivityReminderSent` (`where reminder_sent_at is null`)
      buat UPDATE idempoten — replika kedua yang cuba baris SAMA affect
      0 rows, bukan ralat. Jenis notifikasi baharu `activity_reminder`
      (widen CHECK `notifications`, sama migration). `actor_id`
      notifikasi diset kpd PENERIMA SENDIRI (bukan akaun "sistem" —
      tiada satu wujud dlm skema, dan `actor_id` NOT NULL + ON DELETE
      CASCADE pada `users`).
- [x] **Aktiviti auto-complete — DIBINA 2026-08-16.** Query baharu
      `CompleteEndedActivities` (`internal/activitylifecycle`, sama
      sapuan 1 jam dgn H-1 di atas) — `status='published' and ends_at <
      now()` → `status='completed'`. Guard `status='published'` buat
      kemas kini idempoten (padanan gaya `CancelStaleUnstartedPayments`,
      activitysweep). Tiada push/notifikasi (housekeeping dalaman,
      bukan salah satu 4 jenis push spec asal).

      **Belum disahkan/dibuat**: kedua-dua ciri di atas TIADA ujian
      automasi (padanan `activitysweep`/`paymentreconcile`, yang pun
      tiada test file — konsisten dgn corak sedia ada, bukan jurang
      baharu), dan belum diuji hujung-ke-hujung terhadap aktiviti
      sebenar yang tamat/bermula (cuma disahkan via `go build`/`go vet`
      + baca kod). Opus verify dijadualkan bersama ciri self_scan
      (sesi kerja yang sama).

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

- [ ] **Tiada ujian peringkat-router bahawa CORS terpasang pada
      `/auth/password-reset/confirm`.** `middleware/cors_test.go` menguji
      middleware itu sendiri dengan teliti, tetapi tiada apa menguji
      *wiring*. Buang `passwordResetCORS` daripada `router.go` dan setiap
      ujian Go tetap lulus — sementara seluruh aliran reset mati dengan
      ralat CORS pelayar yang tak muncul langsung dalam log Go. Sama untuk
      `verify-email/confirm`, jadi bukan regresi standard; disebut kerana
      ini satu-satunya kebergantungan silang-repo ciri ini dan satu-satunya
      yang tak dilindungi. (Semakan akhir L32, 2026-08-22.)
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
- [x] **L11** tiada CORS config — okay sekarang (client mobile sahaja).
      Perlu bila ada web client. RESOLVED 2026-08-16: web client (marc_astro)
      wujud sekarang. `internal/http/middleware/cors.go` — dipasang per-route
      (bukan global), origin eksplisit via `CORS_ALLOWED_ORIGINS`. Lihat
      `/auth/verify-email/confirm` (POST) dan `/verify/certificates/:token`
      (GET) di router.go.

### Modul Aktiviti (disemak 2026-08-14)

Keluar daripada semakan menyeluruh modul aktiviti. Severiti di bawah ialah
severiti **hari ini** — L12 khususnya naik apabila satu ciri lain mendarat,
jadi baca syarat kenaikan itu, bukan label sahaja.

- [x] **L12 — `checkin_token` dihantar dalam respons senarai peserta**
      (DIBAIKI 2026-08-22).

      **Nota eskalasi yang TIDAK berlaku:** syarat asal berbunyi "Medium
      sebaik `self_scan` dibina". `self_scan` memang sudah dibina
      (2026-08-15) — tetapi eskalasi itu **tidak** berlaku, kerana
      `self_scan` direka untuk menolak `checkin_token` sepenuhnya
      (identiti daripada JWT, dan handler menolak permintaan `self_scan`
      yang membawa medan itu). Jadi ia kekal Low sepanjang masa. Direkod
      supaya tiada siapa membaca nota lama dan menganggap ia terlepas
      sebagai Medium.

      **Pembaikan:** `ListRegistrationsByActivity` kini menyenaraikan
      lajur SATU-SATU dan bukan `r.*`, jadi `checkin_token` tak lagi
      keluar — dan lajur BAHARU pada `activity_registrations` tak lagi
      disiarkan secara automatik. `ListMyRegistrations` sengaja
      DIKEKALKAN dengan `r.*`: di situ ahli melukis QR sendiri daripada
      tokennya sendiri.

      Disahkan selamat merentas repo sebelum diubah: `marc_flutter`
      `manage_providers.dart` menyatakan sendiri bahawa medan itu
      "SENGAJA tidak dimodelkan". Jadi ini menguatkuasakan di pelayan apa
      yang sebelum ini sekadar konvensyen klien. 74 ujian handler
      (bukan skip) lulus terhadap Postgres sebenar selepas perubahan.

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

- [x] **L13 — `auditActor` mengambil sambungan kolam KEDUA sambil transaksi
      memegang yang pertama** (DIBAIKI 2026-08-22). Low–Medium,
      ketersediaan, **seluruh repo**.

      **Dibaiki secara BERBEZA daripada cadangan asal.** Cadangan asal
      ialah "nilai `auditActor` sebelum `Begin`". Yang dilakukan: hantar
      `q` (Queries terikat-transaksi) dan bukan `h.queries` (terikat
      kolam) pada **15 tapak inline** merentas 5 fail. Ia lebih baik —
      cadangan asal cuma mengelak dua sambungan SERENTAK, ini langsung
      tak meminta sambungan kedua. Bacaan berjalan atas sambungan yang
      sudah dipegang transaksi.

      Bonus: diffnya jauh lebih kecil (tiada penyusunan semula aliran
      kawalan), dan tiada tapak baharu boleh terlepas — `q` cuma wujud
      dalam skop yang MEMANG ada transaksi terbuka, jadi `auditActor(c, q)`
      tak boleh ditulis di tempat yang salah.

      Empat tapak lain betul mengekalkan `h.queries`: `Issue`/`Revoke`
      sijil dan `Mark` kehadiran menilai actor SEBELUM menyerahkannya
      kepada fungsi yang membuka tx sendiri — tiada transaksi wujud untuk
      berlumba dengannya pada titik itu.

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

- [x] **L14 — ujian kebenaran LANGKAU dalam CI** (DIBAIKI 2026-08-22).
      Medium sebagai jurang jaminan, bukan kelemahan hidup.

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

      **Dibuat 2026-08-22** (selepas L36 mengubah pengiraan: kerja yang
      baru siap takkan melindungi apa-apa secara automatik).
      `ci.yml` kini menjalankan perkhidmatan `postgres:18` + `redis:8` —
      kedua-duanya percuma pada runner GitHub-hosted, tiada rahsia
      diperlukan. Kesan diukur:

      | | Sebelum | Selepas |
      |---|---|---|
      | PASS | 89 | **202** |
      | SKIP | 122 | **9** (semuanya R2) |

      **SATU DB setiap pakej**, bukan satu dikongsi: `go test ./...`
      menjalankan pakej secara SELARI, dan sapuan latar memproses SETIAP
      baris yang layak — dua pakej berkongsi satu DB akan saling memakan
      baris benih dan menghasilkan kegagalan bergantung-masa.
      `ACTIVITY_TEST_DB`/`HANDLER_TEST_DB` sengaja berkongsi (pakej yang
      sama).

      `-race` turut dihidupkan — sistem ni menjalankan lima goroutine
      latar plus fan-out notifikasi, jadi perlumbaan data kelas pepijat
      yang nyata di sini. Disahkan bersih secara tempatan sebelum
      dimasukkan.

      **Langkah tripwire ditambah** supaya kembali senyap kepada keadaan
      lama mustahil: kalau mana-mana ujian melapor SKIP dengan sebab
      `_TEST_DB`/`REDIS_TEST_URL`, job GAGAL. Tanpanya, menamakan semula
      satu env var (atau menambah pakej bersandar-DB baharu dan terlupa
      menyambungkannya) akan menjadikan CI hijau semula sambil menguji
      kurang — persis mod kegagalan asal L14. Corak dipadankan pada
      konvensyen penamaan, jadi pakej BAHARU dilindungi automatik.

      Ujian R2 (9 baki SKIP) kekal langkau — ia perlukan kredential
      Cloudflare sebenar, iaitu keputusan berasingan. Tripwire sengaja
      mengecualikannya dengan membuang baris yang menyebut `R2_LIVE_TEST`
      — BUKAN dengan menyempitkan corak utama, sebab empat ujian R2
      menyebut `REAPER_TEST_DB` dalam mesej skipnya dan akan salah
      ditangkap.

      Disahkan DUA arah secara tempatan sebelum dihantar: dengan semua
      perkhidmatan hadir tripwire senyap (202 PASS / 9 SKIP, kesemua 9
      sebab-R2); dengan `AUTHZ_TEST_DB` dibuang, `go test` tetap keluar 0
      (tepat masalahnya) dan tripwire MENYALA.

- [x] **L15 — ahli boleh memusnahkan bukti kehadirannya sendiri, tanpa
      jejak** (DIBAIKI 2026-08-22). Low (mencederakan diri), tetapi tidak
      boleh dipulihkan.

      **Keputusan produk 2026-08-22: pembatalan DITOLAK selepas aktiviti
      tamat.** `CancelRegistration` kini join `activities` dan menuntut
      `a.ends_at > now()`; handler `Cancel` menyemak lebih awal semata
      untuk memulangkan 422 yang membezakan "aktiviti sudah tamat"
      daripada "tidak berdaftar" (dalam SQL, kedua-duanya sifar baris).

      Guard diletak dalam SQL dan bukan HANYA handler supaya laluan tulis
      masa hadapan tak boleh memintasnya — ada ujian khusus yang
      memanggil query TERUS untuk menegakkan itu.

      **Baki yang diterima:** ahli masih boleh batal SEMASA aktiviti
      berjalan, selepas hadir sesi awal. Tetingkapnya jauh lebih sempit
      dan pilihannya lebih jelas kepada ahli (aktiviti belum tamat), jadi
      ia tak dianggap sebagai jurang yang sama. Guard "ada kehadiran
      direkod" akan menutupnya sepenuhnya kalau ia jadi masalah sebenar.

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
- [x] **L24 — medan lain masih tiada had panjang** (di luar skop L17
      asal): `activityRequest.Description`/`LocationAddress`
      (POST DAN PATCH), `Cancel.Reason` (**turut disuntik terus ke body
      push notification yang di-fanout ke setiap pendaftar**),
      `revokeCertificateRequest.Reason`, `markAttendanceRequest.Reason`.

      **Disahkan SIAP 2026-08-22** (dibaiki sepanjang kerja sejak audit,
      cuma tak pernah ditanda): `Description` `max=2000`,
      `LocationAddress` `max=500`, `Cancel.Reason` `required,max=500`,
      kedua-dua `revokeCertificateRequest.Reason` dan
      `markAttendanceRequest.Reason` `omitempty,max=500`. Laluan PATCH
      pula ada semakan `utf8.RuneCountInString` berasingan untuk
      keempat-empat medan aktiviti — kira RUNE, bukan bait, jadi ia tak
      mengulang regresi L23.
- [x] **L25 — spam notification/push tanpa had melalui comment create**
      (corak sama L18, terbuka pada laluan berimpak lebih tinggi).
      `comments.go` panggil `notifyOwner` pada SETIAP
      `POST /posts/:id/comments`, route ni **tiada rate limiter**.
      Gelung = harassment bersasar + pertumbuhan `notifications`/
      `comments` tanpa had. `POST /posts` dan `PATCH /me` turut tak
      dihad kadar.

      **Disahkan SIAP 2026-08-22**: ketiga-tiga route kini ada baldi
      BERNAMA sendiri dalam `router.go` — `comment-create` (3s/10),
      `post-create` (3s/10), `profile-update` (3s/10). Had kadar (bukan
      dedup macam L18) memang ubat yang betul di sini: setiap create
      MEMANG baris baharu yang sah, jadi tiada konsep "dah wujud" untuk
      diguna sebagai guard.
- [x] **L26 — bucket rate-limit auth kini dikongsi 7 route, IP-only**
      (sisi Go SIAP; baki disemak sebagai L26a di bawah).
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

      **Sisi Go SIAP (disahkan 2026-08-22)**: `authSessionRateLimiter`
      (baldi `auth-session`, 3s/burst 10) kini dipasang pada `/refresh`
      dan `/logout` sahaja, berasingan daripada baldi `auth` ketat
      (12s/5) yang kekal untuk register/login/verify-email. Senario
      wifi-kelab/CGNAT tak lagi menghabiskan kuota log masuk.

- [x] **L26a — sahkan sisi Flutter kendalikan 429 pada `/auth/refresh`**
      (DISAHKAN SELAMAT 2026-08-22). Baki L26 yang tak dapat disemak dari
      repo ni. Kalau `marc_flutter` melayan 429 sama macam 401 (token tak
      sah), ia jadi forced-logout beramai-ramai.

      **Tidak berlaku.** `marc_flutter` `lib/core/api_client.dart`:
      `e.response?.statusCode == 401 ? rejected : networkFailure` — jadi
      429 jatuh ke `networkFailure`, yang MENGEKALKAN refresh token dan
      cuma menggagalkan permintaan semasa. Hanya 401 yang membuang sesi.
      Digandingkan dengan baldi `auth-session` berasingan (L26), senario
      wifi-kelab/CGNAT kini selamat pada KEDUA-DUA belah.
- [x] **L27 — dua residual tarikh LOW pada resit donation.**
      (a) `pi.Created` (`stripe.go`) ialah masa PaymentIntent **dicipta**,
      bukan masa bayar sebenar — untuk DuitNow QR/FPX pembayar mungkin
      bayar minit kemudian; `LatestCharge.Created` lebih tepat.
      (b) `donations.go` format `paidAt` dalam server-local (UTC)
      manakala PDF lampiran cetak MYT — badan emel dan lampirannya
      sendiri lari 8 jam.

      **(b) SIAP** (disahkan 2026-08-22): `donationReceiptHTML` kini guna
      `receipt.FormatDateTime`, fungsi SAMA yang PDF guna
      (`t.In(malaysiaTZ)` + akhiran "(MYT)"). Badan emel dan lampiran tak
      boleh lari lagi — satu fungsi, satu zon waktu.

      **(a) RISIKO DITERIMA**, bukan kerja tertunggak. Komen panjang dalam
      `stripe.go` menerangkan sebabnya: Stripe TIDAK meng-expand
      `latest_charge` dalam payload webhook secara lalai — ia tiba sebagai
      rentetan id, dan `Charge.UnmarshalJSON` stripe-go cuma menetapkan
      `ID` sambil membiarkan `Created` pada nilai sifar. Jadi membaca
      `pi.LatestCharge.Created` akan senyap mencetak **1970-01-01** —
      lebih teruk daripada pepijat semasa (tarikh salah tapi munasabah).
      Pembaikan sebenar perlukan tetapan expansion pada endpoint webhook
      Stripe atau panggilan `charge.Get` tambahan.

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

### Audit `queries/` + `internal/` + `cmd/` (2026-08-22)

> Laporan penuh (bukti, laluan kegagalan, kod): [`docs/audits/2026-08-22-queries-internal-cmd.md`](./docs/audits/2026-08-22-queries-internal-cmd.md).
> Bahagian ni senarai kerjanya sahaja.

Pusingan ketiga. Skop: ketiga-tiga direktori penuh, termasuk yang dibina
SELEPAS audit 2026-08-15 (yuran aktiviti, `paymentreconcile`,
`activitysweep`, `activitylifecycle`, `paymentlog`, resit, permintaan
pemadaman akaun). `go build`/`go vet`/`gofmt`/`go test ./...` bersih
semasa audit — jadi semua penemuan di bawah ialah gap yang lulus compiler
DAN lulus ujian sedia ada.

Tiada penemuan capai bar CRITICAL (eksploit sekarang oleh aktor
keistimewaan rendah). L28 dan L29 dua-dua kehilangan data/duit senyap
tanpa jejak, jadi dinilai HIGH.

- [x] **L28 — reaper boleh padam gambar post yang MASIH hidup (HIGH,
      DIBAIKI 2026-08-22).**
      `queries/uploads.sql` `ListStalePendingUploads` komennya kata
      "pending upload yang tak pernah dilekatkan pada mana-mana post",
      tapi query sebenar `select * from pending_uploads where created_at
      < $1` — **tiada semakan lekatan langsung**. Ia bergantung 100% pada
      baris `pending_uploads` dipadam semasa post dicipta, dan
      `posts.go` (gelung `CreatePostImage`) buang ralat itu dengan
      `_ = q.DeletePendingUpload(...)` di bawah komen "row lingering
      harmless". Ia BUKAN harmless: kalau DELETE tu gagal (deadlock, blip
      sambungan), `reaper.sweepAbandonedUploads` gilirkan kunci tu selepas
      6 jam (`abandonedAfter`) dan `drainDeleteQueue` padam objek dari R2 —
      gambar post yang sedang dipaparkan hilang kekal, baris `post_images`
      tinggal, feed papar imej rosak. Laluan avatar (`profile.go`
      `applyAvatar`) buat BETUL — ia semak ralat `DeletePendingUpload`
      dalam transaksi.

      **Dibaiki 2026-08-22, kedua-dua lapisan** (bukan salah satu — kos
      melanggar invarian ni ialah kehilangan data yang tak boleh
      dipulihkan): (a) `ListStalePendingUploads` kini ada DUA klausa
      `not exists` (`post_images`, `profiles.avatar_r2_key`) jadi ia
      betul secara bersendirian dan tak bergantung pada fail lain; (b)
      `posts.go` menyemak ralat `DeletePendingUpload` dan menggagalkan
      transaksi — rollback mengekalkan baris pending, tepat seperti niat
      asal komen lama. Migration `20260822100000_index_post_images_r2_key`
      menambah indeks pada `post_images(r2_key)` — subquery baharu (DAN
      `ListOrphanedPostImageKeys` sedia ada) sebelum ni seq scan jadual
      yang tumbuh selama-lamanya.

      Ujian: `internal/reaper/pending_uploads_live_test.go`, tiga kes —
      gambar post hidup tak disapu, avatar hidup tak disapu, dan yatim
      SEBENAR masih disapu (guard terhadap pembaikan yang terlebih
      ketat). Perlukan **Postgres sahaja**, bukan R2 — yang diuji ialah
      predikat SQL. Dua kes pertama disahkan GAGAL terhadap query lama
      dan LULUS terhadap yang baharu.
- [x] **L29 — bil ToyyibPay dicipta SEBELUM baris DB; bayaran boleh hilang
      tanpa jejak (HIGH, DIBAIKI PENUH 2026-08-22).** `registration_payment.go` `Checkout` panggil
      `h.gw.CreatePayment(...)` (bil SAH wujud di ToyyibPay, ahli boleh
      bayar) dan baru lepas tu `CreateRegistrationPayment(...)`. Kalau
      INSERT gagal → 500, tapi bil tetap hidup. Lepas ahli bayar:
      `UpdateRegistrationPaymentStatusByGatewayRef` kena 0 baris →
      `pgx.ErrNoRows` dilayan sebagai "replay biasa" dan **disenyapkan**;
      `paymentreconcile` pula iterasi baris `registration_payments`, jadi
      ia tak boleh tolong untuk baris yang tak pernah wujud. Duit masuk,
      sifar rekod. Satu-satunya jejak ialah `payment_logs`
      `EventCheckoutFailed` — yang **tak bawa `GatewayRef`** sebab
      `result` dibuang pada laluan ralat, jadi bil yatim tu tak boleh
      dicari pun. Corak sama pada `activity_registration_payment.go`
      (`SetRegistrationPaymentRef` gagal selepas bil dicipta).

      Fix: cipta baris `pending` dengan `gateway_ref` kosong DAHULU,
      panggil `CreatePayment`, lepas tu UPDATE isi ref.

      ⚠️ Susunan semula itu **perlukan migration** — `gateway_ref` ialah
      `not null` dengan indeks unik `(gateway, gateway_ref)`, jadi dua
      baris ref-kosong akan berlanggar pada ahli KEDUA yang checkout.
      Perlu jadikan lajur nullable + tukar indeks kepada partial
      (`where gateway_ref is not null`). Itu yang menjadikannya kerja
      laluan-duit + skema, bukan susunan semula ringkas.

      **PEMBAIKAN PENUH dipasang 2026-08-22.** Migration
      `20260822140000_registration_payments_nullable_ref`:
      `gateway_ref` jadi nullable, indeks unik jadi SEPARA
      (`where gateway_ref is not null`). Susunan `Checkout` dibalikkan:

      1. `CreateRegistrationPayment` — baris 'pending', TIADA ref
      2. `CreatePayment` (createBill)
      3. `SetRegistrationPaymentGatewayRef` — pautkan

      Kegagalan pada langkah 1 tak mencipta apa-apa. Kegagalan pada
      langkah 2 meninggalkan baris tanpa ref, yang ditanda `'failed'`
      (`MarkRegistrationPaymentFailed`, guard `gateway_ref is null`) —
      **tiada bil wujud, jadi tiada duit boleh berpindah tangan**. Urutan
      berbahaya lama (bil wujud, baris tiada) kini MUSTAHIL.

      `SetRegistrationPaymentGatewayRef` sekali-tulis (guard
      `gateway_ref is null`) supaya rekod kewangan yang sudah berpaut tak
      boleh dialihkan kepada bil orang lain.

      **Tetingkap baki, dinyatakan jujur:** langkah 3 boleh gagal selepas
      bil dicipta, meninggalkan bil + baris yang tak berpaut. Ia jauh
      lebih sempit drpd sebelum ni DAN boleh dipulihkan — baris itu kini
      WUJUD dengan user_id, amaun, dan timestamp, jadi mendamaikannya
      ialah satu UPDATE, bukan siasatan forensik. Log ERROR +
      `paymentlog` membawa kedua-dua belah pautan.

      `ListPendingRegistrationPaymentsOlderThan` menapis
      `gateway_ref is not null` — baris tanpa bil tiada apa untuk ditanya
      pada gateway (padanan skop query aktiviti).

      Ujian: `handlers/registration_payment_live_test.go` (5 kes, gateway
      distub) + `TestReconcileLangkauBarisTanpaGatewayRef`. Dua mutasi
      disahkan menggagalkan ujian yang sepatutnya: buang tapisan
      `is not null`, dan buang tanda `'failed'`. Pusingan migration
      `up → down → up` diuji dgn baris ref-NULL sebenar — `Down`
      memusnahkan data (didokumen dalam failnya) tapi memulihkan
      `NOT NULL` + indeks penuh dengan betul.
- [x] **L30 — backlog `paymentreconcile` membesar selama-lamanya (MEDIUM,
      kos + kadar API gateway; DIBAIKI 2026-08-22).** Ketiga-tiga query sumber ada had umur
      **bawah** sahaja — tiada had atas, tiada `LIMIT`:
      `ListPendingRegistrationPaymentsOlderThan`,
      `ListPendingDonationsOlderThan`,
      `ListPendingActivityRegistrationsOlderThan`. Checkout yang
      ditinggalkan TAK PERNAH keluar dari `pending`: ToyyibPay pulang
      `No data found!` → `CheckStatus` = `"pending"` selamanya; Stripe
      intent terbiar kekal `requires_payment_method` → `"pending"`
      selamanya; dan `CancelStaleUnpaidBills` set `status='cancelled'`
      tapi **biarkan `payment_status='pending'`** sedangkan query aktiviti
      tak tapis `status <> 'cancelled'`, jadi baris mati kekal disemak.
      Kesan: setiap 30 minit, satu panggilan HTTP keluar bagi SETIAP baris
      terkumpul sejak hari pertama. Selepas setahun operasi = ratusan/ribuan
      panggilan setiap pusingan, selama-lamanya. Fix: tingkap atas
      (`created_at > now() - interval '7 days'`) + `limit`, dan tapis
      `status <> 'cancelled'` pada query aktiviti.

      **Dibuat 2026-08-22**, ketiga-tiganya: pemalar `maxAge` (7 hari,
      dipilih sebab cutoff paling panjang di tempat lain dalam sistem ni
      ialah 24 jam — `activitysweep.unpaidBillAfter` — jadi tiada bayaran
      yang masih boleh diselesaikan tercicir) dan `batchSize` (200),
      dihantar melalui struct `window`. Struct, bukan dua
      `pgtype.Timestamptz` bersebelahan: kedua-duanya jenis SAMA, jadi
      tertukar susunan akan menghasilkan julat kosong secara SENYAP
      (sifar baris = "tiada kerja"), bukan ralat.

      Guard `status <> 'cancelled'` pada query aktiviti TIDAK
      menyembunyikan race bayar-selepas-batal yang
      `UpdateRegistrationPaymentStatusByPaymentRef` sengaja biarkan
      kelihatan — race itu ditangkap oleh WEBHOOK, yang tak melalui query
      ni, dan tetingkap masanya jauh lebih pendek drpd cutoff 24 jam.

      Baris yang melebihi tingkap TIDAK hilang: ia kekal dalam DB dan
      tetap kelihatan melalui `/admin/payments`, cuma berhenti dipoll.
      `order by created_at` menaik bermakna yang paling lama menunggu
      didahulukan, jadi tiada baris boleh kebuluran di bawah `batchSize`.
- [x] **L31 — `WriteTimeout` server (15s) lebih pendek drpd operasi yang ia
      hoskan (30s) (MEDIUM, DITAMPUNG 2026-08-22 — `WriteTimeout` kini
      90s; pembaikan sebenar, iaitu fasa 2 sijil jadi kerja latar + 202,
      kekal TERBUKA).** `cmd/api/main.go` set `WriteTimeout: 15 *
      time.Second`, tapi `certificateUploadTimeout` dan
      `receiptUploadTimeout` dua-dua 30s, dan `VerifyWebhook` ToyyibPay
      buat poll keluar 15s (`httpClient.Timeout`) SEBELUM kerja DB.
      Kesan konkrit: `POST /activities/:id/certificates` untuk aktiviti
      50–200 orang **dijamin** putus sambungan pada 15s — handler terus
      jalan (Go tak batalkan `Request.Context()` atas write deadline,
      cuma set deadline pada `ResponseWriter`) jadi datanya betul dan
      boleh disambung, tapi pengurus nampak "gagal" setiap kali dan
      takkan tahu ia sebenarnya berjaya sebahagian. Webhook ToyyibPay
      duduk tepat di tepi: 15s poll + DB > 15s → ToyyibPay dapat
      sambungan putus dan retry. Ini kes konkrit bagi amaran generik yang
      dah direkod dalam "Nota reka bentuk keselamatan — yuran pendaftaran
      ToyyibPay" di atas ("pastikan endpoint webhook tetap pulang cepat").
      Fix: naikkan `WriteTimeout` (60–90s), atau lebih baik jadikan fasa 2
      sijil kerja latar + pulang 202 serta-merta.

      **Siapa mengikat siapa** (disemak silang dgn `marc_flutter`
      2026-08-22): klien Flutter SUDAH ada override
      `certificatesIssueTimeout` = **5 minit** khusus untuk laluan terbit
      sijil (lalai dio global 12s). Jadi PELAYAN yang mengikat, bukan
      klien — 15s membenarkan ~20 sijil, 90s membenarkan ~120 (fasa 2
      berjujukan, ~0.7s sesijil). Melebihi itu, pengurus masih bergantung
      pada laluan 202/timeout + "Tekan Terbitkan sekali lagi" yang SUDAH
      dibina di kedua-dua belah dan kekal betul.

      Untuk laluan LAIN (resit dsb), lalai dio 12s yang mengikat dan
      bukan `WriteTimeout` — jadi menaikkannya tak mengubah apa-apa di
      sana. Ini sebab kenapa 90s ialah tampung: ia menaikkan siling,
      bukan menghapuskannya. Hanya fasa 2 sebagai kerja latar yang buat
      begitu.
- [x] **L32 — tiada laluan tukar/reset kata laluan langsung (MEDIUM,
      fungsi).** Grep seluruh `internal/`, `cmd/`, `queries/`: tiada
      `password_reset`, tiada `PATCH /me/password`. Ahli yang lupa kata
      laluan **tiada jalan pulih dalam app** — mesti hubungi staff untuk
      UPDATE DB terus. Untuk app yang gate keahlian dgn kelulusan +
      yuran sebenar, ni gap fungsi yang ketara, bukan nice-to-have.

      **RESET dibina 2026-08-22** (spec:
      `docs/superpowers/specs/2026-08-22-reset-kata-laluan-design.md`,
      pelan: `docs/superpowers/plans/2026-08-22-reset-kata-laluan.md`).
      Jadual `password_reset_tokens`, dua endpoint awam, halaman
      `marc_astro/src/pages/reset-kata-laluan.astro`, skrin
      `marc_flutter` `forgot_password_page.dart`.

      **TUKAR kata laluan semasa log masuk KEKAL TERBUKA** — ditolak
      secara eksplisit semasa brainstorm untuk memendekkan skop. Bukan
      penyekat: ahli yang syak akaun dikompromi ada
      `POST /auth/logout-all`. Buka item baharu kalau ia diperlukan.
      Berkait: `registerRequest.Password` `min=6` longgar, dan tiada
      lockout per-AKAUN (had kadar `auth` per-IP sahaja, lihat L26) —
      jadi brute-force teragih merentas IP tak dihalang apa-apa.
- [x] **L33 — `GET /me/payments` tak pulangkan derma; endpoint resit derma
      tak boleh dicapai (LOW, DIBAIKI 2026-08-22).** `payments.go` `Mine` pulang
      `registration_fee` + `activity_fees` sahaja, dan `queries/donations.sql`
      tiada `ListMyDonations` langsung. Tapi route
      `GET /me/payments/donation/:id/receipt` wujud dan perlukan
      `donations.id` — client tiada cara sah dapat id tu. Endpoint tu mati
      secara praktikal. Fix: tambah `ListMyDonations` (skop `user_id`) +
      seksyen ketiga dalam `Mine`.

      **Dibuat 2026-08-22, kedua-dua repo.** Go: `ListMyDonations` (skop
      `user_id`, isih `created_at desc`) + kunci `donations` dalam
      respons `Mine`. Flutter: `DonationPaymentEntry` +
      seksyen "Sokongan" dalam `payment_history_page.dart`.

      Nota yang mengesahkan diagnosis: `PaymentReceiptRepository.donation()`
      SUDAH wujud di Flutter dan berfungsi — plumbing klien lengkap sejak
      awal, cuma senarai yang mendedahkan `donations.id` tiada. Tiada
      sebaris pun kod resit perlu ditulis di mana-mana belah.

      Derma TANPA NAMA (`user_id` null) sengaja dikecualikan — penderma
      tiada akaun untuk menuntutnya. Status `pending`/`failed` TURUT
      dipulangkan (padanan `ListMyRegistrationPayments`): sejarah patut
      menunjukkan percubaan, bukan senyap menghilangkannya; butang resit
      digate pada `status == 'succeeded'` di UI.

      Ujian Go: 5 kes termasuk pengasingan (derma ahli LAIN dan derma
      tanpa nama tak boleh bocor — kalau bocor, pemanggil boleh muat
      turun resit yang membawa nama + emel penderma). Ujian Flutter: 5
      kes nyahsiri, termasuk **kunci `donations` TIADA → senarai kosong**
      supaya app baharu tak terhempas terhadap backend lama semasa
      tetingkap deploy berperingkat.
- [x] **L34 — respons `PATCH /comments/:id` hilang `author` (LOW,
      DIBAIKI 2026-08-22).** `comments.go` `Update` bina `commentResponse`
      tanpa medan `Author`, berbeza drpd `Create` dan `List` yang dua-dua
      isi. Client yang tulis-ganti komen dalam senarai daripada respons
      PATCH akan nampak nama/avatar penulis hilang sampai refresh.

      Semasa membaiki, `LikeCount`/`LikedByMe` didapati ada kecacatan
      yang SAMA pada laluan yang sama — dua-dua tinggal pada nilai sifar,
      jadi klien yang menulis ganti komen turut nampak kiraan like jatuh
      ke 0. Dibaiki sekali; membaiki `author` sahaja akan meninggalkan
      pepijat serupa satu medan di sebelahnya. Logik author diekstrak ke
      `authorOf` (dikongsi dgn `Create`, jadi dua laluan tak boleh
      terpesong lagi) dan kiraan like ke `likeStateOf`, yang guna semula
      query berkumpulan SAMA dengan `List` (kepingan satu elemen).
- [x] **L35 — like comment tak hantar notifikasi (LOW, DIBAIKI
      2026-08-22).** `posts.go` `Like` panggil `notifyOwner`;
      `comments.go` `CommentHandler.Like` tak panggil apa-apa dan tiada
      komen yang kata ia sengaja. NOTA: L18 di atas merekod
      "`CommentHandler.Like` betul (tak notify)" — tapi itu dalam konteks
      *spam* like berulang, bukan keputusan produk "komen tak layak
      notifikasi". Sahkan mana satu yang dimaksudkan sebelum tambah;
      kalau ditambah, ia perlu guard `:execrows` sama macam `LikePost`
      lepas fix L18.

      **Keputusan produk 2026-08-22: YA, beritahu.** Dilaksana bersama
      guardnya SERENTAK, bukan selepasnya: `LikeComment` jadi
      `:execrows`, dan handler cuma memberitahu bila `rows > 0`. Route
      like TIADA rate limiter — dedup inilah mekanismenya, jadi guard tu
      satu-satunya yang menahan gelung harassment bersasar. Ujian
      `TestLikeCommentBerulangTidakSpamNotifikasi` menegakkannya
      (disahkan gagal dgn `5 notifikasi selepas 5 like` bila guard
      dilumpuhkan).

      Migration `20260822160000_notifications_comment_like` meluaskan
      `notifications_type_check` dengan `'comment_like'`. Senarai tertutup
      DIKEKALKAN (bukan dibuang): ia yang menahan jenis tersalah eja
      daripada menjadi notifikasi yang klien tak tahu cara papar.

      ⚠️ **Flutter belum kendalikan jenis `comment_like`** — backend akan
      mula menghantarnya sebaik ini di-deploy. Lihat
      `../marc_flutter/TODO.md`.
- [ ] **L37 — `POST /auth/register` membocorkan enumerasi akaun (LOW).**
      Ia pulang `409 "email ini sudah berdaftar"` untuk emel yang wujud,
      jadi sesiapa boleh menyenaraikan emel mana yang berdaftar dengan
      memanggil register berulang kali. Dihadkan kadar (baldi `auth`,
      12s/5 per IP) jadi ia perlahan — tapi tak dihalang.

      Ditemui semasa brainstorm **L32** (reset kata laluan), yang memilih
      **tidak** membocorkan enumerasi pada endpoint barunya. Direkod
      BERASINGAN dan bukan diselinapkan ke dalam kerja itu: membaikinya
      menyentuh aliran pendaftaran yang tiada kaitan, dan ia mengubah
      mesej ralat yang Flutter sudah papar.

      Nota jujur: selagi ni terbuka, keputusan bukan-enumerasi L32 TIDAK
      menutup enumerasi merentas sistem — penyerang akan guna `register`.
      Itu bukan hujah untuk membocorkannya pada laluan reset juga;
      menambah kebocoran kedua menjadikan pembaikan nanti lebih mahal.

      Pembaikan BUKAN remeh: pendaftaran mesti tetap memberitahu pengguna
      SEBENAR bahawa emel mereka sudah diambil, kalau tidak UX daftar
      rosak. Corak biasa ialah pulang 201 lalu hantar emel "seseorang cuba
      daftar guna alamat anda" — keputusan produk, bukan sekadar teknikal.

- [x] **L36 — modul duit paling berisiko tiada ujian langsung (MEDIUM,
      liputan; DIBAIKI 2026-08-22).** `go test ./...` lapor `no test files` untuk
      `internal/paymentreconcile`, `internal/activitysweep`,
      `internal/activitylifecycle`, `internal/authz`, `internal/auth`,
      `internal/config`. `paymentreconcile.RunOnce` sengaja diekspos
      "supaya boleh dipanggil terus dalam ujian" — ujian tu tak pernah
      ditulis. Ia logik yang **menulis ganti status bayaran secara
      automatik** berdasarkan jawapan gateway; itu tempat terakhir yang
      patut tiada ujian. `internal/authz` pula ialah keseluruhan lapisan
      kebenaran (gantian RLS) — bertindih dgn L14/nota CI di bahagian
      Ujian bawah.

      **Keenam-enam ditutup 2026-08-22.** Dua yang TULEN (`auth`,
      `config`) ialah ujian unit biasa — ia benar-benar berjalan dalam
      CI, tak macam empat yang lain. Empat yang perlukan Postgres ikut
      corak live sedia ada (env var, `t.Skip` tanpa DB).

      `paymentreconcile` diuji dengan `payment.Gateway` PALSU — gateway
      ialah antara muka, jadi jawapannya boleh ditetapkan per-rujukan dan
      keputusan reconcile jadi deterministik. Hanya DB perlu nyata,
      kerana kekangan yang membentuk logiknya (`CHECK` pada
      `payment_status` yang TIADA 'failed') hidup dalam skema.

      **Ujian mutasi dijalankan pada tiga guard paling kritikal** — dan
      DUA daripadanya mendedahkan penegasan yang vacuous, yang lalu
      dibetulkan:

      - *Tingkap atas L30*: versi pertama menyemai baris pada
        `maxAge + 24h`. Umur benih diterbitkan drpd pemalar yang diuji,
        jadi menaikkan `maxAge` turut menaikkan umur benih — penegasan
        yang TAK BOLEH gagal. Dibetulkan kepada umur MUTLAK (30 hari) +
        semakan awal yang gagal kalau `maxAge` menghampirinya.
      - *Dedup peringatan*: versi pertama menjalankan `RunOnce` dua kali
        dan menegaskan tiada push berganda. Itu menguji tapisan
        `ListActivitiesNeedingReminder`, BUKAN guard race sebenar —
        membuang `if affected == 0` dalam Go tak menggagalkannya, kerana
        lapisan senarai menutupnya dahulu. Ditambah ujian yang memanggil
        `MarkActivityReminderSent` DUA KALI terus (mensimulasikan dua
        replika yang menyenaraikan baris sama sebelum salah satu
        menandanya) dan menegaskan panggilan kedua mengena SIFAR baris.

      Selepas dibetulkan, ketiga-tiga mutasi (longgarkan `maxAge`,
      samakan dua cutoff `activitysweep`, buang guard SQL
      `reminder_sent_at is null`) menggagalkan ujian yang sepatutnya.

**Minor (direkod, bukan kerja tertunggak):**

- `posts.go` `List` **senyapkan** `limit` tak sah ke default;
  `activities.go` `List` pulang 400 untuk kes sama, dan komennya sendiri
  terangkan kenapa senyap itu salah ("limit=500 diamkan jadi 20 ialah
  jenis perbezaan yang klien tak dapat lihat"). Selaraskan.
- `donations.go` `selectGateway(amountCents)` abaikan parameternya
  sepenuhnya (placeholder untuk threshold RM500 SociaBuzz — lihat
  bahagian Payment).
- `paymentreconcile.go` kira `summary.MismatchesFixed++` juga untuk kes
  `cancelled+paid` yang sebenarnya **perlukan campur tangan manual**,
  bukan "dibetulkan" — laporan pencetus manual jadi terlebih optimistik.
- `profile.go` `UpdateMe` tulis nama/telefon dalam satu operasi lalu
  avatar dalam transaksi BERASINGAN; kalau avatar ditolak, ahli dapat 400
  tapi nama sudah tersimpan.
- Purata 6–10 query DB setiap permintaan tulis (2 middleware gate +
  `requireManagement` + `auditActor` + muat semula respons). Belum
  masalah pada skala kelab; `auditActor` boleh guna semula profil yang
  `requireManagement` baru sahaja baca.

**Disahkan bersih pusingan ni** (tiada penemuan baharu): rotasi refresh
token (atom, guard `consumed_at is null`, grace window reuse), corak kunci
pesanan `LockActivityForRegistration` merentas daftar/PATCH/check-in/
terbit-sijil, `audit.Record` dalam transaksi mutasi di SETIAP tapak,
keyset pagination semua senarai, penapisan keterlihatan `ListVisibleProfiles`
dalam SQL, `verifyResponse` sebagai sempadan eksplisit halaman pengesahan
awam, `middleware.BlockTesterWrites` (gagal-tertutup), pengendalian
`extractBillCode` ToyyibPay, dan pengasingan baldi had kadar bernama.

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

# Lapisan kebenaran (hierarki rank dibaca drpd seed migration sebenar)
AUTHZ_TEST_DB="postgres://localhost:5432/marc_check?sslmode=disable" \
  go test ./internal/authz/ -v

# Sapuan pendaftaran berbayar terbiar (DUA cutoff yang sengaja berbeza)
ACTIVITYSWEEP_TEST_DB="postgres://localhost:5432/marc_check?sslmode=disable" \
  go test ./internal/activitysweep/ -v

# Peringatan H-1 + auto-complete (termasuk guard dedup cross-replika)
LIFECYCLE_TEST_DB="postgres://localhost:5432/marc_check?sslmode=disable" \
  go test ./internal/activitylifecycle/ -v

# Reconcile bayaran — gateway DIPALSUKAN, cuma DB yang nyata
RECONCILE_TEST_DB="postgres://localhost:5432/marc_check?sslmode=disable" \
  go test ./internal/paymentreconcile/ -v
```

Keempat-empat suite baharu di atas boleh berkongsi SATU DB buangan —
setiap ujian menyemai barisnya sendiri dengan id rawak dan tidak
bergantung pada jadual yang kosong.

`internal/auth` dan `internal/config` TULEN (tiada DB) — ia berjalan
dalam `go test ./...` biasa, jadi ia antara sedikit ujian repo ni yang
benar-benar melindungi setiap PR.

Guna DB **buangan** untuk yang perlukan Postgres — bukan DB dev kau.

- [ ] Handler test yang tinggal masih manual — belum ada suite integrasi
      penuh untuk posts/comments.
- [x] **Enam pakej langsung `no test files`** — `paymentreconcile`,
      `activitysweep`, `activitylifecycle`, `authz`, `auth`, `config`
      (lihat **L36**). **Keenam-enamnya ditutup 2026-08-22.** Tiada lagi
      pakej dalam `internal/` yang `no test files`.
- [x] **Semua `*_live_test.go` LANGKAU dalam CI** (DIBAIKI 2026-08-22,
      lihat **L14**). `.github/workflows/ci.yml`
      jalankan `go test ./...` tanpa perkhidmatan Postgres dan tanpa env
      ujian, jadi setiap ujian live dalam repo — bukan modul aktiviti
      sahaja — lapor SKIP pada setiap pull request. Maknanya: **binaan
      hijau tidak bermakna ujian live lulus.**

      Kini `ci.yml` menjalankan `postgres:18` + `redis:8` dengan tujuh DB
      berasingan, `-race`, dan langkah tripwire yang GAGALKAN job kalau
      mana-mana ujian melapor SKIP atas sebab env var DB hilang.
      Selebihnya di bawah kekal betul sebagai SEJARAH — ia menerangkan
      kenapa penegasan PII dipecahkan menjadi ujian unit tanpa DB, yang
      masih merupakan reka bentuk yang lebih baik walaupun CI kini ada DB. Ini ditemui semasa kerja
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
