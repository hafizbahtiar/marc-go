package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// sessionResponse - satu baris refresh_tokens yang masih hidup, dilihat
// dari sudut ahli ("device ni pernah log masuk").
//
// TIADA medan `is_current`: /me/sessions dipanggil dengan ACCESS token,
// dan access token (internal/auth/jwt.go) cuma bawa `sub` - tiada jti
// mahupun rujukan kepada baris refresh token yang mengeluarkannya. Jadi
// server TAK BOLEH tahu baris mana milik device yang sedang bertanya
// tanpa mereka-reka mekanisme penjejakan baharu. Senarai tanpa penanda
// "ini device anda" masih berguna dan jujur; menekanya ikut tekaan
// (cth "yang paling baharu") akan buat ahli log keluar device salah.
type sessionResponse struct {
	ID        string  `json:"id"`
	UserAgent *string `json:"user_agent"`
	CreatedIP *string `json:"created_ip"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
}

func toSessionResponse(t sqlc.RefreshToken) sessionResponse {
	return sessionResponse{
		ID:        t.ID.String(),
		UserAgent: textToPtr(t.UserAgent),
		CreatedIP: textToPtr(t.CreatedIp),
		CreatedAt: formatTime(t.CreatedAt),
		ExpiresAt: formatTime(t.ExpiresAt),
	}
}

// ListMySessions - GET /me/sessions. Self-service: ahli sendiri sahaja.
// Query ditapis dengan user id dari token, bukan dari parameter.
func (h *AuthHandler) ListMySessions(c *gin.Context) {
	rows, err := h.queries.ListActiveRefreshTokensByUser(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai sesi"})
		return
	}

	res := make([]sessionResponse, len(rows))
	for i, row := range rows {
		res[i] = toSessionResponse(row)
	}
	c.JSON(http.StatusOK, res)
}

// RevokeMySession - DELETE /me/sessions/:id. Adik-beradik satu-baris
// kepada LogoutAll: padam SATU sesi (device) tanpa menjatuhkan yang
// lain. Ownership dikuatkuasakan DALAM query (id AND user_id), jadi id
// milik ahli lain berkelakuan sama macam id yang tak wujud - 404, tiada
// kebocoran kewujudan.
func (h *AuthHandler) RevokeMySession(c *gin.Context) {
	sessionID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	rows, err := h.queries.DeleteRefreshTokenByIDAndUser(c.Request.Context(), sqlc.DeleteRefreshTokenByIDAndUserParams{
		ID:     sessionID,
		UserID: middleware.UserID(c),
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

// RevokeMySessions - POST /me/sessions/revoke. Padam banyak sesi (device)
// milik pemanggil dalam SATU query. Id milik ahli lain atau tak wujud
// diabaikan senyap (hanya baris milik sendiri dipadam) - padanan
// DeleteRefreshTokensByIDsAndUser.
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
