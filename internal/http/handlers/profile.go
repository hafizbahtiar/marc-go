package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
)

type ProfileHandler struct {
	pool        *pgxpool.Pool
	queries     *sqlc.Queries
	emailClient *email.Client
}

func NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client) *ProfileHandler {
	return &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: emailClient}
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
	RoleRank      int32   `json:"role_rank"`
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
		RoleRank:      row.RoleRank,
	})
}

type updateMeRequest struct {
	DisplayName *string `json:"display_name"`
	Phone       *string `json:"phone"`
}

// UpdateMe setara `ProfileRepository.update` di Flutter — field yang
// tak dihantar (nil) DIBIARKAN tak berubah; field yang dihantar
// (termasuk string kosong) ditetapkan terus kepada nilai tu. Sengaja
// TIDAK di bawah RequireApprovedStatus — sama sebab macam Me.
func (h *ProfileHandler) UpdateMe(c *gin.Context) {
	var req updateMeRequest
	if !bindJSON(c, &req) {
		return
	}

	params := sqlc.UpdateProfileParams{UserID: middleware.UserID(c)}
	if req.DisplayName != nil {
		params.DisplayName = pgtype.Text{String: strings.TrimSpace(*req.DisplayName), Valid: true}
	}
	if req.Phone != nil {
		params.Phone = pgtype.Text{String: strings.TrimSpace(*req.Phone), Valid: true}
	}

	updated, err := h.queries.UpdateProfile(c.Request.Context(), params)
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
	Email       string  `json:"email"`
	RoleKey     string  `json:"role_key"`
	RoleName    string  `json:"role_name"`
	RoleRank    int32   `json:"role_rank"`
	Category    string  `json:"category"`
	Status      string  `json:"status"`
}

// Members setara `membersProvider` di Flutter — gantian RLS
// `select_all_profiles_management`. Keterlihatan ikut hierarki
// `roles.rank` (lihat visibleRankCeiling), BUKAN lagi "ahli nampak diri
// sendiri sahaja". Dua kawalan tambahan:
//
//   - Ahli biasa cuma nampak ahli berstatus 'approved' (+ baris dia
//     sendiri) — direktori ahli, bukan barisan kelulusan.
//   - `?status=pending` (barisan kelulusan Stage 11) management sahaja.
//
// Semua tapisan dikuatkuasakan dalam SQL — lihat ListVisibleProfiles.
func (h *ProfileHandler) Members(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	caller, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
		return
	}
	isManagement := caller.RoleCategory == authz.CategoryManagement

	statusFilter := c.Query("status")
	if statusFilter != "" && !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh tapis ahli ikut status"})
		return
	}

	roles, err := h.queries.ListRoles(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	rows, err := h.queries.ListVisibleProfiles(ctx, sqlc.ListVisibleProfilesParams{
		MaxRank:            visibleRankCeiling(roles, caller.RoleRank),
		Status:             ptrToText(statusFilter),
		IncludeAllStatuses: isManagement,
		ViewerID:           userID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	members := make([]memberResponse, len(rows))
	for i, row := range rows {
		members[i] = toMemberResponse(row.UserID, row.MemberID, row.DisplayName, row.Email, row.RoleKey, row.RoleName, row.RoleRank, row.RoleCategory, row.Status)
	}
	c.JSON(http.StatusOK, members)
}

// visibleRankCeiling — rank TERTINGGI yang seorang viewer boleh nampak
// dalam senarai ahli. Peraturan: nampak semua orang sehingga SATU
// tingkat di atas rank sendiri, kecuali rank tertinggi (superadmin) yang
// tak pernah didedahkan kepada sesiapa selain superadmin sendiri.
//
// Dengan seed semasa (ahli 10, supervisor 50, manager 60, superadmin 100):
//
//	ahli       -> 50  (ahli + supervisor)
//	supervisor -> 60  (ahli + supervisor + manager)
//	manager    -> 60  (manager ke bawah; superadmin tersembunyi)
//	superadmin -> 100 (semua)
//
// Dikira daripada jadual `roles` dan bukan rank hardcoded supaya role
// baharu yang disisip di tengah-tengah hierarki terus ikut peraturan ni.
func visibleRankCeiling(roles []sqlc.Role, viewerRank int32) int32 {
	var topRank int32
	for _, r := range roles {
		if r.Rank > topRank {
			topRank = r.Rank
		}
	}
	if viewerRank >= topRank {
		return topRank
	}

	ceiling := viewerRank
	for _, r := range roles {
		// Rank tertinggi (topRank) sengaja dilangkau — itulah superadmin.
		if r.Rank > viewerRank && r.Rank < topRank && (ceiling == viewerRank || r.Rank < ceiling) {
			ceiling = r.Rank
		}
	}
	return ceiling
}

func toMemberResponse(userID uuid.UUID, memberID string, displayName pgtype.Text, email, roleKey, roleName string, roleRank int32, category, status string) memberResponse {
	return memberResponse{
		UserID:      userID.String(),
		MemberID:    memberID,
		DisplayName: textToPtr(displayName),
		Email:       email,
		RoleKey:     roleKey,
		RoleName:    roleName,
		RoleRank:    roleRank,
		Category:    category,
		Status:      status,
	}
}

// ListRoles (Stage 12) — management sahaja. Senarai role untuk UI edit
// role (bottom sheet). Ditapis kepada role yang caller memang BOLEH
// assign (rank lebih rendah drpd rank dia — syarat sama yang
// dikuatkuasakan UpdateMemberRole), jadi 'superadmin' tak pernah muncul
// kecuali kepada superadmin. Dulu senarai penuh dihantar dan client yang
// kena tapis.
func (h *ProfileHandler) ListRoles(c *gin.Context) {
	ctx := c.Request.Context()
	caller, err := h.queries.GetProfileByUserID(ctx, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai role"})
		return
	}
	if caller.RoleCategory != authz.CategoryManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh lihat senarai role"})
		return
	}

	roles, err := h.queries.ListRoles(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai role"})
		return
	}

	type roleResponse struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Rank int32  `json:"rank"`
	}
	res := make([]roleResponse, 0, len(roles))
	for _, r := range roles {
		if r.Rank >= caller.RoleRank {
			continue
		}
		res = append(res, roleResponse{Key: r.Key, Name: r.Name, Rank: r.Rank})
	}
	c.JSON(http.StatusOK, res)
}

