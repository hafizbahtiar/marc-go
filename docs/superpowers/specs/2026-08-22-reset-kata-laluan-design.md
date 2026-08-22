# Reset kata laluan — reka bentuk (2026-08-22)

Menutup **L32** (`TODO.md`): ahli yang lupa kata laluan tiada laluan pulih
dalam app. Sekarang ia memerlukan staf mengemas kini `users.password_hash`
secara manual melalui akses DB terus.

Menyentuh tiga repo: `marc_go` (teras), `marc_astro` (halaman kata laluan
baharu), `marc_flutter` (titik masuk).

## Keputusan yang dibuat semasa brainstorm

| Soalan | Keputusan |
|---|---|
| Di mana kata laluan baharu ditaip? | **Halaman Astro**, bukan kod dalam app |
| Skop | **Reset sahaja** — tukar-semasa-log-masuk dikecualikan |
| Emel tak dikenali | **Sentiasa 204** — tiada enumerasi |
| Storan token | **Jadual baharu**, bukan guna semula atau tanpa keadaan |

### Kenapa halaman web, bukan kod dalam app

Tiada app-link https dikonfigur (hanya skema tersuai
`marc://stripe-redirect` untuk Stripe), jadi pautan dalam emel membuka
**pelayar**, bukan app. Laluan web sudah terbukti hidup: `sahkan-emel.astro`
memanggil `POST /auth/verify-email/confirm` silang-origin, dan middleware
CORS (`internal/http/middleware/cors.go`) dibina khas untuk kes itu.

Alternatifnya — kod 6 digit yang ditaip dalam app — memperkenalkan
kelayakan yang **boleh dibrute-force** ke dalam sistem auth yang setakat
ini hanya menggunakan token entropi tinggi. Ia memerlukan had percubaan
per-token dan penguncian, iaitu mesin keselamatan baharu direka untuk
masalah yang laluan web tak ada.

### Kenapa jadual berasingan

Token reset MESTI sekali-guna dan MESTI boleh dibatalkan sebelum luput.
Token bertandatangan tanpa keadaan (JWT) tak boleh jadi kedua-duanya —
pautan yang sama berfungsi berulang kali sehingga luput, dan meminta reset
baharu tak boleh membunuh yang lama. Untuk kelayakan yang memberi kawalan
penuh akaun, itu tak boleh diterima.

Menggunakan semula `email_verification_tokens` dengan lajur `purpose` akan
menggabungkan dua kitaran hayat berbeza dan memerlukan migration atas
jadual yang sedang berfungsi — membeli kekemasan skema dengan risiko pada
laluan yang tiada kaitan.

## Skema

Cerminan `email_verification_tokens`:

```sql
create table password_reset_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index password_reset_tokens_user_id_idx on password_reset_tokens(user_id);
```

`token_hash` ialah SHA-256 bagi token legap 32 bait (`auth.GenerateOpaqueToken`
+ `auth.HashToken`, kedua-duanya sedia ada). Token MENTAH hanya wujud dalam
emel; kalau DB bocor, hash tak boleh digunakan untuk reset apa-apa.

TTL **1 jam**, sama seperti pengesahan emel.

## Endpoint

### `POST /auth/password-reset/request`

Awam. Dipanggil dari app Flutter. Baldi had kadar bernama `password-reset`.

```
{ "email": "ahli@contoh.com" }  →  204 (SENTIASA)
```

1. Normalkan emel (huruf kecil, pangkas)
2. Cari user — tiada? pulang 204 tanpa kerja lanjut
3. Padam SEMUA token reset sedia ada ahli itu
4. Jana token, simpan hash, `expires_at = now() + 1h`
5. Hantar emel **dalam goroutine** (lihat Keselamatan)

`PASSWORD_RESET_URL` kosong → **503** dengan mesej jelas, sebelum sebarang
kerja DB.

### `POST /auth/password-reset/confirm`

Awam. Dipanggil dari halaman Astro, jadi ia mendapat CORS + pengendali
`OPTIONS` — padanan tepat `verify-email/confirm`. Baldi had kadar sama.

```
{ "token": "...", "password": "..." }  →  204
```

Dalam SATU transaksi:

1. Cari ikut hash token — tiada → `400 "pautan tidak sah"`
2. Luput → padam baris, `400 "pautan sudah luput"`
3. Hash kata laluan baharu (bcrypt)
4. Kemas kini `users.password_hash`
5. Padam SEMUA token reset ahli
6. Padam SEMUA refresh token ahli

Peraturan kata laluan padan `/auth/register`: `min=6,max=72` (72 ialah had
bcrypt, bukan pilihan sewenang-wenang).

## Keselamatan

**Bukan-enumerasi.** Permintaan sentiasa pulang 204. Mesej UI berbunyi
*"Kalau emel itu berdaftar, kami dah hantar pautan reset"* — ahli yang
tersilap taip tetap dapat maklum balas berguna tanpa server mengesahkan
kewujudan akaun.

**Emel dihantar dalam goroutine** — untuk **masa**, bukan latensi. Kalau
akaun wujud kita panggil Resend (~200ms); kalau tidak kita pulang
serta-merta. Perbezaan itu ialah oracle enumerasi yang mengalahkan
keputusan 204 di atas.

> Ini mitigasi **separa** dan didokumen begitu: kerja DB masih berbeza
> beberapa milisaat antara dua laluan. Perbezaan itu jauh di bawah bunyi
> rangkaian, jadi ia diterima — tetapi ia bukan sifar, dan tiada siapa
> patut membaca kod ni dan menganggap masanya seragam.

