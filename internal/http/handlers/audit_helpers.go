package handlers

import (
	"github.com/gin-gonic/gin"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// auditActor bina audit.Actor daripada permintaan semasa.
//
// Snapshot member_id/role adalah best-effort: kalau profil gagal dibaca,
// jejak tetap direkod dengan user_id sahaja. Catatan tanpa nama role lebih
// baik daripada permintaan yang gagal semata-mata sebab metadata audit tak
// lengkap — berbeza dengan catatan audit itu sendiri, yang MESTI berjaya
// (lihat audit.Record).
func auditActor(c *gin.Context, q *sqlc.Queries) audit.Actor {
	userID := middleware.UserID(c)
	actor := audit.Actor{
		UserID:    userID,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}
	if profile, err := q.GetProfileByUserID(c.Request.Context(), userID); err == nil {
		actor.MemberID = profile.MemberID
		actor.RoleKey = profile.RoleKey
	}
	return actor
}