type updateMemberRoleRequest struct {
	RoleKey string `json:"role_key" binding:"required"`
}

// UpdateMemberRole (Stage 12) — management sahaja, dikawal hierarki
// `roles.rank`: editor cuma boleh edit target dengan rank LEBIH RENDAH
// drpd dia, dan cuma boleh assign role dengan rank LEBIH RENDAH drpd
// rank dia sendiri (elak self-service naik setaraf/lebih tinggi drpd
// orang yang edit). Superadmin (rank tertinggi) secara praktikal boleh
// edit semua sebab semua role lain rank lebih rendah.
func (h *ProfileHandler) UpdateMemberRole(c *gin.Context) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	var req updateMemberRoleRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh tukar role akaun sendiri"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini role ahli"})
		return
	}
	if caller.RoleCategory != authz.CategoryManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh tukar role ahli"})
		return
	}

	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	if caller.RoleRank <= target.RoleRank {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh edit ahli setaraf/lebih tinggi drpd anda"})
		return
	}

	newRole, err := h.queries.GetRoleByKey(ctx, req.RoleKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role tidak sah"})
		return
	}
	if newRole.Rank >= caller.RoleRank {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh assign role setaraf/lebih tinggi drpd anda"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini role ahli"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	updated, err := q.UpdateProfileRole(ctx, sqlc.UpdateProfileRoleParams{
		UserID: targetID,
		RoleID: newRole.ID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini role ahli"})
		return
	}

	// Perubahan keistimewaan — catatan audit paling bernilai dalam sistem
	// ni. Rank direkod sekali, bukan cuma kunci role, supaya "siapa naikkan
	// siapa" boleh dibaca tanpa merujuk jadual roles versi masa itu.
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"role_key": target.RoleKey, "role_rank": target.RoleRank},
		New:        map[string]any{"role_key": newRole.Key, "role_rank": newRole.Rank},
	}); err != nil {
		log.Printf("audit tukar role: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini role ahli"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini role ahli"})
		return
	}

	c.JSON(http.StatusOK, toMemberResponse(updated.UserID, updated.MemberID, updated.DisplayName, target.Email, newRole.Key, newRole.Name, newRole.Rank, newRole.Category, updated.Status))
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

	// Elak self-lockout: tak boleh approve/reject akaun sendiri.
	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh laksanakan tindakan ini pada akaun sendiri"})
		return
	}

	// Elak reject sesama management (fat-finger atau serangan lateral
	// boleh reject SEMUA management, termasuk yang terakhir — sistem
	// approval jadi buntu tanpa cara in-app untuk pulih).
	if status == "rejected" {
		targetCategory, err := h.queries.GetRoleCategoryByUserID(ctx, targetID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
			return
		}
		if targetCategory == authz.CategoryManagement {
			c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh tolak ahli pengurusan"})
			return
		}
	}

	approvedBy := pgtype.UUID{Bytes: callerID, Valid: true}

	var updated sqlc.Profile
	if status == "approved" {
		updated, err = h.queries.ApproveProfile(ctx, sqlc.ApproveProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	} else {
		updated, err = h.queries.RejectProfile(ctx, sqlc.RejectProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Sama ada target tak wujud, ATAU target dah pun dalam status
			// ni (guard replay di query) — dua-dua kes, bezakan dengan
			// cuba fetch profil semasa: kalau wujud, ni idempotent no-op
			// (jangan re-hantar email/notification); kalau tak wujud
			// langsung, 404 sebenar.
			current, ferr := h.queries.GetProfileByUserID(ctx, targetID)
			if ferr != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
				return
			}
			c.JSON(http.StatusOK, memberActionResponse{
				UserID:     current.UserID.String(),
				Status:     current.Status,
				ApprovedBy: nullableUUIDString(current.ApprovedBy),
				ApprovedAt: formatTimeNullable(current.ApprovedAt),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}

	notifType := "member_approved"
	subject, html := "Pendaftaran MARC Diluluskan",
		"<p>Pendaftaran anda telah diluluskan oleh pihak pengurusan. Log masuk semula dan sahkan email anda untuk mula guna app MARC.</p>"
	if status == "rejected" {
		notifType = "member_rejected"
		subject, html = "Pendaftaran MARC Ditolak",
			"<p>Pendaftaran anda ke app MARC tidak diluluskan pada masa ini. Jika ini satu kesilapan, sila hubungi pihak pengurusan MAIWP.</p>"
		if rerr := h.queries.DeleteRefreshTokensByUser(ctx, targetID); rerr != nil {
			log.Printf("gagal revoke refresh token ahli ditolak: %v", rerr)
		}
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