**Reset membatalkan setiap sesi.** Sebab orang reset selalunya kerana syak
akaun dikompromi; membiarkan refresh token penyerang hidup mengalahkan
tujuan reset. Pembatalan berlaku dalam transaksi yang SAMA dengan tukar
kata laluan — padanan corak `setMemberStatus` pada laluan tolak-ahli.

**Sekali-guna** dikuatkuasakan dengan memadam token dalam transaksi yang
sama. **Permintaan baharu membunuh yang lama** (langkah 3 pada request).

**Baldi had kadar bernama sendiri** (`password-reset`), bukan berkongsi
`auth`. Ini pengajaran L26 secara langsung: trafik reset tak patut
menghabiskan kuota log masuk ahli, dan sebaliknya.

**Berfungsi untuk SEBARANG status profil**, termasuk `pending`/`rejected`.
Alasan sama dengan `/me` (lihat `ARCHITECTURE.md`, Lapisan akses): ahli
yang terkunci keluar mesti boleh pulih, kalau tidak mereka terperangkap.

**TIDAK menanda `email_verified = true`.** Mengklik pautan reset memang
membuktikan kawalan emel — tetapi menggabungkan keduanya bermakna akaun
yang dikompromi lalu direset senyap memperoleh status disahkan. Kekalkan
dua invarian itu berasingan.

**Tiada fallback HTML Go** bila `PASSWORD_RESET_URL` kosong. Borang kata
laluan bukan sesuatu yang patut muncul daripada halaman sandaran yang
tiada siapa reka. Ciri dimatikan dengan 503 jelas — padanan corak config
R2/Stripe/ToyyibPay (`ARCHITECTURE.md`, Config).

## Permukaan

### marc_go

- Migration `password_reset_tokens`
- `queries/password_reset_tokens.sql` — Create/GetByHash/DeleteByUser/Delete
- Query kata laluan pada `queries/users.sql` — `UpdateUserPassword`
- Handler dalam `internal/http/handlers/auth.go` (bersama laluan auth lain)
- Dua route + baldi had kadar + CORS pada `confirm`, dalam `router.go`
- `PASSWORD_RESET_URL` dalam `config.go` + `.env.example`

### marc_astro

`src/pages/reset-kata-laluan.astro`, mencerminkan `sahkan-emel.astro`
dengan satu perbezaan: `sahkan-emel` **auto-hantar** (tiada input
pengguna), reset perlukan **borang**. Keadaan:

```
form (kata laluan + sahkan)  →  loading  →  success / error
```

Pengesahan sisi klien (min 6, dua medan sepadan) untuk maklum balas
serta-merta. Server tetap menguatkuasakan sendiri — semakan borang bukan
sempadan. Token dibaca daripada `?token=`; hilang → terus ke `error`.
Guna `PUBLIC_API_BASE_URL` sedia ada.

### marc_flutter

- `login_page.dart` — pautan "Lupa kata laluan?"
- `forgot_password_page.dart` **baharu** — satu medan emel, satu mesej neutral
- Laluan dalam `router.dart`, kaedah dalam `auth_service.dart`

**Tiada** skrin kata laluan baharu — itu milik Astro. Skop Flutter sengaja
nipis.

## Ujian

**Go** — ujian live handler (memerlukan Postgres; kini berjalan dalam CI
selepas L14):

| Kes | Invarian |
|---|---|
| Emel tak dikenali → 204, sifar token ditulis | Bukan-enumerasi |
| Permintaan kedua membatalkan token pertama | Pautan lama mati |
| Confirm menukar kata laluan; yang lama ditolak | Fungsi teras |
| Confirm sekali-guna | Pautan diguna semula gagal |
| Token luput ditolak | TTL berkuat kuasa |
| Confirm membatalkan SEMUA refresh token | Tujuan reset |
| Berfungsi untuk ahli `pending` | Terkunci keluar boleh pulih |

**Ujian mutasi wajib** (corak yang ditetapkan sepanjang audit ni — ujian
yang tak pernah dilihat gagal bukan ujian):

- Buang pembatalan refresh-token → ujian sesi mesti gagal
- Buang pemadaman token → ujian sekali-guna mesti gagal

**Flutter** — ujian widget untuk `forgot_password_page`: mesej neutral
dipaparkan tanpa mengira respons server.

**Astro** — ⚠️ repo itu **tiada suite ujian**. Pengesahan ialah
`astro check` + `astro build` + pemeriksaan manual. Mencipta rangka kerja
ujian di sana adalah di luar skop L32 dan kekal sebagai keputusan berasingan.

## Di luar skop

Direkod supaya ia tidak diselinapkan masuk, dan tidak hilang:

- **Tukar kata laluan semasa log masuk** (`PATCH /me/password`) — ditolak
  secara eksplisit semasa brainstorm. Bukan penyekat: ahli yang syak akaun
  dikompromi sudah ada `POST /auth/logout-all`.
- **`POST /auth/register` membocorkan enumerasi** — ia pulang
  `409 "email ini sudah berdaftar"`. Isu SEDIA ADA, bukan diperkenalkan di
  sini, dan membaikinya menyentuh aliran pendaftaran yang tiada kaitan.
  Direkod sebagai **L37** dalam `TODO.md`.

  > Nota: ini bermakna keputusan bukan-enumerasi di atas TIDAK menutup
  > enumerasi merentas sistem. Sesiapa yang mahu menyenaraikan emel akan
  > guna `register`. Itu bukan hujah untuk membocorkannya di sini juga —
  > menambah kebocoran kedua menjadikan pembaikan nanti lebih mahal.
