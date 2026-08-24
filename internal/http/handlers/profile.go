package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"marc/internal/payment"
	"marc/internal/paymentlog"
	"marc/internal/phone"
	"marc/internal/storage"
)

type ProfileHandler struct {
	pool           *pgxpool.Pool
	queries        *sqlc.Queries
	emailClient    *email.Client
	r2             *storage.R2Client
	feeCents       int64
	regPaymentGW   payment.Gateway
}

func NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client, r2 *storage.R2Client, registrationFeeCents int, regPaymentGW payment.Gateway) *ProfileHandler {
	return &ProfileHandler{
		pool:         pool,
		queries:      sqlc.New(pool),
		emailClient:  emailClient,
		r2:           r2,
		feeCents:     int64(registrationFeeCents),
		regPaymentGW: regPaymentGW,
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
	// RegistrationFeeCents — jumlah (sen) yuran pendaftaran SEMASA
	// (`REGISTRATION_FEE_CENTS`), supaya client boleh papar jumlah SEBELUM
	// ahli tekan bayar (checkout ToyyibPay tak dedah jumlah dalam app —
	// cuma redirect ke halaman ToyyibPay). Cuma diisi untuk ahli belum
	// `approved`, padan skop `RegistrationPaymentStatus` di atas — ahli
	// approved dah lepas gate, tak perlu tahu angka ni lagi.
	RegistrationFeeCents *int64  `json:"registration_fee_cents"`
	TelegramLinked       bool    `json:"telegram_linked"`
	TelegramUsername     *string `json:"telegram_username"`

	// EmergencyContactName/Phone/HealthNotes — self-service (PATCH /me).
	EmergencyContactName  *string `json:"emergency_contact_name"`
	EmergencyContactPhone *string `json:"emergency_contact_phone"`
	HealthNotes           *string `json:"health_notes"`
	// IsActive — flag keahlian (BUKAN status kelulusan). Baca sahaja di
	// sini — cuma management boleh tukar, via PATCH /members/:id/active.
	IsActive bool `json:"is_active"`

	// DepartmentCode/DepartmentName/Position — baca sahaja di sini, cuma
	// manager ke atas boleh tukar (via PATCH /members/:id/department),
	// BUKAN self-service (beza drpd EmergencyContact*/HealthNotes).
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`
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
	var feeCents *int64
	if row.Status != "approved" {
		if status, err := h.queries.GetLatestRegistrationPaymentStatus(ctx, userID); err == nil {
			paymentStatus = &status
		} else if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("baca status bayaran pendaftaran (user=%s): %v", userID, err)
		}
		feeCents = &h.feeCents
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
		RegistrationFeeCents:      feeCents,
		TelegramLinked:            row.TelegramChatID.Valid,
		TelegramUsername:          textToPtr(row.TelegramUsername),
		EmergencyContactName:      textToPtr(row.EmergencyContactName),
		EmergencyContactPhone:     textToPtr(row.EmergencyContactPhone),
		HealthNotes:               textToPtr(row.HealthNotes),
		IsActive:                  row.IsActive,
		DepartmentCode:            textToPtr(row.DepartmentCode),
		DepartmentName:            textToPtr(row.DepartmentName),
		Position:                  textToPtr(row.Position),
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

	// EmergencyContactName/Phone/HealthNotes — sama pola DisplayName/Phone:
	// nil = tak dihantar (biar), string kosong dibenarkan (buang nilai).
	EmergencyContactName  *string `json:"emergency_contact_name"`
	EmergencyContactPhone *string `json:"emergency_contact_phone"`
	HealthNotes           *string `json:"health_notes"`
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
	if req.EmergencyContactName != nil && len(*req.EmergencyContactName) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nama waris terlalu panjang (maksimum 100 aksara)"})
		return
	}
	if req.EmergencyContactPhone != nil && len(*req.EmergencyContactPhone) > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nombor telefon waris terlalu panjang (maksimum 30 aksara)"})
		return
	}
	if req.HealthNotes != nil && len(*req.HealthNotes) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nota kesihatan terlalu panjang (maksimum 500 aksara)"})
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
	// Sama pola `Phone` — waris pun nombor Malaysia, sahkan format sama
	// (string kosong tetap dibenarkan, buang nombor).
	var normalizedEmergencyPhone string
	if req.EmergencyContactPhone != nil {
		trimmed := strings.TrimSpace(*req.EmergencyContactPhone)
		if trimmed != "" {
			normalized, ok := phone.NormalizeMY(trimmed)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "format nombor telefon waris tidak sah"})
				return
			}
			normalizedEmergencyPhone = normalized
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
	if req.EmergencyContactName != nil {
		params.EmergencyContactName = pgtype.Text{String: strings.TrimSpace(*req.EmergencyContactName), Valid: true}
	}
	if req.EmergencyContactPhone != nil {
		params.EmergencyContactPhone = pgtype.Text{String: normalizedEmergencyPhone, Valid: true}
	}
	if req.HealthNotes != nil {
		params.HealthNotes = pgtype.Text{String: strings.TrimSpace(*req.HealthNotes), Valid: true}
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
		Actor:      auditActor(c, q),
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

	// RegistrationPaymentStatus — "pending"/"succeeded"/"failed", atau
	// null. Ditambah 2026-08-15 supaya management nampak siapa dah bayar
	// SEBELUM tekan Luluskan (gate `ApproveMember` sedia ada sejak awal,
	// cuma tak kelihatan di senarai sebelum ni). Sama pola privasi
	// dengan Email — cuma management yang dapat nilai sebenar, ahli
	// biasa dapat null (bukan medan yang perlu didedahkan untuk lihat
	// ahli lain).
	RegistrationPaymentStatus *string `json:"registration_payment_status"`

	// IsActive — flag keahlian (bukan status kelulusan). Dedah kepada
	// semua viewer (bukan cuma management, padanan `Status`) supaya
	// senarai ahli papar status aktif konsisten dgn cara `Status` sedia
	// ada dipapar.
	IsActive bool `json:"is_active"`

	// DepartmentCode/DepartmentName/Position — dedah kepada semua viewer
	// (padanan IsActive) - info organisasi, bukan data sensitif macam
	// Email/RegistrationPaymentStatus.
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`
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
		// Padanan pola Email — status bayaran cuma berguna untuk
		// management, ahli biasa dapat null (lihat komen memberResponse).
		paymentStatus := row.RegistrationPaymentStatus
		if !isManagement {
			paymentStatus = ""
		}
		members[i] = h.toMemberResponse(ctx, memberRow{
			UserID: row.UserID, MemberID: row.MemberID, DisplayName: row.DisplayName,
			Email: email, RoleKey: row.RoleKey, RoleName: row.RoleName,
			RoleRank: row.RoleRank, Category: row.RoleCategory, Status: row.Status,
			AvatarKey: row.AvatarR2Key, RegistrationPaymentStatus: paymentStatus,
			IsActive: row.IsActive, DepartmentCode: row.DepartmentCode,
			DepartmentName: row.DepartmentName, Position: row.Position,
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
	UserID                    uuid.UUID
	MemberID                  string
	DisplayName               pgtype.Text
	Email                     string // kosong = sembunyikan medan
	RoleKey                   string
	RoleName                  string
	RoleRank                  int32
	Category                  string
	Status                    string
	AvatarKey                 pgtype.Text
	RegistrationPaymentStatus string // kosong = sembunyikan medan (padanan Email)
	IsActive                  bool
	DepartmentCode            pgtype.Text
	DepartmentName            pgtype.Text
	Position                  pgtype.Text
}

func (h *ProfileHandler) toMemberResponse(ctx context.Context, m memberRow) memberResponse {
	var emailPtr *string
	if m.Email != "" {
		emailPtr = &m.Email
	}
	var paymentStatusPtr *string
	if m.RegistrationPaymentStatus != "" {
		paymentStatusPtr = &m.RegistrationPaymentStatus
	}
	return memberResponse{
		UserID:                    m.UserID.String(),
		MemberID:                  m.MemberID,
		DisplayName:               textToPtr(m.DisplayName),
		Email:                     emailPtr,
		RoleKey:                   m.RoleKey,
		RoleName:                  m.RoleName,
		RoleRank:                  m.RoleRank,
		Category:                  m.Category,
		Status:                    m.Status,
		AvatarURL:                 h.avatarURL(ctx, m.AvatarKey),
		RegistrationPaymentStatus: paymentStatusPtr,
		IsActive:                  m.IsActive,
		DepartmentCode:            textToPtr(m.DepartmentCode),
		DepartmentName:            textToPtr(m.DepartmentName),
		Position:                  textToPtr(m.Position),
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
		Actor:      auditActor(c, q),
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
		AvatarKey: updated.AvatarR2Key, IsActive: updated.IsActive,
	}))
}

type updateMemberActiveRequest struct {
	IsActive bool `json:"is_active"`
}

type memberActiveResponse struct {
	UserID   string `json:"user_id"`
	IsActive bool   `json:"is_active"`
}

// UpdateMemberActive — PATCH /members/:id/active. Tukar flag KEAHLIAN
// (`is_active`), BERASINGAN drpd `status` (kelulusan) — ahli `approved`
// boleh jadi tak aktif kemudian (cth berhenti) tanpa perlu tolak
// pendaftaran asal. Gate sama hierarki rank macam UpdateMemberRole.
func (h *ProfileHandler) UpdateMemberActive(c *gin.Context) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	var req updateMemberActiveRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh tukar status aktif akaun sendiri"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status aktif ahli"})
		return
	}
	if caller.RoleCategory != authz.CategoryManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh tukar status aktif ahli"})
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

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status aktif ahli"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	updated, err := q.UpdateProfileActive(ctx, sqlc.UpdateProfileActiveParams{
		UserID:   targetID,
		IsActive: req.IsActive,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status aktif ahli"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old:        map[string]any{"is_active": target.IsActive},
		New:        map[string]any{"is_active": updated.IsActive},
	}); err != nil {
		log.Printf("audit tukar status aktif: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status aktif ahli"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status aktif ahli"})
		return
	}

	c.JSON(http.StatusOK, memberActiveResponse{
		UserID:   updated.UserID.String(),
		IsActive: updated.IsActive,
	})
}

