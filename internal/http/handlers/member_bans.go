package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type MemberBanHandler struct {
	queries *sqlc.Queries
}

func NewMemberBanHandler(queries *sqlc.Queries) *MemberBanHandler {
	return &MemberBanHandler{queries: queries}
}

type bannedMemberResponse struct {
	UserID       string  `json:"user_id"`
	MemberID     *string `json:"member_id"`
	DisplayName  *string `json:"display_name"`
	Email        string  `json:"email"`
	RoleKey      string  `json:"role_key"`
	BannedAt     string  `json:"banned_at"`
	BanExpiresAt *string `json:"ban_expires_at"`
	BanReason    string  `json:"ban_reason"`
	BannedBy     *string `json:"banned_by"`
}

func (h *MemberBanHandler) requireSuperAdmin(c *gin.Context) bool {
	role, err := h.queries.GetRoleKeyByUserID(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if role != superAdminRoleKey {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk superadmin sahaja"})
		return false
	}
	return true
}

func (h *MemberBanHandler) List(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	rows, err := h.queries.ListBannedProfiles(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai akaun digantung"})
		return
	}
	result := make([]bannedMemberResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, toBannedMemberResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"members": result})
}

type banMemberRequest struct {
	ExpiresAt string `json:"expires_at"`
	Reason    string `json:"reason" binding:"required,max=500"`
}

func (h *MemberBanHandler) Ban(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id ahli tidak sah"})
		return
	}
	if targetID == middleware.UserID(c) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "akaun sendiri tidak boleh digantung"})
		return
	}
	var request banMemberRequest
	if !bindJSON(c, &request) {
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sebab penggantungan diperlukan"})
		return
	}

	target, err := h.queries.GetProfileByUserID(c.Request.Context(), targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	if target.RoleKey == superAdminRoleKey {
		c.JSON(http.StatusForbidden, gin.H{"error": "akaun superadmin tidak boleh digantung"})
		return
	}

	var expiresAt pgtype.Timestamptz
	if strings.TrimSpace(request.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
		if err != nil || !parsed.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tarikh tamat ban tidak sah"})
			return
		}
		expiresAt = pgtype.Timestamptz{Time: parsed, Valid: true}
	}

	row, err := h.queries.BanProfile(c.Request.Context(), sqlc.BanProfileParams{
		UserID:    targetID,
		BanReason: pgtype.Text{String: reason, Valid: true},
		BannedBy: pgtype.UUID{
			Bytes: uuid.UUID(middleware.UserID(c)),
			Valid: true,
		},
		BanExpiresAt: expiresAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusConflict, gin.H{"error": "akaun ini sudah digantung"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal menggantung akaun"})
		return
	}
	if err := audit.Record(c.Request.Context(), h.queries, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"banned": false},
		New:        map[string]any{"banned": true, "ban_expires_at": request.ExpiresAt, "reason": reason},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal merekod penggantungan akaun"})
		return
	}
	c.JSON(http.StatusOK, toBannedMemberResponseFromProfile(row))
}

func (h *MemberBanHandler) Unban(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id ahli tidak sah"})
		return
	}
	row, err := h.queries.UnbanProfile(c.Request.Context(), targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusConflict, gin.H{"error": "akaun ini tidak sedang digantung"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal membuka penggantungan akaun"})
		return
	}
	if err := audit.Record(c.Request.Context(), h.queries, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"banned": true},
		New:        map[string]any{"banned": false},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal merekod pembukaan penggantungan"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user_id": row.UserID, "banned": false})
}

func toBannedMemberResponse(row sqlc.ListBannedProfilesRow) bannedMemberResponse {
	return bannedMemberResponse{
		UserID:       row.UserID.String(),
		MemberID:     textToPtr(row.MemberID),
		DisplayName:  textToPtr(row.DisplayName),
		Email:        row.Email,
		RoleKey:      row.RoleKey,
		BannedAt:     formatTime(row.BannedAt),
		BanExpiresAt: formatTimeNullable(row.BanExpiresAt),
		BanReason:    row.BanReason.String,
		BannedBy:     nullableUUIDString(row.BannedBy),
	}
}

func toBannedMemberResponseFromProfile(row sqlc.Profile) bannedMemberResponse {
	return bannedMemberResponse{
		UserID:       row.UserID.String(),
		MemberID:     textToPtr(row.MemberID),
		DisplayName:  textToPtr(row.DisplayName),
		BannedAt:     formatTime(row.BannedAt),
		BanExpiresAt: formatTimeNullable(row.BanExpiresAt),
		BanReason:    row.BanReason.String,
		BannedBy:     nullableUUIDString(row.BannedBy),
	}
}
