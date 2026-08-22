# Integrasi Telegram — Fasa 1: binding akaun (2026-08-22)

Fasa pertama daripada tiga fasa integrasi Telegram yang dirancang:
**binding** (spec ini) → notifikasi → 2FA. Setiap fasa spec + plan
berasingan; notifikasi dan 2FA bergantung pada binding, binding tak
bergantung pada mana-mana keduanya.

Menyentuh dua repo: `marc_go` (teras + webhook), `marc_flutter` (UI
sambung/nyahsambung).

## Keputusan yang dibuat semasa brainstorm

| Soalan | Keputusan |
|---|---|
| Library Go untuk Telegram Bot API | `go-telegram/bot` — sifar dependency, `net/http` tulen, `context.Context` konsisten dgn corak `sqlc`/pgxpool sedia ada. Alternatif `telego` (fasthttp + codegen) ditolak: bawa pelayan HTTP kedua ke codebase yang seluruhnya Gin/`net/http` tanpa faedah sepadan di sini |
| Cara terima update | **Webhook**, bukan long polling |
| Storan chat ID | Lajur pada `users`, bukan jadual berasingan |
| Binding | 1:1 ketat — satu akaun Telegram hanya boleh ikat SATU akaun MARC |
| Pertindihan (chat sedia terikat cuba bind akaun lain) | **Tolak** |
| User sedia terikat jana token utk bind chat LAIN | **Gantikan** (padanan "permintaan baharu membunuh yg lama") |
| Nyahikat | Boleh, dari app (`DELETE /me/telegram-link`) |

### Kenapa webhook, bukan long polling

Telegram **menolak** (`409 Conflict`) lebih drpd satu proses melakukan
`getUpdates` serentak bagi token bot yang sama. Lima kerja latar sedia
ada `marc_go` (`reaper`, `retention`, dll) sengaja berjalan pada SETIAP
instance tanpa kunci teragih kerana semuanya idempoten (`TODO.md`:
*"Jangan 'betulkan' ini dengan Redis"*). Long polling tak boleh ikut
corak itu — kalau Railway jalankan >1 replica, satu je yang menang,
selebihnya asyik 409 dan retry sia-sia. Webhook elak masalah ni
sepenuhnya: Telegram hantar terus ke URL awam kita, tak kira berapa
replica di belakangnya — sama konsep webhook Stripe/ToyyibPay yang dah
wujud (`router.go`, `POST /webhooks/:gateway`).

### Kenapa lajur pada `profiles`, bukan jadual berasingan

Binding ni keadaan **kekal, tunggal, tanpa sejarah** — satu ahli ada
paling banyak satu chat Telegram terikat, tiada TTL, tiada berbilang
rekod hidup serentak. Itu profil yang sama dgn `email_verified` atau
`avatar_r2_key`: atribut akaun, bukan entiti dgn kitaran hayat sendiri.
Jadual berasingan (spt `password_reset_tokens`) wajar bila rekod itu
sementara/berbilang/boleh luput — binding kekal tak padan corak itu.

**Pembetulan drpd draf awal spec ni:** lajur diletak pada `profiles`,
BUKAN `users` — `users` cuma pegang kelayakan (id/email/password_hash),
setiap atribut akaun lain (`email_verified`, `avatar_r2_key`) sedia ada
duduk pada `profiles`. Disemak semasa tulis plan (2026-08-22), Baiki
sebelum sebarang kod ditulis.

Token pautan-dalam (deep-link) itu sendiri LAIN cerita: ia sementara,
sekali-guna, dan MEMANG dpt jadual sendiri (`telegram_link_tokens`) —
sama sebab `password_reset_tokens` berasingan drpd `users`.

## Skema

```sql
alter table profiles
  add column telegram_chat_id bigint unique,
  add column telegram_username text,
  add column telegram_linked_at timestamptz;

create table telegram_link_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index telegram_link_tokens_user_id_idx on telegram_link_tokens(user_id);
```

`token_hash` ialah SHA-256 bagi token legap 32 bait
(`auth.GenerateOpaqueToken` + `auth.HashToken`, kedua-duanya sedia ada
— corak SAMA persis dgn `password_reset_tokens`). TTL **10 minit** —
lebih pendek drpd reset kata laluan punya 1 jam, sebab aliran ni
app→Telegram serta-merta (deep-link dibuka sebaik ditekan), bukan
tunggu emel dibaca.

`telegram_chat_id unique` menguatkuasakan binding 1:1 di lapisan DB —
percubaan bind kedua bagi chat yang sama akan langgar constraint,
ditangkap dan dibalas sbg ralat dlm chat (lihat Endpoint di bawah).

## Endpoint

### `POST /me/telegram-link/token`

