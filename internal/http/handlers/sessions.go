package handlers

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// sessionResponse - SATU sesi = SATU family refresh token (satu device),
// bukan satu baris hash. Refresh memutar hash baru dalam family yang
// sama; senarai per-baris akan nampak macam banyak device.
type sessionResponse struct {
	ID        string  `json:"id"`
	UserAgent *string `json:"user_agent"`
	CreatedIP *string `json:"created_ip"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
	IsCurrent bool    `json:"is_current"`
}

// groupSessionsByFamily - satu baris per family_id. created_at = log
// masuk asal (paling awal), expires_at = token termuda (baki TTL
// sebenar), metadata device dari baris paling awal (label tak berubah
// semasa rotate).
func groupSessionsByFamily(rows []sqlc.RefreshToken, currentFamily uuid.UUID) []sessionResponse {
	type acc struct {
		family    uuid.UUID
		ua        *string
		ip        *string
		createdAt sqlc.RefreshToken
		expiresAt sqlc.RefreshToken
	}
	byFamily := make(map[uuid.UUID]*acc, len(rows))
	order := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		a, ok := byFamily[r.FamilyID]
		if !ok {
			cp := r
			byFamily[r.FamilyID] = &acc{
				family:    r.FamilyID,
				ua:        textToPtr(r.UserAgent),
				ip:        textToPtr(r.CreatedIp),
				createdAt: cp,
				expiresAt: cp,
			}
			order = append(order, r.FamilyID)
			continue
		}
		if r.CreatedAt.Valid && (!a.createdAt.CreatedAt.Valid || r.CreatedAt.Time.Before(a.createdAt.CreatedAt.Time)) {
			a.createdAt = r
			a.ua = textToPtr(r.UserAgent)
			a.ip = textToPtr(r.CreatedIp)
		}
		if r.ExpiresAt.Valid && (!a.expiresAt.ExpiresAt.Valid || r.ExpiresAt.Time.After(a.expiresAt.ExpiresAt.Time)) {
			a.expiresAt = r
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		a, b := byFamily[order[i]], byFamily[order[j]]
		return a.createdAt.CreatedAt.Time.After(b.createdAt.CreatedAt.Time)
	})

	res := make([]sessionResponse, 0, len(order))
	for _, id := range order {
		a := byFamily[id]
		res = append(res, sessionResponse{
			ID:        a.family.String(),
			UserAgent: a.ua,
			CreatedIP: a.ip,
			CreatedAt: formatTime(a.createdAt.CreatedAt),
			ExpiresAt: formatTime(a.expiresAt.ExpiresAt),
			IsCurrent: currentFamily != uuid.Nil && a.family == currentFamily,
		})
	}
	return res
}

// ListMySessions - GET /me/sessions. Self-service: ahli sendiri sahaja.
// Query ditapis dengan user id dari token, bukan dari parameter.
func (h *AuthHandler) ListMySessions(c *gin.Context) {
	rows, err := h.queries.ListActiveRefreshTokensByUser(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai sesi"})
		return
	}

	c.JSON(http.StatusOK, groupSessionsByFamily(rows, middleware.SessionID(c)))
}

// RevokeMySession - DELETE /me/sessions/:id. `:id` ialah family_id (id
// dalam GET /me/sessions), bukan hash token individu. Padam SELURUH
// family supaya rotate tak "hidupkan semula" device yang baru dilog
// keluar.
func (h *AuthHandler) RevokeMySession(c *gin.Context) {
	sessionID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	rows, err := h.queries.DeleteRefreshTokenFamilyByIDAndUser(c.Request.Context(), sqlc.DeleteRefreshTokenFamilyByIDAndUserParams{
		FamilyID: sessionID,
		UserID:   middleware.UserID(c),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal log keluar sesi"})
		return
	}
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "sesi tidak dijumpai"})
		return
	}

	c.Status(http.StatusNoContent)
}

type revokeMySessionsRequest struct {
	IDs []string `json:"ids" binding:"required,min=1,max=50,dive,uuid"`
}

type revokeMySessionsResponse struct {
	Deleted int64 `json:"deleted"`
}

// RevokeMySessions - POST /me/sessions/revoke. Padam banyak family
// milik pemanggil dalam SATU query. Id milik ahli lain atau tak wujud
// diabaikan senyap.
func (h *AuthHandler) RevokeMySessions(c *gin.Context) {
	var req revokeMySessionsRequest
	if !bindJSON(c, &req) {
		return
	}

	ids := make([]uuid.UUID, len(req.IDs))
	for i, s := range req.IDs {
		id, err := uuid.Parse(s)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id sesi tidak sah"})
			return
		}
		ids[i] = id
	}

	deleted, err := h.queries.DeleteRefreshTokensByIDsAndUser(c.Request.Context(), sqlc.DeleteRefreshTokensByIDsAndUserParams{
		UserID: middleware.UserID(c),
		Ids:    ids,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal log keluar sesi"})
		return
	}
	if deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "sesi tidak dijumpai"})
		return
	}

	c.JSON(http.StatusOK, revokeMySessionsResponse{Deleted: deleted})
}
