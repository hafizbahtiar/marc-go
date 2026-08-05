package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type ProfileHandler struct {
	queries *sqlc.Queries
}

func NewProfileHandler(pool *pgxpool.Pool) *ProfileHandler {
	return &ProfileHandler{queries: sqlc.New(pool)}
}

type profileResponse struct {
	MemberID      string  `json:"member_id"`
	Email         string  `json:"email"`
	EmailVerified bool    `json:"email_verified"`
	DisplayName   *string `json:"display_name"`
	Phone         *string `json:"phone"`
	RoleKey       string  `json:"role_key"`
	RoleName      string  `json:"role_name"`
	Category      string  `json:"category"`
}

// Me setara `myProfileProvider` di Flutter — profil user semasa.
func (h *ProfileHandler) Me(c *gin.Context) {
	row, err := h.queries.GetProfileByUserID(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
		return
	}

	c.JSON(http.StatusOK, profileResponse{
		MemberID:      row.MemberID,
		Email:         row.Email,
		EmailVerified: row.EmailVerified,
		DisplayName:   textToPtr(row.DisplayName),
		Phone:         textToPtr(row.Phone),
		RoleKey:       row.RoleKey,
		RoleName:      row.RoleName,
		Category:      row.RoleCategory,
	})
}

type updateMeRequest struct {
	DisplayName string `json:"display_name"`
	Phone       string `json:"phone"`
}

// UpdateMe setara `ProfileRepository.update` di Flutter — string kosong
// (lepas trim) disimpan sebagai NULL.
func (h *ProfileHandler) UpdateMe(c *gin.Context) {
	var req updateMeRequest
	if !bindJSON(c, &req) {
		return
	}

	updated, err := h.queries.UpdateProfile(c.Request.Context(), sqlc.UpdateProfileParams{
		UserID:      middleware.UserID(c),
		DisplayName: ptrToText(req.DisplayName),
		Phone:       ptrToText(req.Phone),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"member_id":    updated.MemberID,
		"display_name": textToPtr(updated.DisplayName),
		"phone":        textToPtr(updated.Phone),
	})
}

type memberResponse struct {
	MemberID    string  `json:"member_id"`
	DisplayName *string `json:"display_name"`
	RoleName    string  `json:"role_name"`
	Category    string  `json:"category"`
}

// Members setara `membersProvider` di Flutter — gantian RLS
// `select_all_profiles_management`: ahli biasa nampak diri sendiri
// sahaja, management nampak semua.
func (h *ProfileHandler) Members(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	isManagement, err := authz.IsManagement(ctx, h.queries, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	if !isManagement {
		row, err := h.queries.GetProfileByUserID(ctx, userID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
			return
		}
		c.JSON(http.StatusOK, []memberResponse{{
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
		}})
		return
	}

	rows, err := h.queries.ListProfiles(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	members := make([]memberResponse, len(rows))
	for i, row := range rows {
		members[i] = memberResponse{
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
		}
	}
	c.JSON(http.StatusOK, members)
}

func textToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func ptrToText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
