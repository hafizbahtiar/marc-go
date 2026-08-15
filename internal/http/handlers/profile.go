package handlers

import (
	"context"
	"errors"
	"fmt"
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
	"marc/internal/phone"
	"marc/internal/storage"
)

type ProfileHandler struct {
	pool        *pgxpool.Pool
	queries     *sqlc.Queries
	emailClient *email.Client
	r2          *storage.R2Client
}

func NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client, r2 *storage.R2Client) *ProfileHandler {
	return &ProfileHandler{
		pool:        pool,
		queries:     sqlc.New(pool),
		emailClient: emailClient,
		r2:          r2,
	}
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
	AvatarURL     *string `json:"avatar_url"`
	// RegistrationPaymentStatus — "pending"/"succeeded"/"failed", atau
	// null kalau ahli tak pernah cuba bayar langsung. Ditambah 2026-08-15:
	// webhook ToyyibPay dah rekod bayaran gagal/berjaya BETUL dalam DB
	// sejak awal, tapi client tak pernah baca — ahli nampak "tiada apa
	// berlaku" walau hasil sebenar sentiasa betul di sisi pelayan. Cuma
	// diisi untuk ahli `pending` (approved tak perlu, dah lepas gate).
	RegistrationPaymentStatus *string `json:"registration_payment_status"`
}

// Me setara `myProfileProvider` di Flutter — profil user semasa. Sengaja
// TIDAK di bawah RequireApprovedStatus (Stage 11): user pending/rejected
// kena boleh baca status dia sendiri supaya app boleh papar skrin yang
// betul.
func (h *ProfileHandler) Me(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	row, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
		return
	}

	var paymentStatus *string
	if row.Status != "approved" {
		if status, err := h.queries.GetLatestRegistrationPaymentStatus(ctx, userID); err == nil {
			paymentStatus = &status
		} else if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("baca status bayaran pendaftaran (user=%s): %v", userID, err)
		}
	}

	c.JSON(http.StatusOK, profileResponse{
		MemberID:                  row.MemberID,
		Email:                     row.Email,
		EmailVerified:             row.EmailVerified,
		Status:                    row.Status,
		DisplayName:               textToPtr(row.DisplayName),
		Phone:                     textToPtr(row.Phone),
		RoleKey:                   row.RoleKey,
		RoleName:                  row.RoleName,
		Category:                  row.RoleCategory,
		RoleRank:                  row.RoleRank,
		AvatarURL:                 h.avatarURL(ctx, row.AvatarR2Key),
		RegistrationPaymentStatus: paymentStatus,
	})
}

type updateMeRequest struct {
	// DisplayName/Phone ialah *string (bukan string) supaya "tak dihantar"
	// dapat dibezakan daripada "buang nilai" — validator gin/go-playground
	// TIDAK menguatkuasakan `max` pada medan pointer (ia senyap dilangkau
	// untuk Kind() Ptr), jadi had panjang disemak secara manual dalam
	// UpdateMe selepas bindJSON, bukan melalui tag `binding`.
	DisplayName *string `json:"display_name"`
	Phone       *string `json:"phone"`

	// AvatarR2Key — kunci daripada /uploads/presign. Pointer supaya tiga
	// keadaan boleh dibezakan: tak dihantar (biar), string kosong (buang
	// avatar), atau kunci baharu (ganti).
	AvatarR2Key *string `json:"avatar_r2_key"`
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
	if req.DisplayName != nil && len(*req.DisplayName) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nama paparan terlalu panjang (maksimum 100 aksara)"})
		return
	}
	if req.Phone != nil && len(*req.Phone) > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nombor telefon terlalu panjang (maksimum 30 aksara)"})
		return
	}
	// Sahkan format Malaysia sama macam /auth/register (Opus verify
	// 2026-08-15 jumpa: laluan ni terima SEBARANG string sebelum ni,
	// membuka semula bug asal yang perubahan register cuba tutup — ahli
	// approved boleh PATCH phone jadi "abc", ToyyibPay createBill akan
	// tolak semula bila ahli tu cuba bayar). String KOSONG tetap
	// dibenarkan (buang nombor, padanan pola medan opsyenal lain di
	// handler ni) — cuma nilai BUKAN kosong perlu format sah.
	var normalizedPhone string
	if req.Phone != nil {
		trimmed := strings.TrimSpace(*req.Phone)
		if trimmed != "" {
			normalized, ok := phone.NormalizeMY(trimmed)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "format nombor telefon tidak sah"})
				return
			}
			normalizedPhone = normalized
		}
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	params := sqlc.UpdateProfileParams{UserID: userID}
	if req.DisplayName != nil {
		params.DisplayName = pgtype.Text{String: strings.TrimSpace(*req.DisplayName), Valid: true}
	}
	if req.Phone != nil {
		params.Phone = pgtype.Text{String: normalizedPhone, Valid: true}
	}

	updated, err := h.queries.UpdateProfile(ctx, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return
	}

	if req.AvatarR2Key != nil {
		updated, err = h.applyAvatar(c, userID, strings.TrimSpace(*req.AvatarR2Key))
		if err != nil {
			return // applyAvatar dah tulis respons ralat
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"member_id":    updated.MemberID,
		"display_name": textToPtr(updated.DisplayName),
		"phone":        textToPtr(updated.Phone),
		"avatar_url":   h.avatarURL(ctx, updated.AvatarR2Key),
	})
}

