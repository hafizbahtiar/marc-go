package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// auditActor bina audit.Actor daripada permintaan semasa.
//
// Snapshot member_id/role adalah best-effort: kalau profil gagal dibaca,
// jejak tetap direkod dengan user_id sahaja. Catatan tanpa nama role lebih
// baik daripada permintaan yang gagal semata-mata sebab metadata audit tak
// lengkap - berbeza dengan catatan audit itu sendiri, yang MESTI berjaya
// (lihat audit.Record).
//
// `q` MESTI Queries yang terikat pada transaksi mutasi apabila pemanggil
// sedang memegang satu (L13, dibaiki 2026-08-22) - BUKAN `h.queries`
// yang terikat kolam.
//
// Sebabnya KETERSEDIAAN, bukan ketepatan. `pgxpool` tanpa konfigurasi
// memberi `MaxConns = max(4, NumCPU)`. Kalau fungsi ni membaca melalui
// kolam sambil pemanggil memegang transaksi, setiap tulisan pengurusan
// memerlukan DUA sambungan serentak - jadi pada kotak 2-vCPU, empat
// tulisan serentak boleh memegang keempat-empat sambungan sambil
// kesemuanya menunggu sambungan kelima yang takkan datang. Berbuntu
// sehingga timeout klien.
//
// Guna `q` menghapuskan kelas kegagalan itu sepenuhnya: bacaan berjalan
// atas sambungan yang SUDAH dipegang. Ini pembaikan yang lebih baik
// daripada cadangan asal L13 ("nilai sebelum Begin") - ia bukan sekadar
// mengelak dua sambungan SERENTAK, ia langsung tak minta yang kedua.
//
// Pemanggil yang BELUM membuka transaksi betul menghantar `h.queries`:
// `Issue`/`Revoke` sijil dan `Mark` kehadiran menilai actor dahulu lalu
// menyerahkannya kepada fungsi yang membuka tx sendiri - tiada transaksi
// wujud untuk berlumba dengannya pada titik itu.
func auditActor(c *gin.Context, q *sqlc.Queries) audit.Actor {
	userID := middleware.UserID(c)
	actor := audit.Actor{
		UserID:    userID,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}
	if profile, err := q.GetProfileByUserID(c.Request.Context(), userID); err == nil {
		if profile.MemberID.Valid {
			actor.MemberID = profile.MemberID.String
		}
		actor.RoleKey = profile.RoleKey
	}
	return actor
}

// actorAuditFields - salin identiti pelaku ke dalam delta jsonb (new_values)
// supaya bacaan timeline entiti (cth kelulusan/batal bil ahli pending)
// nampak SIAPA admin tanpa silang-rujuk lajur actor_id jadual audit_logs.
// UUID wajib bila UserID ada - bezakan admin A vs admin B walaupun role sama.
func actorAuditFields(actor audit.Actor) map[string]any {
	fields := map[string]any{}
	if actor.UserID != uuid.Nil {
		fields["actor_user_id"] = actor.UserID.String()
	}
	if actor.MemberID != "" {
		fields["actor_member_id"] = actor.MemberID
	}
	if actor.RoleKey != "" {
		fields["actor_role_key"] = actor.RoleKey
	}
	return fields
}

func mergeAuditFields(base map[string]any, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}