type updateMemberDepartmentRequest struct {
	// DepartmentCode/Position — GANTI PENUH (bukan partial macam UpdateMe):
	// nil ATAU string kosong = kosongkan, kod bukan-kosong = tetapkan.
	// Borang "tetapkan bahagian+jawatan skrg" satu tindakan, bukan patch
	// berperingkat.
	DepartmentCode *string `json:"department_code"`
	Position       *string `json:"position"`
}

type memberDepartmentResponse struct {
	UserID         string  `json:"user_id"`
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`
}

// UpdateMemberDepartment — PATCH /members/:id/department. Manager KE ATAS
// sahaja (superadmin/admin/manager — bukan supervisor, keputusan produk
// 2026-08-25), gate rank "SETARAF DAN KE BAWAH sahaja"
// (caller.RoleRank >= target.RoleRank) — BEZA drpd UpdateMemberRole/
// UpdateMemberActive yang caller.RoleRank kena STRICTLY lebih tinggi
// (>). Bahagian/jawatan bukan keistimewaan sistem (role/status aktif),
// jadi manager boleh tetapkan utk manager lain yang setaraf — termasuk
// diri sendiri (tiada sekatan targetID == callerID).
func (h *ProfileHandler) UpdateMemberDepartment(c *gin.Context) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	var req updateMemberDepartmentRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Position != nil && len(*req.Position) > 150 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jawatan terlalu panjang (maksimum 150 aksara)"})
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isManagerUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "manager")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}
	if !isManagerUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma manager ke atas boleh tukar bahagian/jawatan ahli"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}
	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	if caller.RoleRank < target.RoleRank {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh edit ahli lebih tinggi drpd anda"})
		return
	}

	deptCode := pgtype.Text{}
	if req.DepartmentCode != nil {
		code := strings.TrimSpace(*req.DepartmentCode)
		if code != "" {
			exists, err := h.queries.DepartmentExists(ctx, code)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
				return
			}
			if !exists {
				c.JSON(http.StatusBadRequest, gin.H{"error": "bahagian tidak sah"})
				return
			}
			deptCode = pgtype.Text{String: code, Valid: true}
		}
	}
	position := pgtype.Text{}
	if req.Position != nil {
		trimmed := strings.TrimSpace(*req.Position)
		if trimmed != "" {
			position = pgtype.Text{String: trimmed, Valid: true}
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	if _, err := q.UpdateProfileDepartment(ctx, sqlc.UpdateProfileDepartmentParams{
		UserID:         targetID,
		DepartmentCode: deptCode,
		Position:       position,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}

	// Baca semula MELALUI tx (bukan h.queries) — perlukan department_name
	// terjoin, dan mesti nampak baris yang baru dikemas kini dalam
	// transaksi yang sama (isolation default belum commit lagi).
	updated, err := q.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old: map[string]any{
			"department_code": textToPtr(target.DepartmentCode),
			"position":        textToPtr(target.Position),
		},
		New: map[string]any{
			"department_code": textToPtr(updated.DepartmentCode),
			"position":        textToPtr(updated.Position),
		},
	}); err != nil {
		log.Printf("audit tukar bahagian/jawatan: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian/jawatan ahli"})
		return
	}

	c.JSON(http.StatusOK, memberDepartmentResponse{
		UserID:         updated.UserID.String(),
		DepartmentCode: textToPtr(updated.DepartmentCode),
		DepartmentName: textToPtr(updated.DepartmentName),
		Position:       textToPtr(updated.Position),
	})
}

type memberActionResponse struct {
	UserID     string  `json:"user_id"`
	Status     string  `json:"status"`
	ApprovedBy *string `json:"approved_by"`
	ApprovedAt *string `json:"approved_at"`
}

type approveMemberRequest struct {
	// BypassPayment — admin/superadmin sahaja (rank >= "admin"). Langkau
	// gate `HasSucceededRegistrationPayment` untuk ahli lama yang dah
	// bayar secara manual sebelum sistem digital wujud. Nota WAJIB bila
	// ni true — jejak audit kelab lama->digital kena jelas siapa langkau
	// bayaran, untuk siapa, dan kenapa.
	BypassPayment bool   `json:"bypass_payment"`
	BypassReason  string `json:"bypass_reason" binding:"max=500"`
}

// ApproveMember (Stage 11) — management sahaja. Set status='approved',
// hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) ApproveMember(c *gin.Context) {
	// Body ini pilihan sepenuhnya (ahli biasa diluluskan tanpa body
	// langsung sebelum ni) — kosong terus laluan sedia ada (gate bayaran
	// biasa) tidak berubah. Semak `err` terus terhadap io.EOF (bukan
	// `ContentLength > 0`, Opus verify: ContentLength == -1 untuk
	// Transfer-Encoding: chunked/unknown, jadi guard ContentLength tu
	// terlepas body cacat yang dihantar TANPA Content-Length eksplisit)
	// — body cacat (cth `bypass_payment` jenis string bukan bool) pulang
	// 400 jelas, bukan senyap gagal-jadi-false lalu mengelirukan admin
	// dengan mesej "ahli belum bayar" walhal dia memang cuba bypass.
	var req approveMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": friendlyBindError(err)})
		return
	}
	h.setMemberStatus(c, "approved", req)
}

// RejectMember (Stage 11) — management sahaja. Set status='rejected'
// (row KEKAL, bukan padam — audit trail + boleh undo via ApproveMember
// lain kali). Hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) RejectMember(c *gin.Context) {
	h.setMemberStatus(c, "rejected", approveMemberRequest{})
}

func (h *ProfileHandler) setMemberStatus(c *gin.Context, status string, req approveMemberRequest) {
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
		// Semak bayaran SEBENAR dulu, tak kira flag bypass — kalau ahli
		// dah bayar (online, atau padanan lain), langkau tak relevan
		// langsung. Ni sengaja ditulis SEBELUM cawangan bypass (Opus
		// verify: admin yang tersilap hantar bypass_payment=true untuk
		// ahli yang DAH bayar tak patut buat audit rekod
		// "payment_bypassed" palsu bagi yuran yang sebenarnya dikutip).
		paid, err := h.queries.HasSucceededRegistrationPayment(ctx, targetID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
			return
		}
		if paid {
			req.BypassPayment = false
		} else if req.BypassPayment {
			// Langkau bayaran — hanya admin/superadmin (rank >= "admin"),
			// BUKAN supervisor/manager. IsAtLeastRole (bukan IsManagement)
			// sengaja dipakai di sini supaya tier di bawah admin tak boleh
			// langkau gate kewangan ni.
			isAdminUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "admin")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
				return
			}
			if !isAdminUp {
				c.JSON(http.StatusForbidden, gin.H{"error": "cuma admin/superadmin boleh langkau bayaran yuran"})
				return
			}
			if strings.TrimSpace(req.BypassReason) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "nota diperlukan untuk langkau bayaran yuran"})
				return
			}

			// Baris pembayaran 'pending' yang masih boleh diselesaikan
			// bila-bila masa (Opus verify: MEDIUM, dan verify susulan —
			// TANPA tapisan gateway_ref, lihat komen query) — kalau baris
			// begini wujud, ahli boleh bayar lepas diluluskan dan webhook
			// tandakan 'succeeded', jadi terima 2 pengesahan bayaran
			// (tunai + bil online lama) tanpa refund path. Blok sehingga
			// baris lama diselesaikan (webhook/pautan manual) atau tamat
			// tempoh (registrationsweep + billExpiryDate ToyyibPay).
			hasPendingBill, err := h.queries.HasPendingRegistrationPayment(ctx, targetID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
				return
			}
			if hasPendingBill {
				c.JSON(http.StatusConflict, gin.H{
					"error": "ahli ada bil pendaftaran online yang belum selesai — selesaikan/tamatkan bil tu dulu sebelum langkau bayaran, kalau tidak ahli boleh bayar dua kali",
				})
				return
			}
		} else {
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
	actor := auditActor(c, q)
	newAuditFields := mergeAuditFields(
		map[string]any{"status": updated.Status},
		actorAuditFields(actor),
	)
	if status == "approved" && req.BypassPayment {
		// Ahli ni approved TANPA baris 'succeeded' dalam
		// registration_payments — nota+aktor di sini ialah SATU-SATUNYA
		// tempat sebab tu direkod, jadi wajib ada nilai (bukan best-effort).
		newAuditFields["payment_bypassed"] = true
		newAuditFields["bypass_reason"] = req.BypassReason
	}
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      actor,
		Old:        map[string]any{"status": target.Status},
		New:        newAuditFields,
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

// CancelMemberRegistrationPayment — POST /members/:id/cancel-registration-payment.
// Admin/superadmin batalkan bil yuran pendaftaran 'pending' ahli supaya
// laluan langkau bayaran boleh digunakan (ahli lama migrasi manual).
// Semak gateway DULU — kalau dah bayar, tolak; kalau masih pending,
// tandakan 'failed' + audit.
func (h *ProfileHandler) CancelMemberRegistrationPayment(c *gin.Context) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isAdminUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "admin")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
		return
	}
	if !isAdminUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma admin/superadmin boleh batalkan bil pendaftaran"})
		return
	}

	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh laksanakan tindakan ini pada akaun sendiri"})
		return
	}

	if _, err := h.queries.GetProfileByUserID(ctx, targetID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	pending, err := h.queries.GetLatestPendingRegistrationPayment(ctx, targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "tiada bil pendaftaran pending untuk ahli ini"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
		return
	}

	if pending.GatewayRef.Valid && pending.GatewayRef.String != "" && h.regPaymentGW != nil && h.regPaymentGW.Enabled() {
		status, err := h.regPaymentGW.CheckStatus(ctx, pending.GatewayRef.String)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "gagal semak status bil di gateway"})
			return
		}
		switch status {
		case "succeeded":
			if _, uerr := h.queries.UpdateRegistrationPaymentStatusByGatewayRef(ctx, sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
				Gateway: pending.Gateway, GatewayRef: pending.GatewayRef, Status: "succeeded",
			}); uerr != nil && !errors.Is(uerr, pgx.ErrNoRows) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "ahli sudah bayar yuran pendaftaran — luluskan tanpa langkau bayaran"})
			return
		case "failed":
			if _, uerr := h.queries.UpdateRegistrationPaymentStatusByGatewayRef(ctx, sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
				Gateway: pending.Gateway, GatewayRef: pending.GatewayRef, Status: "failed",
			}); uerr != nil && !errors.Is(uerr, pgx.ErrNoRows) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
				return
			}
			if err := h.recordCancelRegistrationPaymentAudit(c, targetID, pending); err != nil {
				log.Printf("audit batalkan bil pendaftaran: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "failed", "payment_id": pending.ID.String()})
			return
		}
	} else if !pending.GatewayRef.Valid || pending.GatewayRef.String == "" {
		if err := h.queries.MarkRegistrationPaymentFailed(ctx, pending.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
			return
		}
		if err := h.recordCancelRegistrationPaymentAudit(c, targetID, pending); err != nil {
			log.Printf("audit batalkan bil pendaftaran: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "failed", "payment_id": pending.ID.String()})
		return
	}

	expired, err := h.queries.ExpireRegistrationPayment(ctx, pending.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusConflict, gin.H{"error": "bil pendaftaran sudah tidak pending"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
		return
	}

	if err := h.recordCancelRegistrationPaymentAudit(c, targetID, pending); err != nil {
		log.Printf("audit batalkan bil pendaftaran: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batalkan bil pendaftaran"})
		return
	}

	amount := int64(expired.AmountCents)
	paymentlog.Record(ctx, h.queries, paymentlog.Entry{
		Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventReconcileCheck,
		Status: "failed", Gateway: expired.Gateway,
		GatewayRef: expired.GatewayRef.String, AmountCents: &amount,
		UserID: &targetID, RelatedID: &expired.ID,
		Message: "admin batalkan bil pending",
	})

	c.JSON(http.StatusOK, gin.H{"status": "failed", "payment_id": expired.ID.String()})
}

// recordCancelRegistrationPaymentAudit — jejak siapa batalkan bil pending
// ahli mana (entity_id = user ahli; actor = admin/superadmin snapshot).
func (h *ProfileHandler) recordCancelRegistrationPaymentAudit(c *gin.Context, targetID uuid.UUID, pending sqlc.RegistrationPayment) error {
	old := map[string]any{"registration_payment_status": "pending"}
	if pending.GatewayRef.Valid && pending.GatewayRef.String != "" {
		old["gateway_ref"] = pending.GatewayRef.String
	}
	actor := auditActor(c, h.queries)
	return audit.Record(c.Request.Context(), h.queries, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      actor,
		Old:        old,
		New: mergeAuditFields(map[string]any{
			"registration_payment_status": "failed",
			"cancelled_by_admin":            true,
			"payment_id":                    pending.ID.String(),
		}, actorAuditFields(actor)),
	})
}

type accountDeletionRequestResponse struct {
	Status      string `json:"status"`
	RequestedAt string `json:"requested_at"`
}

// RequestAccountDeletion — POST /me/deletion-request. Keperluan Google
// Play Console: app yang sokong penciptaan akaun MESTI sediakan cara ahli
// MEMINTA pemadaman akaun + data. v1 sengaja REQUEST-sahaja — rekod
// permintaan + jejak audit, staff tindak secara MANUAL (akses DB terus)
// buat masa ni. TIADA auto-purge post/bayaran/pendaftaran aktiviti dsb —
// lihat TODO.md untuk gap ni sebagai fast-follow.
//
// Sengaja TIDAK di bawah RequireApprovedStatus — ahli pending/rejected pun
// berhak minta akaun dia dipadam.
//
// Idempoten: panggilan berulang oleh ahli sama pulangkan 200 dengan data
// permintaan yang SEDIA ADA (bukan ralat) — padanan pola
// AddBlockedEmailDomain/ApproveProfile.
func (h *ProfileHandler) RequestAccountDeletion(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod permintaan pemadaman akaun"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	row, err := q.CreateAccountDeletionRequest(ctx, userID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod permintaan pemadaman akaun"})
			return
		}
		// `on conflict do nothing` — dah ada permintaan sedia ada. Ambil
		// baris tu supaya response pulangkan data ASAL (bukan cuba
		// dicipta semula), dan JANGAN tulis catatan audit baharu — tiada
		// apa yang berubah.
		existing, getErr := h.queries.GetAccountDeletionRequestByUserID(ctx, userID)
		if getErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod permintaan pemadaman akaun"})
			return
		}
		c.JSON(http.StatusOK, accountDeletionRequestResponse{
			Status:      existing.Status,
			RequestedAt: formatTime(existing.RequestedAt),
		})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityAccountDeletionRequest,
		EntityID:   userID,
		Action:     audit.ActionCreate,
		Actor:      auditActor(c, q),
		New:        map[string]any{"status": row.Status},
	}); err != nil {
		log.Printf("audit permintaan pemadaman akaun: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod permintaan pemadaman akaun"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod permintaan pemadaman akaun"})
		return
	}

	c.JSON(http.StatusOK, accountDeletionRequestResponse{
		Status:      row.Status,
		RequestedAt: formatTime(row.RequestedAt),
	})
}

func textToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// textOrEmpty — padanan textToPtr, tapi pulangkan "" (bukan nil) bila
// tak sah. Utk tapak panggilan yang perlukan string terus (cth
// receipt.Donation/FeePayment, yang dah ada fallback() sendiri utk rentetan
// kosong).
func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func ptrToText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