Auth (`RequireAuth`). Baldi had kadar **bernama sendiri**
(`telegram-link`) — pengajaran L26 langsung: trafik binding tak patut
kongsi kuota dgn `auth`/`password-reset`.

```
→  { "deep_link": "https://t.me/<TELEGRAM_BOT_USERNAME>?start=<token>" }
```

1. Padam SEMUA token binding sedia ada milik user (permintaan baharu
   membunuh yg lama — padanan corak reset kata laluan)
2. Jana token, simpan hash, `expires_at = now() + 10m`
3. Bina & pulang deep-link penuh; app buka terus guna `url_launcher`
   (dah dipakai utk muat turun sijil)

`TELEGRAM_BOT_TOKEN` kosong → **503**, sebelum sebarang kerja DB.

### `POST /webhooks/telegram`

Awam, dipanggil Telegram. **Bukan** endpoint yg app panggil. Disahkan
via header `X-Telegram-Bot-Api-Secret-Token` (dibanding
`TELEGRAM_WEBHOOK_SECRET`) — mismatch → 401, request tak diproses.

⚠️ Endpoint ni MESTI sentiasa pulang **200** ke Telegram selepas
disahkan, tak kira apa hasil pemprosesan `/start` — kalau tidak
Telegram cuba hantar update yg sama berulang kali. Sebarang ralat
"kpd pengguna" (token tak sah, pertindihan) dihantar sbg **mesej bot**
dlm chat tu, BUKAN status HTTP — asimetri berbanding endpoint lain
dlm sistem ni yg pulang ralat terus kpd caller.

Kendalikan `/start [token]`:

| Kes | Balasan bot |
|---|---|
| `/start` tanpa token | Greeting + pautan Play Store (`https://play.google.com/store/apps/details?id=com.hafizbahtiar.marc`) |
| Token tak sah/luput | Mesej ralat, minta cuba lagi dari app |
| Token sah, chat belum terikat mana-mana akaun, user (pemilik token) BELUM ada binding lain | Tuntut token (atomik, `DELETE...RETURNING`), tulis `telegram_chat_id`/`telegram_username`/`telegram_linked_at` pada `users`, balas kejayaan |
| Token sah, chat belum terikat mana-mana akaun, TAPI user (pemilik token) SUDAH ada chat lain terikat | Tuntut token, **GANTIKAN** (`telegram_chat_id` lama ditulis-timpa dgn yg baharu — chat lama senyap tak terikat lagi, tiada mesej dihantar ke chat lama krn fasa ni tiada mekanisme hantar-notifikasi lagi), balas kejayaan pada chat baharu |
| Token sah TAPI chat ni dah terikat akaun **lain** | Tolak (constraint unik / semakan eksplisit), balas ralat pertindihan — jgn tuntut token (biar ahli cuba lagi dgn akaun Telegram yg betul) |
| `/start` (chat ni dah bind akaun ni) | Balas "akaun kamu dah disambungkan", tiada tulisan DB |

### `DELETE /me/telegram-link`

Auth. Kosongkan `telegram_chat_id`/`telegram_username`/
`telegram_linked_at`. `204` sentiasa, tak kira sebelum ni terikat atau
tidak (idempoten, padanan corak lain dlm sistem ni).

### `GET /me`

Respons sedia ada tambah `telegram_linked: bool` +
`telegram_username` (nullable) — app guna ni utk papar keadaan
"Disambungkan" / butang "Sambung Telegram".

## Config (`marc_go`)

`.env` / `.env.example`:

```
# Optional — kosong = ciri binding Telegram dimatikan sepenuhnya
# (503 pd endpoint token, route webhook tak didaftar).
TELEGRAM_BOT_TOKEN=
# Username bot TANPA @, cth "MarcKelabBot" — dipakai bina deep-link.
TELEGRAM_BOT_USERNAME=
# Rahsia dikongsi, dihantar semasa setWebhook, disahkan setiap
# panggilan webhook via header X-Telegram-Bot-Api-Secret-Token.
TELEGRAM_WEBHOOK_SECRET=
```

Padanan tepat corak `PASSWORD_RESET_URL`/R2/Stripe/ToyyibPay —
kosong = ciri mati (503), bukan degradasi senyap.

## Permukaan

### marc_go

- Migration: 3 lajur `users` + jadual `telegram_link_tokens`
- `queries/telegram_link_tokens.sql` — Create/Consume (atomik,
  `delete ... returning`)/DeleteByUser, padanan
  `queries/password_reset_tokens.sql`
- `queries/users.sql` — `SetTelegramLink`/`ClearTelegramLink`
- Handler baharu (fail sendiri `internal/http/handlers/telegram.go`,
  bukan ditambah ke `auth.go` — domain berlainan)
