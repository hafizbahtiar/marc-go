# MARC Backend — TODO

Kerja yang **belum siap** sahaja. Sejarah penuh (keputusan, gotcha, hasil
verifikasi setiap stage) ada dalam git log — cari ikut nombor stage.
Struktur kod: [`ARCHITECTURE.md`](./ARCHITECTURE.md).
Schema & migration: [`DATABASE.md`](./DATABASE.md).

Stage 0–15 siap: auth custom, RBAC, posts/comments/likes, kelulusan ahli,
push, upload R2, donation Stripe, jejak audit, pembersih storan, polisi
simpanan.

---

## Perlu tindakan kau (bukan kod)

- [ ] **Deploy environment `production` Railway** — `staging` sahaja live.
- [ ] **Migrate data lama dari Supabase** (2 profiles, 4 roles).
- [ ] **Ganti r2.dev sebelum produksi.** `R2_PUBLIC_URL` sekarang guna
      Public Development URL. Dua masalah: Cloudflare kadar-hadkan dan kata
      ia bukan untuk produksi, DAN ia menjadikan seluruh bucket boleh dibaca
      sesiapa yang ada URL. Kunci UUID tak diteka — itu kekaburan, bukan
      kawalan akses. Untuk app ahli-sahaja yang simpan gambar ahli,
      gantinya ialah **presigned GET** (perubahan kod, belum dibuat).
- [ ] **Rotate kunci test Stripe** yang sempat masuk git (commit `c170391`,
      dah di-amend sebelum push — tapi rotate tetap lebih selamat).

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

## Jejak audit — jurang yang tinggal

- [ ] Audit untuk `create` post/comment. Volum tinggi, faedah rendah (entiti
      sendiri dah simpan author + created_at) — keputusan produk, belum buat.
- [ ] Comment tiada gambar buat masa ni. Kalau ditambah, ia mesti masuk
      gilir `deleted_uploads` yang sama macam post.

## Security — Low yang sengaja dibiar

- [ ] **L10** trusted-proxy (`100.64.0.0/10` + RFC1918) betul untuk Railway,
      tapi kalau pindah platform lain kena semak semula.
- [ ] **L11** tiada CORS config — okay sekarang (client mobile sahaja).
      Perlu bila ada web client.

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
```

Guna DB **buangan** untuk yang perlukan Postgres — bukan DB dev kau.

- [ ] Handler test yang tinggal masih manual — belum ada suite integrasi
      penuh untuk posts/comments.
