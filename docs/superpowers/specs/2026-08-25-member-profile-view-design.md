# Skrin profil ahli lain (view-only, tiered) (2026-08-25)

Menyentuh dua repo: `marc_go` (endpoint baru), `marc_flutter` (skrin baru).
Time-boxed - spec ringkas + terus laksana guna subagent, bukan proses SDD
penuh (padanan pendekatan `2026-08-25-profile-extend-design.md`).

## Masalah

Tiada API untuk lihat profil AHLI LAIN (bukan `/me`) - cuma `GET /members`
(senarai), yang dah ada penapisan sebahagian (emel/status bayaran
disembunyikan drpd bukan-management, keterlihatan baris ikut
`visibleRankCeiling`) tapi tak pernah dedah telefon/waris/kesihatan/alamat
langsung kepada sesiapa selain pemilik sendiri. Di Flutter, `members_page.dart`
punya `_MemberTile.onTap` cuma buka sheet tindakan pengurusan (role/status/
bahagian) bila viewer ada kebenaran edit - ahli biasa yang ketik baris tak
buat apa-apa, dan tiada skrin profil untuk viewer TENGOK sahaja.

## Skop

Endpoint + skrin profil satu-ahli, tiga peringkat keterlihatan medan
(dikuatkuasakan SERVER-side - client render apa sahaja yang response bagi,
tiada logik sembunyi/tunjuk medan berasaskan role di client):

1. **Standard** (mana-mana ahli `approved`, tengok sesiapa dlm skop
   `visibleRankCeiling` sedia ada): nama, gambar, no. ahli, role, bahagian,
   jawatan, status aktif.
2. **Admin** (`RoleCategory == management`, iaitu supervisor/manager/
   superadmin): + emel, telefon, status bayaran pendaftaran.
3. **Superadmin** (`role_key == "superadmin"`, rank 100 - "owner/aku"): +
   kenalan kecemasan (nama+telefon), nota kesihatan, status pautan
   Telegram + username, **senarai alamat penuh**.

Keputusan eksplisit dari perbincangan: admin (supervisor/manager) BUKAN
superadmin - dua peringkat berasingan, superadmin nampak SEMUA (bukan
sekadar peringkat admin + sikit lagi).

## Keputusan reka bentuk

| Soalan | Keputusan | Sebab |
|---|---|---|
| Endpoint baru atau luaskan `GET /members`? | **Endpoint baru** `GET /members/:id` | Senarai (`memberResponse`) dan detail satu-ahli (`memberDetailResponse`) ada bentuk berbeza (detail perlu alamat, waris, kesihatan - medan yang tak masuk akal dlm payload senarai berbilang baris) |
| Query pangkalan data baru? | **Tiada baru** - `GetProfileByUserID` (sedia ada, `select p.*` + join role/email/department) dipakai semula untuk sesiapa sahaja (bukan cuma caller), `ListAddressesByUser` (sedia ada) juga sudah generik ikut `user_id`, cuma laluan `/me/addresses` yang sekat kepada diri sendiri - laluan baru boleh panggil terus dengan `:id` |
| Keterlihatan BARIS (boleh nampak ahli ni langsung ke tidak) | Guna semula `visibleRankCeiling(roles, caller.RoleRank)` - kalau target rank > ceiling, `404` (bukan `403` - elak dedah kewujudan baris rank tinggi kepada viewer bawah, padanan cara `GetPostByID` dsb pulang `404` generik) | Konsisten dgn peraturan senarai sedia ada - tiada peraturan keterlihatan baris baharu |
| Keterlihatan MEDAN (tier 1/2/3) | Dikira di handler ikut `caller.RoleCategory`/`caller.RoleKey`, bukan target punya rank - pointer medan `nil` kalau caller tak layak, padanan pola `Email`/`RegistrationPaymentStatus` sedia ada dlm `memberResponse` | Sama corak sedia ada, tak perlu struktur baru |
| Superadmin dikenal pasti macam mana? | `caller.RoleKey == "superadmin"` (bukan rank>=100 hardcoded - `role_key` lagi eksplisit & stabil kalau rank seed berubah kelak) | Padanan cara migration seed roles guna `key` sebagai identiti primer |
| Alamat: query berasingan atau embed dlm response utama? | **Panggilan berasingan** dalam handler yang sama (satu round-trip HTTP, dua query DB), field `addresses: []addressResponse` dlm `memberDetailResponse`, cuma diisi (bukan `nil`) kalau caller superadmin | Elak N+1 di client; alamat cuma relevan utk tier tertinggi jadi tak rugi skip query utk viewer lain |

## API (`marc_go`)

**`GET /members/:id`** (`:id` = `user_id`, padanan laluan `/members/:id/...`
sedia ada), route group `approved` (sama macam `GET /members`).

Response `memberDetailResponse` (200):
```json
{
  "user_id": "uuid",
  "member_id": "string",
  "display_name": "string | null",
  "avatar_url": "string | null",
  "role_key": "string",
  "role_name": "string",
  "role_rank": 10,
  "category": "string",
  "status": "string",
  "is_active": true,
  "department_code": "string | null",
  "department_name": "string | null",
  "position": "string | null",

  "email": "string | null",
  "phone": "string | null",
  "registration_payment_status": "string | null",

  "emergency_contact_name": "string | null",
  "emergency_contact_phone": "string | null",
  "health_notes": "string | null",
  "telegram_linked": "bool | null",
  "telegram_username": "string | null",
  "addresses": "[]addressResponse | null"
}
```