// applyAvatar tukar (atau buang) gambar profil.
//
// Semua dalam SATU transaksi: tetapkan kunci baharu, gilirkan yang lama
// untuk dipadam, dan tulis catatan audit. Kalau mana-mana gagal, tiada
// satu pun berlaku — kalau tidak avatar lama bocor dalam bucket atau
// perubahan berlaku tanpa jejak.
//
// `key` kosong = buang avatar.
func (h *ProfileHandler) applyAvatar(c *gin.Context, userID uuid.UUID, key string) (sqlc.Profile, error) {
	ctx := c.Request.Context()

	before, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return sqlc.Profile{}, err
	}

	if key != "" {
		// Kunci datang dari client, jadi ia MESTI disahkan milik caller —
		// tanpa ni sesiapa boleh menetapkan kunci orang lain (atau kunci
		// yang diteka) sebagai avatar mereka. Laluan sama macam gambar post.
		owned, err := h.queries.IsPendingUploadOwnedByUser(ctx, sqlc.IsPendingUploadOwnedByUserParams{
			R2Key: key, UserID: userID,
		})
		if err != nil || !owned {
			c.JSON(http.StatusBadRequest, gin.H{"error": "gambar tidak sah atau belum diupload"})
			return sqlc.Profile{}, errAvatarRejected
		}

		if err := h.r2.VerifyAvatar(ctx, key); err != nil {
			log.Printf("verify avatar gagal (r2_key=%s, user=%s): %v", key, userID, err)
			_ = h.r2.DeleteImage(ctx, key)
			_ = h.queries.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID})
			if errors.Is(err, storage.ErrImageTooManyPixels) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("dimensi gambar profil melebihi %dpx", storage.MaxAvatarDimension),
				})
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"error": "gambar tidak sah atau belum diupload"})
			}
			return sqlc.Profile{}, errAvatarRejected
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return sqlc.Profile{}, err
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	updated, err := q.UpdateProfileAvatar(ctx, sqlc.UpdateProfileAvatarParams{
		UserID:      userID,
		AvatarR2Key: ptrToText(key),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return sqlc.Profile{}, err
	}

	// Avatar LAMA mesti digilirkan, kalau tidak setiap kali tukar gambar
	// meninggalkan satu objek yatim dalam R2 selama-lamanya.
	if before.AvatarR2Key.Valid && before.AvatarR2Key.String != key {
		if err := q.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
			R2Key:  before.AvatarR2Key.String,
			Reason: "avatar_replaced",
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
			return sqlc.Profile{}, err
		}
	}

	if key != "" {
		// Kunci dah jadi milik profil sekarang — buang daripada pending
		// supaya penyapu "karangan ditinggalkan" tak memadamnya kemudian.
		if err := q.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
			return sqlc.Profile{}, err
		}
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   userID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"avatar_r2_key": textToAny(before.AvatarR2Key)},
		New:        map[string]any{"avatar_r2_key": textToAny(updated.AvatarR2Key)},
	}); err != nil {
		log.Printf("audit avatar: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return sqlc.Profile{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return sqlc.Profile{}, err
	}
	return updated, nil
}

var errAvatarRejected = errors.New("avatar ditolak")

