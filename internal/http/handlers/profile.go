package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
)

type ProfileHandler struct {
	queries     *sqlc.Queries
	emailClient *email.Client
}

func NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client) *ProfileHandler {
	return &ProfileHandler{queries: sqlc.New(pool), emailClient: emailClient}
}

type profileResponse struct {
	MemberID      string  `json:"member_id"`
	Email         string  `json:"email"`
	EmailVerified bool    `json:"email_verified"`
	Status        string  `json:"status"`
	DisplayName   *string `json:"display_name"`
	Phone         *string `json:"phone"`
	RoleKey       string  `json:"role_key"`
	RoleName      string  `json:"role_name"`
	Category      string  `json:"category"`
}

// Me setara `myProfileProvider` di Flutter — profil user semasa. Sengaja
// TIDAK di bawah RequireApprovedStatus (Stage 11): user pending/rejected
// kena boleh baca status dia sendiri supaya app boleh papar skrin yang
// betul.
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
		Status:        row.Status,
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
// (lepas trim) disimpan sebagai NULL. Sengaja TIDAK di bawah
// RequireApprovedStatus (Stage 11) — sama sebab macam Me.
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
	UserID      string  `json:"user_id"`
	MemberID    string  `json:"member_id"`
	DisplayName *string `json:"display_name"`
	RoleName    string  `json:"role_name"`
	Category    string  `json:"category"`
	Status      string  `json:"status"`
}

// Members setara `membersProvider` di Flutter — gantian RLS
// `select_all_profiles_management`: ahli biasa nampak diri sendiri
// sahaja, management nampak semua. Management boleh tapis
// `?status=pending` untuk senarai ahli menunggu kelulusan (Stage 11).
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
			UserID:      row.UserID.String(),
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
			Status:      row.Status,
		}})
		return
	}

	if statusFilter := c.Query("status"); statusFilter != "" {
		rows, err := h.queries.ListProfilesByStatus(ctx, statusFilter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
			return
		}
		members := make([]memberResponse, len(rows))
		for i, row := range rows {
			members[i] = memberResponse{
				UserID:      row.UserID.String(),
				MemberID:    row.MemberID,
				DisplayName: textToPtr(row.DisplayName),
				RoleName:    row.RoleName,
				Category:    row.RoleCategory,
				Status:      row.Status,
			}
		}
		c.JSON(http.StatusOK, members)
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
			UserID:      row.UserID.String(),
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
			Status:      row.Status,
		}
	}
	c.JSON(http.StatusOK, members)
}

type memberActionResponse struct {
	UserID     string  `json:"user_id"`
	Status     string  `json:"status"`
	ApprovedBy *string `json:"approved_by"`
	ApprovedAt *string `json:"approved_at"`
}

// ApproveMember (Stage 11) — management sahaja. Set status='approved',
// hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) ApproveMember(c *gin.Context) {
	h.setMemberStatus(c, "approved")
}

// RejectMember (Stage 11) — management sahaja. Set status='rejected'
// (row KEKAL, bukan padam — audit trail + boleh undo via ApproveMember
// lain kali). Hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) RejectMember(c *gin.Context) {
	h.setMemberStatus(c, "rejected")
}

func (h *ProfileHandler) setMemberStatus(c *gin.Context, status string) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isManagement, err := authz.IsManagement(ctx, h.queries, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh luluskan/tolak ahli"})
		return
	}

	approvedBy := pgtype.UUID{Bytes: callerID, Valid: true}

	var updated sqlc.Profile
	if status == "approved" {
		updated, err = h.queries.ApproveProfile(ctx, sqlc.ApproveProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	} else {
		updated, err = h.queries.RejectProfile(ctx, sqlc.RejectProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	notifType := "member_approved"
	subject, html := "Pendaftaran MARC Diluluskan",
		"<p>Pendaftaran anda telah diluluskan oleh pihak pengurusan. Log masuk semula dan sahkan email anda untuk mula guna app MARC.</p>"
	if status == "rejected" {
		notifType = "member_rejected"
		subject, html = "Pendaftaran MARC Ditolak",
			"<p>Pendaftaran anda ke app MARC tidak diluluskan pada masa ini. Jika ini satu kesilapan, sila hubungi pihak pengurusan MAIWP.</p>"
	}

	if target, err := h.queries.GetProfileByUserID(ctx, targetID); err == nil {
		if err := h.emailClient.Send(ctx, target.Email, subject, html); err != nil {
			log.Printf("gagal hantar email status ahli: %v", err)
		}
	}

	if _, err := h.queries.CreateNotification(ctx, sqlc.CreateNotificationParams{
		RecipientID: targetID,
		ActorID:     callerID,
		Type:        notifType,
		PostID:      pgtype.UUID{},
		CommentID:   pgtype.UUID{},
	}); err != nil {
		log.Printf("gagal cipta notification status ahli: %v", err)
	}

	c.JSON(http.StatusOK, memberActionResponse{
		UserID:     updated.UserID.String(),
		Status:     updated.Status,
		ApprovedBy: nullableUUIDString(updated.ApprovedBy),
		ApprovedAt: formatTimeNullable(updated.ApprovedAt),
	})
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