Tier 2 (`email`/`phone`/`registration_payment_status`) dan Tier 3 (baki
medan) sentiasa hadir dlm JSON tapi bernilai `null` kalau caller tak
layak - padanan corak `Email`/`RegistrationPaymentStatus` dlm
`memberResponse` sedia ada (client bezakan "null = disembunyikan" drpd
"tiada nilai", tak perlu logik keadaan tambahan).

Handler:
1. `caller := h.queries.GetProfileByUserID(ctx, callerUserID)` (dari JWT).
2. `target := h.queries.GetProfileByUserID(ctx, targetID)` - `404` kalau
   tak jumpa.
3. `roles := h.queries.ListRoles(ctx)`; kalau
   `target.RoleRank > visibleRankCeiling(roles, caller.RoleRank)` → `404`.
4. Bina `memberDetailResponse` drpd `target` (medan Tier 1 sentiasa isi).
5. Kalau `caller.RoleCategory == authz.CategoryManagement`: isi Tier 2.
6. Kalau `caller.RoleKey == "superadmin"`: isi Tier 3 + panggil
   `h.queries.ListAddressesByUser(ctx, targetID)`, map ke
   `[]addressResponse` (struct sedia ada dari `2026-08-25-profile-extend-design.md`).
7. `avatar_url` guna `avatarURLFor` (helper sedia ada, sama pola posts).

Tiada audit log - ini bacaan sahaja (padanan `GET /members`, tiada
audit utk senarai/lihat, cuma utk TINDAKAN spt approve/reject/tukar role).

## Flutter (`marc_flutter`)

- `MemberDetail` model baru (`lib/features/members/member_detail_model.dart`
  atau lokasi serupa `MemberRow`) - semua medan `memberDetailResponse`,
  `String?`/`bool?` utk medan tier 2/3 (null = viewer tak layak, bukan
  "kosong" - UI skip seksyen tu terus, jangan papar label+"-").
- `memberDetailProvider = FutureProvider.family<MemberDetail, String>`
  (`dio.get('/members/$userId')`), padanan pola `postDetailProvider`.
- Skrin baru `lib/features/members/member_detail_page.dart`
  (`MemberDetailPage({required this.userId})`), route
  `/members/:userId` (`app/router.dart`).
  - Header: `MemberAvatar` (reuse, radius besar macam `ProfilePage._Header`)
    + `heroTag`/tap-to-view (reuse `ImageViewerPage`, padanan kerja avatar
    sesi ni) + nama + no. ahli + chip role/bahagian/jawatan + status aktif.
  - Seksyen "Kenalan" (Tier 2) - render HANYA kalau `email`/`phone`
    tak null; kalau caller Tier 1, seksyen ni terus tak wujud (bukan
    dipaparkan kosong).
  - Seksyen "Kecemasan & Kesihatan" (Tier 3) - sama corak, render kalau
    ada sebarang medan Tier 3 bukan null.
  - Seksyen "Alamat" (Tier 3) - senarai ringkas (reuse gaya tile
    `manage_addresses_page.dart`, TANPA aksi edit/padam - view-only).
  - Butang tindakan pengurusan (role/status/bahagian - `_showMemberActionsSheet`
    sedia ada, `members_page.dart`) - pindah ke sini sebagai `IconButton`
    di AppBar, kelihatan HANYA kalau `canEdit`/`canEditDeptPosition`
    (kira semula di sini, sama formula rank sedia ada), bukan lagi tindakan
    ketik baris di senarai.
- `members_page.dart`: `_MemberTile.onTap` tukar drpd
  `canEdit ? openSheet : null` kepada **sentiasa**
  `() => context.push('/members/${row.userId}')` - semua viewer (termasuk
  yang ada kebenaran edit) navigasi ke skrin profil; sheet tindakan
  pindah ke skrin tu (atas).
- UI bahasa Melayu, ikut tema `AppTheme` sedia ada.

## Pelan pelaksanaan (ringkas)

1. **Backend**: struct `memberDetailResponse` + handler `GetMemberDetail`
   (guna semula `GetProfileByUserID`/`ListRoles`/`ListAddressesByUser`/
   `visibleRankCeiling`/`avatarURLFor` - tiada migration, tiada query sqlc
   baru), daftar `approved.GET("/members/:id", profileHandler.GetMemberDetail)`
   (`router.go`). Tiada risiko pertembungan laluan: `/members` (senarai)
   satu segmen, `/members/:id` (baru) dua segmen, semua tindakan sedia ada
   (`/members/:id/approve` dsb) tiga segmen - Gin padan ikut kedalaman
   laluan penuh, bukan awalan, jadi tertib pendaftaran tak penting di
   sini. `go build`/`go vet`/test unit sedia ada lulus.
2. **Frontend**: model + provider + `MemberDetailPage` + route +
   `members_page.dart` tukar `onTap`, pindah sheet tindakan ke skrin baru.
   `dart analyze`/`flutter test` lulus.
3. Manual sanity: log masuk sbg ahli biasa - tengok profil ahli lain
   (Tier 1 sahaja); log masuk sbg manager - tengok profil (Tier 1+2); log
   masuk sbg superadmin - tengok profil (semua tier + alamat).