// avatarURL bina URL awam, atau nil kalau ahli tiada avatar / R2 belum
// dikonfigur. Nil (bukan "") supaya client boleh bezakan "tiada gambar"
// daripada rentetan kosong yang mengelirukan.
func (h *ProfileHandler) avatarURL(ctx context.Context, key pgtype.Text) *string {
	if !key.Valid || key.String == "" {
		return nil
	}
	url := h.r2.SignedURL(ctx, key.String)
	if url == "" {
		return nil
	}
	return &url
}

func textToAny(t pgtype.Text) any {
	if !t.Valid {
		return nil
	}
	return t.String
}

type memberResponse struct {
	UserID      string  `json:"user_id"`
	MemberID    string  `json:"member_id"`
	DisplayName *string `json:"display_name"`

	// Nullable: emel ahli LAIN cuma didedahkan kepada management. Sejak
	// keterlihatan ahli diluaskan (ahli kini nampak ahli + supervisor),
	// menghantarnya kepada semua orang bermakna setiap ahli boleh menyalin
	// direktori emel penuh — pendedahan yang jauh lebih luas daripada niat
	// asal medan ni, semasa senarai ahli management-sahaja.
	//
	// `null` = disembunyikan (bukan "tiada emel"), jadi client boleh
	// bezakan dua keadaan itu.
	Email    *string `json:"email"`
	RoleKey  string  `json:"role_key"`
	RoleName string  `json:"role_name"`
	RoleRank int32   `json:"role_rank"`
	Category string  `json:"category"`
	Status   string  `json:"status"`

	// null = tiada gambar profil (atau R2 belum dikonfigur). Client jatuh
	// balik kepada huruf pertama nama.
	AvatarURL *string `json:"avatar_url"`
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
		// Ahli biasa nampak emel SENDIRI sahaja. Ditapis di sini dan bukan
		// dalam SQL sebab baris caller sendiri tetap perlukan emel itu.
		email := row.Email
		if !isManagement && row.UserID != userID {
			email = ""
		}
		members[i] = h.toMemberResponse(ctx, memberRow{
			UserID: row.UserID, MemberID: row.MemberID, DisplayName: row.DisplayName,
			Email: email, RoleKey: row.RoleKey, RoleName: row.RoleName,
			RoleRank: row.RoleRank, Category: row.RoleCategory, Status: row.Status,
			AvatarKey: row.AvatarR2Key,
		})
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

// toMemberResponse — `email` kosong bermakna sembunyikan medan itu.
// memberRow — input untuk toMemberResponse. Struct, bukan senarai
// parameter: versi lama ada sembilan argumen positional bertype string
// yang sama, jadi tertukar susunan (cth roleKey lawan roleName) akan
// compile dengan senyap.
type memberRow struct {
	UserID      uuid.UUID
	MemberID    string
	DisplayName pgtype.Text
	Email       string // kosong = sembunyikan medan
	RoleKey     string
	RoleName    string
	RoleRank    int32
	Category    string
	Status      string
	AvatarKey   pgtype.Text
}

func (h *ProfileHandler) toMemberResponse(ctx context.Context, m memberRow) memberResponse {
	var emailPtr *string
	if m.Email != "" {
		emailPtr = &m.Email
	}
	return memberResponse{
		UserID:      m.UserID.String(),
		MemberID:    m.MemberID,
		DisplayName: textToPtr(m.DisplayName),
		Email:       emailPtr,
		RoleKey:     m.RoleKey,
		RoleName:    m.RoleName,
		RoleRank:    m.RoleRank,
		Category:    m.Category,
		Status:      m.Status,
		AvatarURL:   h.avatarURL(ctx, m.AvatarKey),
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

	c.JSON(http.StatusOK, h.toMemberResponse(ctx, memberRow{
		UserID: updated.UserID, MemberID: updated.MemberID, DisplayName: updated.DisplayName,
		Email: target.Email, RoleKey: newRole.Key, RoleName: newRole.Name,
		RoleRank: newRole.Rank, Category: newRole.Category, Status: updated.Status,
		AvatarKey: updated.AvatarR2Key,
	}))
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

	// Satu fetch di sini beri semua yang diperlukan kemudian: status LAMA
	// (untuk jejak audit), kategori role (semakan di bawah), dan emel
	// (notifikasi). Dulu tiga query berasingan pada baris yang sama.
	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	// Elak reject sesama management (fat-finger atau serangan lateral
	// boleh reject SEMUA management, termasuk yang terakhir — sistem
	// approval jadi buntu tanpa cara in-app untuk pulih).
	if status == "rejected" && target.RoleCategory == authz.CategoryManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh tolak ahli pengurusan"})
		return
	}

	// Status dah sama — no-op idempotent. Pulang keadaan semasa TANPA
	// menulis catatan audit atau menghantar semula emel/notifikasi:
	// tiada apa yang berubah, jadi tiada apa untuk direkodkan.
	if target.Status == status {
		c.JSON(http.StatusOK, memberActionResponse{
			UserID:     target.UserID.String(),
			Status:     target.Status,
			ApprovedBy: nullableUUIDString(target.ApprovedBy),
			ApprovedAt: formatTimeNullable(target.ApprovedAt),
		})
		return
	}

	// Gate: pending -> approved MESTI ada bayaran yuran pendaftaran
	// 'succeeded' (Stage 12, ToyyibPay — lihat TODO.md bahagian Payment).
	// Ahli sedia ada yang dah approved sebelum ciri ni wujud tak pernah
	// sampai sini (no-op di atas dah return awal), jadi grandfathered
	// SECARA AUTOMATIK tanpa perlu semakan "bila akaun dicipta" — hanya
	// peralihan SEBENAR pending->approved kena gate. RejectMember tak
	// disentuh — penolakan mesti berfungsi tanpa kira status bayaran.
	// Diletak SEBELUM tx.Begin sengaja: kalau tak lulus, tiada transaksi
	// untuk dibuka langsung.
	if status == "approved" {
		paid, err := h.queries.HasSucceededRegistrationPayment(ctx, targetID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
			return
		}
		if !paid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ahli belum bayar yuran pendaftaran"})
			return
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	approvedBy := pgtype.UUID{Bytes: callerID, Valid: true}

	var updated sqlc.Profile
	if status == "approved" {
		updated, err = q.ApproveProfile(ctx, sqlc.ApproveProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	} else {
		updated, err = q.RejectProfile(ctx, sqlc.RejectProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Guard replay dalam query — dua permintaan serentak, yang kalah
			// sampai sini. Layan sama macam no-op di atas.
			c.JSON(http.StatusOK, memberActionResponse{
				UserID:     target.UserID.String(),
				Status:     status,
				ApprovedBy: nullableUUIDString(target.ApprovedBy),
				ApprovedAt: formatTimeNullable(target.ApprovedAt),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}

	// Kelulusan keahlian ialah keputusan pentadbiran — siapa yang benarkan
	// (atau halang) seseorang masuk mesti dapat dijawab kemudian.
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"status": target.Status},
		New:        map[string]any{"status": updated.Status},
	}); err != nil {
		log.Printf("audit status ahli: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}

	notifType := "member_approved"
	subject, html := "Pendaftaran MARC Diluluskan",
		"<p>Pendaftaran anda telah diluluskan oleh pihak pengurusan. Log masuk semula dan sahkan email anda untuk mula guna app MARC.</p>"
	if status == "rejected" {
		notifType = "member_rejected"
		subject, html = "Pendaftaran MARC Ditolak",
			"<p>Pendaftaran anda ke app MARC tidak diluluskan pada masa ini. Jika ini satu kesilapan, sila hubungi pihak pengurusan MARC.</p>"

		// Dipindahkan ke DALAM transaksi: sesi yang masih hidup untuk ahli
		// yang baru ditolak ialah jurang keselamatan, jadi penolakan dan
		// pembatalan token mesti jadi atau gagal BERSAMA. Sebelum ni ia
		// best-effort di luar — kegagalan cuma dilog, dan ahli yang ditolak
		// kekal membawa refresh token yang sah.
		if err := q.DeleteRefreshTokensByUser(ctx, targetID); err != nil {
			log.Printf("gagal revoke refresh token ahli ditolak: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}

	// Selepas commit — best effort. Emel/notifikasi yang gagal tak patut
	// membatalkan keputusan kelulusan yang dah dibuat.
	if err := h.emailClient.Send(ctx, target.Email, subject, html); err != nil {
		log.Printf("gagal hantar email status ahli: %v", err)
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