- 3 route (token, webhook, unlink) + baldi had kadar `telegram-link`,
  dlm `router.go`
- `TELEGRAM_BOT_TOKEN`/`TELEGRAM_BOT_USERNAME`/`TELEGRAM_WEBHOOK_SECRET`
  dlm `config.go` + `.env.example`
- `setWebhook` didaftar sekali semasa boot (`cmd/api/main.go`), sama
  fasa dgn migration/Redis ping — kalau gagal, log & app tetap boot
  (padanan Redis Ping, bukan fatal)

### marc_flutter

- `profile_page.dart` — satu `ListTile` baharu dlm senarai navigasi
  sedia ada (padanan `/my-activities`, `/donate`, dll), bukan
  `about_page.dart` (itu laman rasmi/penafian, bukan tetapan akaun)
- Skrin baharu `telegram_link_page.dart` — papar keadaan
  Sambung/Disambungkan, butang buka deep-link, butang nyahsambung
- Kaedah baharu dlm service auth/profile sedia ada utk 3 endpoint di
  atas
- `url_launcher` buka deep-link (pakej dah ada dependency)

## Keselamatan

**Token binding** ikuti tepat corak reset kata laluan: legap 32-bait,
hash SHA-256 disimpan (bukan mentah), tuntutan atomik `DELETE...
RETURNING` sbg statement PERTAMA dlm transaksi — elak TOCTOU yg sama
yg `ConsumePasswordResetToken` wujud utk tutup.

**Pertindihan ditolak di lapisan DB** (`telegram_chat_id unique`) +
disahkan eksplisit dlm handler sebelum tulis, supaya mesej ralat yg
tepat boleh dihantar kpd ahli (constraint violation semata2 tak
cukup maklumat utk balasan bot yg berguna).

**Rahsia webhook** ialah token dikongsi statik (bukan HMAC spt
Stripe) — itu satu2nya mekanisme yg Telegram sediakan. Bukan
kelemahan yg kita perkenalkan, tapi patut direkod: sesiapa yg bocor
`TELEGRAM_WEBHOOK_SECRET` boleh hantar update palsu ke endpoint ni.

**Token deep-link dlm URL** — sama kelas risiko dgn token reset kata
laluan dlm pautan emel (kelihatan dlm sejarah app/clipboard sekejap).
TTL pendek (10 minit) + sekali-guna mengehadkan tetingkap eksploitasi.

## Ujian

**Go** — ujian live handler (padanan format ujian reset kata laluan):

| Kes | Invarian |
|---|---|
| Jana token, tuntut via `/start` → `telegram_chat_id` ditulis | Fungsi teras |
| Permintaan token kedua membatalkan yg pertama | Pautan lama mati |
| Token luput ditolak (mesej bot ralat, bukan 500) | TTL berkuat kuasa |
| Tuntut token sekali-guna (ujian perlumbaan, padanan
  `TestConfirmResetSekaliGunaDiBawahPerlumbaan`) | Sekali-guna atomik |
| Chat dah terikat cuba bind akaun lain → tolak, akaun asal tak berubah | Invarian 1:1 |
| User dah ada binding, jana token & bind chat baharu → chat lama tergantikan, chat baharu aktif | Gantian, bukan pertindihan-ditolak |
| Webhook tanpa/header rahsia salah → 401, tiada DB ditulis | Pengesahan webhook |
| Webhook dgn rahsia betul tapi payload rosak → 200 (bukan 500), disebabkan kontrak "Telegram jgn retry" | Kontrak balasan webhook |
| `DELETE /me/telegram-link` dua kali berturut2 → kedua-duanya 204 | Idempoten |
| `TELEGRAM_BOT_TOKEN` kosong → token endpoint 503, route webhook tak wujud (404) | Ciri mati bila tak dikonfigur |

**Ujian mutasi wajib** (standard yg ditetapkan sepanjang audit L28-L37
— ujian yg tak pernah dilihat gagal bukan ujian):

- Buang tuntutan atomik (`Consume...`) → ujian perlumbaan mesti gagal
- Buang semakan pertindihan → ujian "chat dah terikat" mesti gagal
- Tukar semakan header rahsia webhook ke selalu-lulus → ujian 401 mesti gagal

**Flutter** — ujian widget: keadaan "Sambung" vs "Disambungkan"
dipapar betul ikut `telegram_linked` dari `/me`.

## Di luar skop (fasa ni)

- **Notifikasi via bot** — fasa 2, spec berasingan
- **2FA via Telegram** — fasa 3, spec berasingan
- **Perintah bot selain `/start`** — tiada keperluan lg buat masa ni;
  bot ni hanya kendali binding dlm fasa 1
- **Papar senarai ahli terikat kpd admin** — tak diminta, tak dibina
