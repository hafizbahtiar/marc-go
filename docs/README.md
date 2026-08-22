# docs/

Dokumen berumur panjang yang tak sesuai duduk dalam `TODO.md` (kerja belum
siap) atau `ARCHITECTURE.md`/`DATABASE.md` (rujukan hidup).

```
docs/
  audits/       — laporan audit penuh, satu fail setiap pusingan
  superpowers/
    specs/      — reka bentuk yang dipersetujui sebelum kod ditulis
    plans/      — pecahan pelaksanaan yang terhasil daripada spec
```

## Audit

Ringkasan setiap pusingan dan status setiap item hidup dalam `TODO.md`
(label `L##`, bahagian Security). Fail di sini ialah **laporan penuh** —
bukti, laluan kod, dan sebab sesuatu itu dinilai pada keterukan tertentu.

| Tarikh | Skop | Laporan |
|---|---|---|
| 2026-08-14 | Modul aktiviti | `TODO.md` sahaja (L1–L15) |
| 2026-08-15 | `internal/` menyeluruh + pusingan verifikasi | `TODO.md` sahaja (L16–L27) |
| 2026-08-22 | `queries/` + `internal/` + `cmd/` | [`audits/2026-08-22-queries-internal-cmd.md`](./audits/2026-08-22-queries-internal-cmd.md) (L28–L36) |

## Spec & plan

| Tarikh | Dokumen |
|---|---|
| 2026-08-07 | [Kelulusan status ahli — spec](./superpowers/specs/2026-08-07-member-approval-status-design.md) · [plan](./superpowers/plans/2026-08-07-member-approval-status.md) |
| 2026-08-09 | [Modul aktiviti — spec](./superpowers/specs/2026-08-09-modul-aktiviti-design.md) · [plan](./superpowers/plans/2026-08-09-modul-aktiviti.md) |
| 2026-08-22 | [Reset kata laluan — spec](./superpowers/specs/2026-08-22-reset-kata-laluan-design.md) · [plan](./superpowers/plans/2026-08-22-reset-kata-laluan.md) (L32) |

Dua ciri yang **belum** ada spec: onboard ahli lama (brainstorm terhenti
di pertengahan) dan auto-purge permintaan pemadaman akaun. Lihat `TODO.md`.
