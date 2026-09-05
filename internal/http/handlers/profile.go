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
	pool         *pgxpool.Pool
	queries      *sqlc.Queries
	emailClient  *email.Client
	r2           *storage.R2Client
	feeCents     int64
	regPaymentGW payment.Gateway
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
	MemberID      *string `json:"member_id"`
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
	// RegistrationPaymentStatus - "pending"/"succeeded"/"failed", atau
	// null kalau ahli tak pernah cuba bayar langsung. Ditambah 2026-08-15:
	// webhook ToyyibPay dah rekod bayaran gagal/berjaya BETUL dalam DB
	// sejak awal, tapi client tak pernah baca - ahli nampak "tiada apa
	// berlaku" walau hasil sebenar sentiasa betul di sisi pelayan. Cuma
	// diisi untuk ahli `pending` (approved tak perlu, dah lepas gate).
	RegistrationPaymentStatus *string `json:"registration_payment_status"`
	// RegistrationFeeCents - jumlah (sen) yuran pendaftaran SEMASA
	// (`REGISTRATION_FEE_CENTS`), supaya client boleh papar jumlah SEBELUM
	// ahli tekan bayar (checkout ToyyibPay tak dedah jumlah dalam app -
	// cuma redirect ke halaman ToyyibPay). Cuma diisi untuk ahli belum
	// `approved`, padan skop `RegistrationPaymentStatus` di atas - ahli
	// approved dah lepas gate, tak perlu tahu angka ni lagi.
	RegistrationFeeCents *int64  `json:"registration_fee_cents"`
	TelegramLinked       bool    `json:"telegram_linked"`
	TelegramUsername     *string `json:"telegram_username"`

	// EmergencyContactName/Phone/HealthNotes - self-service (PATCH /me).
	EmergencyContactName  *string `json:"emergency_contact_name"`
	EmergencyContactPhone *string `json:"emergency_contact_phone"`
	HealthNotes           *string `json:"health_notes"`
	// IsActive - flag keahlian (BUKAN status kelulusan). Baca sahaja di
	// sini - cuma management boleh tukar, via PATCH /members/:id/active.
	IsActive bool `json:"is_active"`

	// DepartmentCode/DepartmentName/Position - baca sahaja di sini, cuma
	// manager ke atas boleh tukar (via PATCH /members/:id/department),
	// BUKAN self-service (beza drpd EmergencyContact*/HealthNotes).
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`

	// StaffID/StaffIDVerifiedAt - nama medan sama macam memberResponse/
	// memberDetailResponse. Ditambah 2026-09-03 (Opus verify): ahli
	// `pending` cuma boleh panggil /me (endpoint /members* di bawah
	// RequireApprovedStatus), jadi tanpa medan ni dia langsung tiada cara
	// nampak nombor staff dia sendiri atau sama ada ia dah disahkan -
	// sedangkan pengesahan itulah gate kelulusan dia. Data caller SENDIRI,
	// jadi tiada tiering di sini (beza drpd memberResponse).
	// VerifiedAt null = belum disahkan.
	StaffID           string  `json:"staff_id"`
	StaffIDVerifiedAt *string `json:"staff_id_verified_at"`
}

// Me setara `myProfileProvider` di Flutter - profil user semasa. Sengaja
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
		MemberID:                  textToPtr(row.MemberID),
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
		StaffID:                   row.StaffID,
		StaffIDVerifiedAt:         formatTimeNullable(row.StaffIDVerifiedAt),
	})
}

type updateMeRequest struct {
	// DisplayName/Phone ialah *string (bukan string) supaya "tak dihantar"
	// dapat dibezakan daripada "buang nilai" - validator gin/go-playground
	// TIDAK menguatkuasakan `max` pada medan pointer (ia senyap dilangkau
	// untuk Kind() Ptr), jadi had panjang disemak secara manual dalam
	// UpdateMe selepas bindJSON, bukan melalui tag `binding`.
	DisplayName *string `json:"display_name"`
	Phone       *string `json:"phone"`

	// AvatarR2Key - kunci daripada /uploads/presign. Pointer supaya tiga
	// keadaan boleh dibezakan: tak dihantar (biar), string kosong (buang
	// avatar), atau kunci baharu (ganti).
	AvatarR2Key *string `json:"avatar_r2_key"`

	// EmergencyContactName/Phone/HealthNotes - sama pola DisplayName/Phone:
	// nil = tak dihantar (biar), string kosong dibenarkan (buang nilai).
	EmergencyContactName  *string `json:"emergency_contact_name"`
	EmergencyContactPhone *string `json:"emergency_contact_phone"`
	HealthNotes           *string `json:"health_notes"`
}

// UpdateMe setara `ProfileRepository.update` di Flutter - field yang
// tak dihantar (nil) DIBIARKAN tak berubah; field yang dihantar
// (termasuk string kosong) ditetapkan terus kepada nilai tu. Sengaja
// TIDAK di bawah RequireApprovedStatus - sama sebab macam Me.
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
	// membuka semula bug asal yang perubahan register cuba tutup - ahli
	// approved boleh PATCH phone jadi "abc", ToyyibPay createBill akan
	// tolak semula bila ahli tu cuba bayar). String KOSONG tetap
	// dibenarkan (buang nombor, padanan pola medan opsyenal lain di
	// handler ni) - cuma nilai BUKAN kosong perlu format sah.
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
	// Sama pola `Phone` - waris pun nombor Malaysia, sahkan format sama
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
// satu pun berlaku - kalau tidak avatar lama bocor dalam bucket atau
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
		// Kunci datang dari client, jadi ia MESTI disahkan milik caller -
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
		// Kunci dah jadi milik profil sekarang - buang daripada pending
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
	MemberID    *string `json:"member_id"`
	DisplayName *string `json:"display_name"`

	// Nullable: emel ahli LAIN cuma didedahkan kepada management. Sejak
	// keterlihatan ahli diluaskan (ahli kini nampak ahli + supervisor),
	// menghantarnya kepada semua orang bermakna setiap ahli boleh menyalin
	// direktori emel penuh - pendedahan yang jauh lebih luas daripada niat
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

	// RegistrationPaymentStatus - "pending"/"succeeded"/"failed", atau
	// null. Ditambah 2026-08-15 supaya management nampak siapa dah bayar
	// SEBELUM tekan Luluskan (gate `ApproveMember` sedia ada sejak awal,
	// cuma tak kelihatan di senarai sebelum ni). Sama pola privasi
	// dengan Email - cuma management yang dapat nilai sebenar, ahli
	// biasa dapat null (bukan medan yang perlu didedahkan untuk lihat
	// ahli lain).
	RegistrationPaymentStatus *string `json:"registration_payment_status"`

	// IsActive - flag keahlian (bukan status kelulusan). Dedah kepada
	// semua viewer (bukan cuma management, padanan `Status`) supaya
	// senarai ahli papar status aktif konsisten dgn cara `Status` sedia
	// ada dipapar.
	IsActive bool `json:"is_active"`

	// DepartmentCode/DepartmentName/Position - dedah kepada semua viewer
	// (padanan IsActive) - info organisasi, bukan data sensitif macam
	// Email/RegistrationPaymentStatus.
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`

	// StaffID - nombor staff SEBENAR ahli (nombor pekerja majikan), jadi
	// ia ditier macam Email/RegistrationPaymentStatus di atas dan BUKAN
	// macam MemberID: cuma management (+ baris caller sendiri) dapat nilai
	// sebenar, ahli biasa dapat `null` (Opus verify 2026-09-03 - sebelum
	// ni setiap ahli approved boleh kikis nombor staff semua orang melalui
	// GET /members).
	//
	// StaffIDVerifiedAt kekal terbuka kepada semua viewer yang boleh
	// nampak baris ni: ia cuma STATUS pengesahan (sudah/belum), bukan
	// nombor itu sendiri, dan management perlu nampak status tu terus dari
	// barisan kelulusan (Task 7). null = belum disahkan.
	StaffID           *string `json:"staff_id"`
	StaffIDVerifiedAt *string `json:"staff_id_verified_at"`
}

// Members setara `membersProvider` di Flutter - gantian RLS
// `select_all_profiles_management`. Keterlihatan ikut hierarki
// `roles.rank` (lihat visibleRankCeiling), BUKAN lagi "ahli nampak diri
// sendiri sahaja". Dua kawalan tambahan:
//
//   - Ahli biasa cuma nampak ahli berstatus 'approved' (+ baris dia
//     sendiri) - direktori ahli, bukan barisan kelulusan.
//   - `?status=pending` (barisan kelulusan Stage 11) management sahaja.
//
// Semua tapisan dikuatkuasakan dalam SQL - lihat ListVisibleProfiles.
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
		// Padanan pola Email - status bayaran cuma berguna untuk
		// management, ahli biasa dapat null (lihat komen memberResponse).
		paymentStatus := row.RegistrationPaymentStatus
		if !isManagement {
			paymentStatus = ""
		}
		// Padanan pola Email juga - nombor staff SENDIRI kekal nampak,
		// nombor staff orang lain cuma untuk management.
		staffID := row.StaffID
		if !isManagement && row.UserID != userID {
			staffID = ""
		}
		members[i] = h.toMemberResponse(ctx, memberRow{
			UserID: row.UserID, MemberID: row.MemberID, DisplayName: row.DisplayName,
			Email: email, RoleKey: row.RoleKey, RoleName: row.RoleName,
			RoleRank: row.RoleRank, Category: row.RoleCategory, Status: row.Status,
			AvatarKey: row.AvatarR2Key, RegistrationPaymentStatus: paymentStatus,
			IsActive: row.IsActive, DepartmentCode: row.DepartmentCode,
			DepartmentName: row.DepartmentName, Position: row.Position,
			StaffID: staffID, StaffIDVerifiedAt: row.StaffIDVerifiedAt,
		})
	}
	c.JSON(http.StatusOK, members)
}

// memberDetailResponse - GET /members/:id, tiga peringkat keterlihatan
// medan (lihat GetMemberDetail). Tier 2/3 SENTIASA hadir dlm JSON tapi
// bernilai `null` bila caller tak layak - padanan corak Email/
// RegistrationPaymentStatus dlm memberResponse, supaya client bezakan
// "null = disembunyikan" drpd "tiada nilai" tanpa logik keadaan tambahan.
type memberDetailResponse struct {
	// Tier 1 - sesiapa dlm skop visibleRankCeiling.
	UserID         string  `json:"user_id"`
	MemberID       *string `json:"member_id"`
	DisplayName    *string `json:"display_name"`
	AvatarURL      *string `json:"avatar_url"`
	RoleKey        string  `json:"role_key"`
	RoleName       string  `json:"role_name"`
	RoleRank       int32   `json:"role_rank"`
	Category       string  `json:"category"`
	Status         string  `json:"status"`
	IsActive       bool    `json:"is_active"`
	DepartmentCode *string `json:"department_code"`
	DepartmentName *string `json:"department_name"`
	Position       *string `json:"position"`
	// StaffIDVerifiedAt - Tier 1 (padanan memberResponse, Task 7): STATUS
	// pengesahan staff (sudah/belum) bukan data sensitif macam emel/
	// telefon, dan management perlu nampak status tu dari senarai. Nombor
	// staff SENDIRI pula Tier 2 di bawah.
	StaffIDVerifiedAt *string `json:"staff_id_verified_at"`

	// Tier 2 - caller.RoleCategory == authz.CategoryManagement sahaja.
	Email                     *string `json:"email"`
	Phone                     *string `json:"phone"`
	RegistrationPaymentStatus *string `json:"registration_payment_status"`
	// StaffID - Tier 2, bukan Tier 1 (Opus verify 2026-09-03): nombor
	// staff ialah nombor pekerja majikan, jadi ia ditier macam Email/
	// Phone. Ahli nampak nombor staff SENDIRI melalui GET /me (yang
	// memulangkan staff_id + staff_id_verified_at), bukan melalui
	// endpoint ni.
	StaffID *string `json:"staff_id"`

	// Tier 3 - caller.RoleKey == "superadmin" sahaja.
	EmergencyContactName  *string           `json:"emergency_contact_name"`
	EmergencyContactPhone *string           `json:"emergency_contact_phone"`
	HealthNotes           *string           `json:"health_notes"`
	TelegramLinked        *bool             `json:"telegram_linked"`
	TelegramUsername      *string           `json:"telegram_username"`
	Addresses             []addressResponse `json:"addresses"`
}

// GetMemberDetail - GET /members/:id. Skrin profil SATU ahli (bukan
// senarai) - berbeza drpd Members yang pulangkan pelbagai baris ringkas.
// Tiga peringkat keterlihatan medan dikuatkuasakan DI SINI (server-side)
// - client cuma render apa yang response bagi, tiada logik sembunyi/
// tunjuk medan berasaskan role di client:
//
//	Tier 1 (semua viewer dlm skop visibleRankCeiling): nama, gambar,
//	no. ahli, role, bahagian, jawatan, status aktif.
//	Tier 2 (caller.RoleCategory == management): + emel, telefon, status
//	bayaran pendaftaran.
//	Tier 3 (caller.RoleKey == "superadmin", BUKAN sekadar admin/manager):
//	+ kenalan kecemasan, nota kesihatan, status/username Telegram,
//	senarai alamat penuh.
//
// Keterlihatan BARIS (boleh nampak ahli ni langsung ke tidak) guna
// visibleRankCeiling sama macam Members - 404 (bukan 403) kalau target
// rank > siling caller, elak dedah KEWUJUDAN baris rank tinggi kepada
// viewer bawah (padanan cara GetPostByID dsb pulang 404 generik).
//
// Tiada audit log - bacaan sahaja, padanan GET /members (audit cuma utk
// TINDAKAN spt approve/reject/tukar role, bukan senarai/lihat).
func (h *ProfileHandler) GetMemberDetail(c *gin.Context) {
	targetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
		return
	}

	// 404 (bukan 500) - ID tak wujud ialah keadaan biasa (pautan lapuk,
	// ahli dipadam), bukan ralat pelayan.
	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	roles, err := h.queries.ListRoles(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat profil ahli"})
		return
	}
	// Keterlihatan BARIS - kena semak SEBELUM apa-apa medan (termasuk
	// Tier 1) dibina, kalau tidak viewer bawah siling boleh nampak
	// serpihan Tier 1 target rank tinggi sebelum 404 sempat dipulangkan.
	if target.RoleRank > visibleRankCeiling(roles, caller.RoleRank) {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	// Padanan peraturan Members - ahli biasa cuma nampak ahli berstatus
	// 'approved' (+ baris dia sendiri). Endpoint ni direktori ahli, bukan
	// barisan kelulusan, jadi profil ahli 'pending'/'rejected' tak patut
	// dibaca melalui sini oleh bukan-pengurusan.
	if caller.RoleCategory != authz.CategoryManagement &&
		target.Status != "approved" && target.UserID != callerID {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	res := memberDetailResponse{
		UserID:            target.UserID.String(),
		MemberID:          textToPtr(target.MemberID),
		DisplayName:       textToPtr(target.DisplayName),
		AvatarURL:         avatarURLFor(ctx, h.r2, target.AvatarR2Key),
		RoleKey:           target.RoleKey,
		RoleName:          target.RoleName,
		RoleRank:          target.RoleRank,
		Category:          target.RoleCategory,
		Status:            target.Status,
		IsActive:          target.IsActive,
		DepartmentCode:    textToPtr(target.DepartmentCode),
		DepartmentName:    textToPtr(target.DepartmentName),
		Position:          textToPtr(target.Position),
		StaffIDVerifiedAt: formatTimeNullable(target.StaffIDVerifiedAt),
	}

	if caller.RoleCategory == authz.CategoryManagement {
		email := target.Email
		res.Email = &email
		res.Phone = textToPtr(target.Phone)
		staffID := target.StaffID
		res.StaffID = &staffID

		// Padanan pola Me() - status bayaran cuma wujud kalau ahli PERNAH
		// cuba bayar (pgx.ErrNoRows = tak pernah, bukan ralat pelayan).
		if status, err := h.queries.GetLatestRegistrationPaymentStatus(ctx, targetID); err == nil {
			res.RegistrationPaymentStatus = &status
		} else if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("baca status bayaran pendaftaran (target=%s): %v", targetID, err)
		}
	}

	if caller.RoleKey == superAdminRoleKey {
		res.EmergencyContactName = textToPtr(target.EmergencyContactName)
		res.EmergencyContactPhone = textToPtr(target.EmergencyContactPhone)
		res.HealthNotes = textToPtr(target.HealthNotes)
		telegramLinked := target.TelegramChatID.Valid
		res.TelegramLinked = &telegramLinked
		res.TelegramUsername = textToPtr(target.TelegramUsername)

		addrRows, err := h.queries.ListAddressesByUser(ctx, targetID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat profil ahli"})
			return
		}
		addresses := make([]addressResponse, len(addrRows))
		for i, row := range addrRows {
			addresses[i] = toAddressResponse(row)
		}
		res.Addresses = addresses
	}

	c.JSON(http.StatusOK, res)
}

// visibleRankCeiling - rank TERTINGGI yang seorang viewer boleh nampak
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
		// Rank tertinggi (topRank) sengaja dilangkau - itulah superadmin.
		if r.Rank > viewerRank && r.Rank < topRank && (ceiling == viewerRank || r.Rank < ceiling) {
			ceiling = r.Rank
		}
	}
	return ceiling
}

// toMemberResponse - `email` kosong bermakna sembunyikan medan itu.
// memberRow - input untuk toMemberResponse. Struct, bukan senarai
// parameter: versi lama ada sembilan argumen positional bertype string
// yang sama, jadi tertukar susunan (cth roleKey lawan roleName) akan
// compile dengan senyap.
type memberRow struct {
	UserID                    uuid.UUID
	MemberID                  pgtype.Text
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
	StaffID                   string // kosong = sembunyikan medan (padanan Email)
	StaffIDVerifiedAt         pgtype.Timestamptz
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
	var staffIDPtr *string
	if m.StaffID != "" {
		staffIDPtr = &m.StaffID
	}
	return memberResponse{
		UserID:                    m.UserID.String(),
		MemberID:                  textToPtr(m.MemberID),
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
		StaffID:                   staffIDPtr,
		StaffIDVerifiedAt:         formatTimeNullable(m.StaffIDVerifiedAt),
	}
}

// ListRoles (Stage 12) - management sahaja. Senarai role untuk UI edit
// role (bottom sheet). Ditapis kepada role yang caller memang BOLEH
// assign (rank lebih rendah drpd rank dia - syarat sama yang
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

// UpdateMemberRole (Stage 12) - management sahaja, dikawal hierarki
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

	// Perubahan keistimewaan - catatan audit paling bernilai dalam sistem
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
		DepartmentCode: updated.DepartmentCode, Position: updated.Position,
		StaffID: updated.StaffID, StaffIDVerifiedAt: updated.StaffIDVerifiedAt,
	}))
}

type updateMemberActiveRequest struct {
	IsActive bool `json:"is_active"`
}

type memberActiveResponse struct {
	UserID   string `json:"user_id"`
	IsActive bool   `json:"is_active"`
}

// UpdateMemberActive - PATCH /members/:id/active. Tukar flag KEAHLIAN
// (`is_active`), BERASINGAN drpd `status` (kelulusan) - ahli `approved`
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
	// DepartmentCode/Position - GANTI PENUH (bukan partial macam UpdateMe):
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

// UpdateMemberDepartment - PATCH /members/:id/department. Manager KE ATAS
// sahaja (superadmin/admin/manager - bukan supervisor, keputusan produk
// 2026-08-25), gate rank "SETARAF DAN KE BAWAH sahaja"
// (caller.RoleRank >= target.RoleRank) - BEZA drpd UpdateMemberRole/
// UpdateMemberActive yang caller.RoleRank kena STRICTLY lebih tinggi
// (>). Bahagian/jawatan bukan keistimewaan sistem (role/status aktif),
// jadi manager boleh tetapkan utk manager lain yang setaraf - termasuk
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

	// Baca semula MELALUI tx (bukan h.queries) - perlukan department_name
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
	// BypassPayment - admin/superadmin sahaja (rank >= "admin"). Langkau
	// gate `HasSucceededRegistrationPayment` untuk ahli lama yang dah
	// bayar secara manual sebelum sistem digital wujud. Nota WAJIB bila
	// ni true - jejak audit kelab lama->digital kena jelas siapa langkau
	// bayaran, untuk siapa, dan kenapa.
	BypassPayment bool   `json:"bypass_payment"`
	BypassReason  string `json:"bypass_reason" binding:"max=500"`
}

// ApproveMember (Stage 11) - management sahaja. Set status='approved',
// hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) ApproveMember(c *gin.Context) {
	// Body ini pilihan sepenuhnya (ahli biasa diluluskan tanpa body
	// langsung sebelum ni) - kosong terus laluan sedia ada (gate bayaran
	// biasa) tidak berubah. Semak `err` terus terhadap io.EOF (bukan
	// `ContentLength > 0`, Opus verify: ContentLength == -1 untuk
	// Transfer-Encoding: chunked/unknown, jadi guard ContentLength tu
	// terlepas body cacat yang dihantar TANPA Content-Length eksplisit)
	// - body cacat (cth `bypass_payment` jenis string bukan bool) pulang
	// 400 jelas, bukan senyap gagal-jadi-false lalu mengelirukan admin
	// dengan mesej "ahli belum bayar" walhal dia memang cuba bypass.
	var req approveMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": friendlyBindError(err)})
		return
	}
	h.setMemberStatus(c, "approved", req)
}

// RejectMember (Stage 11) - management sahaja. Set status='rejected'
// (row KEKAL, bukan padam - audit trail + boleh undo via ApproveMember
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
	// boleh reject SEMUA management, termasuk yang terakhir - sistem
	// approval jadi buntu tanpa cara in-app untuk pulih).
	if status == "rejected" && target.RoleCategory == authz.CategoryManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh tolak ahli pengurusan"})
		return
	}

	// Status dah sama - no-op idempotent. Pulang keadaan semasa TANPA
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
	// 'succeeded' (Stage 12, ToyyibPay - lihat TODO.md bahagian Payment).
	// Ahli sedia ada yang dah approved sebelum ciri ni wujud tak pernah
	// sampai sini (no-op di atas dah return awal), jadi grandfathered
	// SECARA AUTOMATIK tanpa perlu semakan "bila akaun dicipta" - hanya
	// peralihan SEBENAR pending->approved kena gate. RejectMember tak
	// disentuh - penolakan mesti berfungsi tanpa kira status bayaran.
	// Diletak SEBELUM tx.Begin sengaja: kalau tak lulus, tiada transaksi
	// untuk dibuka langsung.
	if status == "approved" {
		// Gate keras: nombor staff MESTI disahkan sebelum ahli boleh
		// diluluskan langsung - tiada bypass utk semakan ni (beza drpd
		// gate bayaran di bawah, yang admin boleh langkau). Diletak
		// SEBELUM blok bayaran/exempt di bawah supaya `staffExempt`
		// (ditakrif seterusnya) SENTIASA true pada titik itu.
		if !target.StaffIDVerifiedAt.Valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff ahli ni belum disahkan - sahkan nombor staff dulu sebelum meluluskan"})
			return
		}

		// staffExempt sentiasa true di sini (gate keras di atas dah pulang
		// awal kalau tidak) - dikekalkan sbg pemboleh ubah bernama eksplisit
		// (bukan di-inline) sebab ia titik pelanjutan utk kemungkinan jenis
		// "ahli am" (bukan staff) yang disebut di luar skop dlm spec - bila
		// itu wujud, staffExempt TAK LAGI sentiasa true, dan cawangan else
		// di bawah (kod bayaran/bypass sedia ada, tak berubah) akan jadi
		// boleh dicapai lagi.
		staffExempt := target.StaffIDVerifiedAt.Valid
		if staffExempt {
			// Exempt sepenuhnya drpd gate bayaran. PAKSA flag bypass ke
			// false EKSPLISIT di sini - walau caller (cth manager rank 60,
			// yang TAK dibenarkan bypass admin) hantar bypass_payment=true,
			// JANGAN biar ia sampai ke rekod audit di bawah sbg seolah-olah
			// kuasa bypass admin betul-betul digunakan. Exemption ni datang
			// drpd staff disahkan, BUKAN drpd kuasa admin - audit mesti
			// mencerminkan sebab sebenar (v1 bug: audit palsu direkod bila
			// blok bayaran seluruhnya dilangkau tanpa paksaan ni).
			req.BypassPayment = false

			// (d) HasPendingRegistrationPayment SAHAJA yang tetap disemak
			// walau staffExempt - kes tepi: ahli ada bil ToyyibPay pending
			// (cth cuba bayar dulu sblm staff disahkan), staff disahkan,
			// lepas tu diluluskan. Tiada block keras di sini (jangan sekat
			// approval staff-exempt semata sebab bil pending yg IRRELEVANT
			// kepada exemption dia) - cuma log amaran supaya staff ada
			// jejak utk refund manual kalau bil tu lepas ni tiba-tiba
			// 'succeeded' (webhook lewat). (a)/(b)/(c) di atas TIDAK
			// relevan lagi sbg staffExempt dah paksa BypassPayment=false.
			hasPendingBill, err := h.queries.HasPendingRegistrationPayment(ctx, targetID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
				return
			}
			if hasPendingBill {
				log.Printf("approve ahli staff-exempt (target=%s) ada bil pendaftaran pending - semak manual kalau bil ni jadi succeeded lepas ni", targetID)
			}
		} else {
			// ...KOD SEDIA ADA (a)-(d) TAK BERUBAH LANGSUNG - unreachable
			// hari ini sbg gate keras di atas dah pastikan staffExempt
			// sentiasa true, tapi dikekalkan utk laluan bukan-staff akan
			// datang (lihat komen staffExempt di atas).

			// Semak bayaran SEBENAR dulu, tak kira flag bypass - kalau ahli
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
				// Langkau bayaran - hanya admin/superadmin (rank >= "admin"),
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
				// bila-bila masa (Opus verify: MEDIUM, dan verify susulan -
				// TANPA tapisan gateway_ref, lihat komen query) - kalau baris
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
						"error": "ahli ada bil pendaftaran online yang belum selesai - selesaikan/tamatkan bil tu dulu sebelum langkau bayaran, kalau tidak ahli boleh bayar dua kali",
					})
					return
				}
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"error": "ahli belum bayar yuran pendaftaran"})
				return
			}
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
			// Guard replay dalam query - dua permintaan serentak, yang kalah
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

	// Kelulusan keahlian ialah keputusan pentadbiran - siapa yang benarkan
	// (atau halang) seseorang masuk mesti dapat dijawab kemudian.
	actor := auditActor(c, q)
	newAuditFields := mergeAuditFields(
		map[string]any{"status": updated.Status},
		actorAuditFields(actor),
	)
	if status == "approved" && req.BypassPayment {
		// Ahli ni approved TANPA baris 'succeeded' dalam
		// registration_payments - nota+aktor di sini ialah SATU-SATUNYA
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
		// best-effort di luar - kegagalan cuma dilog, dan ahli yang ditolak
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

	// Selepas commit - best effort. Emel/notifikasi yang gagal tak patut
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

// staffFeeExempt - ahli ni dikecualikan (atau PASTI akan dikecualikan)
// daripada yuran pendaftaran? Dua cabang:
//
//   - `staff_id_verified_at` dah diisi - pengecualian SEDANG berkuat kuasa.
//     Ini `staffExempt` yang sama dalam setMemberStatus (Task 8).
//   - Belum disahkan TAPI `staff_id` ialah nombor staff SEBENAR (bukan
//     placeholder `user_id::text` yang migrasi isi untuk baris lama) -
//     satu-satunya jalan ahli ni boleh diluluskan ialah melalui
//     VerifyStaffID (gate keras dalam setMemberStatus), dan detik itu
//     berlaku dia jadi exempt. Jadi dia TAK PERNAH terhutang yuran.
//
// Guna untuk flag `outstanding_registration_fee` (GET /me/payments):
// tanpa cabang kedua, setiap ahli pending baharu nampak banner "bayar
// yuran" untuk yuran yang dia takkan pernah perlu bayar - dan kalau dia
// betul-betul bayar, duit tu masuk tanpa laluan refund yang jelas (lihat
// amaran bil pending dalam setMemberStatus). Baris lama pra-migrasi
// (staff_id placeholder, belum disahkan) TIDAK exempt - mereka daftar
// bawah rejim yuran lama dan memang boleh terhutang.
//
// setMemberStatus SENGAJA kekal guna `target.StaffIDVerifiedAt.Valid`
// terus, BUKAN fungsi ni: gate kelulusan mesti kekal pada pengesahan
// SEBENAR ("akan disahkan" bukan alasan untuk luluskan sesiapa).
func staffFeeExempt(p sqlc.GetProfileByUserIDRow) bool {
	if p.StaffIDVerifiedAt.Valid {
		return true
	}
	return p.StaffID != "" && p.StaffID != p.UserID.String()
}

type verifyStaffIDRequest struct {
	// StaffID - override pilihan. TAK dibenarkan kalau ahli DAH disahkan
	// (lihat VerifyStaffID) - guna PATCH /members/:id/staff-id (Task 6)
	// untuk betulkan nombor staff selepas pengesahan pertama.
	StaffID *string `json:"staff_id"`
}

type verifyStaffIDResponse struct {
	UserID     string  `json:"user_id"`
	MemberID   string  `json:"member_id"`
	VerifiedAt *string `json:"verified_at"`
}

// VerifyStaffID - POST /members/:id/verify-staff-id. Manager KE ATAS
// sahaja mengesahkan nombor staff yang ahli isi semasa daftar
// (POST /auth/register, Task 4) dan menjana member_id (format
// MARC-{staff_id}/{tahun}-{kod}) buat kali pertama - ahli tak dapat
// member_id sehingga langkah ni selesai. Baris sedia ada yang dah
// ada member_id format lama dibiarkan; VerifyStaffID tak tulis semula
// nilai yang dah wujud.
//
// Idempoten: panggilan kedua pulang 200 dengan keadaan sedia ada TANPA
// menulis catatan audit baharu - selagi caller TAK cuba hantar
// `staff_id` override sekali gus (lihat cawangan 409 di bawah).
func (h *ProfileHandler) VerifyStaffID(c *gin.Context) {
	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isManagerUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "manager")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}
	if !isManagerUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma manager ke atas boleh sahkan nombor staff"})
		return
	}

	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	// Elak self-lockout - padanan setMemberStatus/UpdateMemberRole/
	// UpdateMemberActive.
	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh sahkan nombor staff akaun sendiri"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}

	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	// Semakan hierarki rank - padanan UpdateMemberRole/UpdateMemberActive/
	// CorrectStaffID/CorrectMemberID. Pengesahan ni MENULIS pada baris
	// target (staff_id override + member_id dijana + ahli jadi fee-exempt),
	// jadi ia tertakluk peraturan sama: jangan benarkan caller sentuh ahli
	// setaraf/lebih tinggi. Tanpa ni, manager boleh cap akaun rank lebih
	// tinggi yang masih pending dengan nombor staff pilihan dia sendiri.
	// Diletak SEBELUM semakan status/body supaya tiada maklumat keadaan
	// target bocor kepada caller yang memang tak layak.
	if caller.RoleRank <= target.RoleRank {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh edit ahli setaraf/lebih tinggi drpd anda"})
		return
	}
	if target.Status == "rejected" {
		c.JSON(http.StatusConflict, gin.H{"error": "ahli ni dah ditolak"})
		return
	}

	// Body ni pilihan sepenuhnya (padanan ApproveMember) - semak `err`
	// terus terhadap io.EOF supaya body cacat pulang 400 jelas, bukan
	// senyap diabaikan.
	var req verifyStaffIDRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": friendlyBindError(err)})
		return
	}

	if target.StaffIDVerifiedAt.Valid {
		if req.StaffID != nil {
			// Ahli ni DAH disahkan dan caller cuba hantar override dalam
			// panggilan yang sama - JANGAN senyap abaikan (itu akan
			// menyembunyikan percubaan betulkan typo sebenar). Arahkan ke
			// endpoint pembetulan khusus (Task 6) sebaliknya.
			c.JSON(http.StatusConflict, gin.H{
				"error": "ahli ni dah disahkan - guna PATCH /members/:id/staff-id untuk betulkan nombor staff",
			})
			return
		}
		c.JSON(http.StatusOK, verifyStaffIDResponse{
			UserID:     target.UserID.String(),
			MemberID:   target.MemberID.String,
			VerifiedAt: formatTimeNullable(target.StaffIDVerifiedAt),
		})
		return
	}

	var override pgtype.Text
	if req.StaffID != nil {
		trimmed := strings.TrimSpace(*req.StaffID)
		if trimmed == "" || len(trimmed) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff tidak sah"})
			return
		}
		if strings.Contains(trimmed, "/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff tidak boleh mengandungi '/'"})
			return
		}
		override = pgtype.Text{String: trimmed, Valid: true}
	}

	effectiveStaffID := target.StaffID
	if override.Valid {
		effectiveStaffID = override.String
	}

	// Jangan sahkan UUID backfill sebagai nombor staff sebenar.
	// Migrasi isi staff_id = user_id::text untuk baris sedia ada;
	// pending/rejected kekal unverified. Kalau manager sahkan tanpa
	// override, UUID tu jadi staff_id "rasmi", ahli jadi fee-exempt,
	// dan approval boleh jalan. Override wajib bila staff_id masih
	// sama dengan user_id - SAMADA member_id dah ada (pending pra-
	// migrasi, format lama) atau masih NULL. JANGAN sempitkan syarat
	// ni kepada `!target.MemberID.Valid`: itu akan benarkan UUID
	// disahkan sebagai nombor staff untuk baris pending pra-migrasi.
	if !override.Valid && target.StaffID == target.UserID.String() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "staff_id ahli ni masih placeholder - sila isi nombor staff sebenar semasa sahkan",
		})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}
	defer tx.Rollback(ctx) // no-op selepas Commit berjaya, padanan setMemberStatus
	qtx := h.queries.WithTx(tx)

	var newMemberID pgtype.Text
	if !target.MemberID.Valid {
		generated, err := generateMemberID(ctx, qtx, effectiveStaffID, target.RoleKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana nombor ahli"})
			return
		}
		newMemberID = pgtype.Text{String: generated, Valid: true}
	}

	updated, err := qtx.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
		StaffID:    override,
		VerifiedBy: pgtype.UUID{Bytes: callerID, Valid: true},
		MemberID:   newMemberID,
		UserID:     targetID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Perlumbaan: permintaan lain sahkan ahli ni dulu antara bacaan kita
		// di atas dan UPDATE ni (VerifyStaffID sekat `staff_id_verified_at
		// is null` dalam WHERE) - UPDATE kita padan sifar baris sebaik
		// pemenang commit. Rollback (via defer) balikkan SEPENUHNYA
		// termasuk kenaikan sequence generateMemberID (NextSequence ialah
		// upsert jadual, bukan nextval telanjang), kemudian baca keadaan
		// SEBENAR pemenang melalui querier bukan-tx (tx kita takkan commit).
		refreshed, rErr := h.queries.GetProfileByUserID(ctx, targetID)
		if rErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
			return
		}
		c.JSON(http.StatusOK, verifyStaffIDResponse{
			UserID:     refreshed.UserID.String(),
			MemberID:   refreshed.MemberID.String,
			VerifiedAt: formatTimeNullable(refreshed.StaffIDVerifiedAt),
		})
		return
	} else if constraint, ok := uniqueViolationConstraint(err); ok {
		switch constraint {
		case "profiles_staff_id_key":
			c.JSON(http.StatusConflict, gin.H{"error": "nombor staff ini sudah digunakan"})
		case "profiles_member_id_key":
			c.JSON(http.StatusConflict, gin.H{"error": "nombor ahli yang dijana berlanggar dengan rekod sedia ada - cuba sahkan semula"})
		default:
			c.JSON(http.StatusConflict, gin.H{"error": "konflik data - cuba semula"})
		}
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}

	// Pengesahan nombor staff + penjanaan member_id ialah keputusan
	// pentadbiran (padanan setMemberStatus) - siapa sahkan, bila, dan
	// nombor ahli yang dijana mesti dapat dijawab kemudian.
	actor := auditActor(c, qtx)
	newFields := mergeAuditFields(
		map[string]any{
			"member_id":            updated.MemberID.String,
			"staff_id_verified_at": formatTime(updated.StaffIDVerifiedAt),
			"staff_id_verified_by": callerID.String(),
		},
		actorAuditFields(actor),
	)
	if override.Valid {
		newFields["staff_id"] = override.String
	}
	if err := audit.Record(ctx, qtx, audit.Entry{
		EntityType: audit.EntityStaffIDVerification,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      actor,
		Old:        map[string]any{"staff_id_verified_at": nil, "member_id": textToAny(target.MemberID)},
		New:        newFields,
	}); err != nil {
		log.Printf("audit sahkan nombor staff: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan nombor staff"})
		return
	}

	c.JSON(http.StatusOK, verifyStaffIDResponse{
		UserID:     updated.UserID.String(),
		MemberID:   updated.MemberID.String,
		VerifiedAt: formatTimeNullable(updated.StaffIDVerifiedAt),
	})
}

type correctStaffIDRequest struct {
	StaffID string `json:"staff_id"`
}

type correctStaffIDResponse struct {
	UserID  string `json:"user_id"`
	StaffID string `json:"staff_id"`
}

// CorrectStaffID - PATCH /members/:id/staff-id (Task 6). Admin/superadmin
// SAHAJA (rank >= "admin") - lebih ketat drpd VerifyStaffID (manager ke
// atas) sebab ni pembetulan nombor staff SELEPAS pengesahan pertama
// (biasanya typo), bukan pengesahan asal. Sengaja TIDAK dalam transaksi
// - satu kemas kini tunggal, tiada penjanaan member_id atau interaksi
// jadual lain untuk digabungkan (beza drpd VerifyStaffID).
//
// Membetulkan staff_id TIDAK boleh un-verify ahli - staff_id_verified_at/
// staff_id_verified_by dibiar tak disentuh (lihat query CorrectStaffID).
func (h *ProfileHandler) CorrectStaffID(c *gin.Context) {
	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isAdminUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "admin")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor staff"})
		return
	}
	if !isAdminUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma admin ke atas boleh betulkan nombor staff"})
		return
	}

	targetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh betulkan nombor staff akaun sendiri"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor staff"})
		return
	}

	// Pre-image - nilai staff_id LAMA untuk catatan audit di bawah, sekali
	// gus jadi asas semakan rank di bawah.
	target, err := h.queries.GetProfileByUserID(ctx, targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}
	if caller.RoleRank <= target.RoleRank {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh edit ahli setaraf/lebih tinggi drpd anda"})
		return
	}

	var req correctStaffIDRequest
	if !bindJSON(c, &req) {
		return
	}
	trimmed := strings.TrimSpace(req.StaffID)
	if trimmed == "" || len(trimmed) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff tidak sah"})
		return
	}
	if strings.Contains(trimmed, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff tidak boleh mengandungi '/'"})
		return
	}

	updated, err := h.queries.CorrectStaffID(ctx, sqlc.CorrectStaffIDParams{
		StaffID: trimmed,
		UserID:  targetID,
	})
	if isUniqueViolation(err) {
		c.JSON(http.StatusConflict, gin.H{"error": "nombor staff ini sudah digunakan"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor staff"})
		return
	}

	actor := auditActor(c, h.queries)
	if err := audit.Record(ctx, h.queries, audit.Entry{
		EntityType: audit.EntityStaffIDCorrection,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      actor,
		Old:        map[string]any{"staff_id": target.StaffID},
		New: mergeAuditFields(
			map[string]any{"staff_id": updated.StaffID},
			actorAuditFields(actor),
		),
	}); err != nil {
		log.Printf("audit betulkan nombor staff: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor staff"})
		return
	}

	c.JSON(http.StatusOK, correctStaffIDResponse{
		UserID:  updated.UserID.String(),
		StaffID: updated.StaffID,
	})
}

type correctMemberIDRequest struct {
	MemberID string `json:"member_id"`
}

type correctMemberIDResponse struct {
	UserID   string `json:"user_id"`
	MemberID string `json:"member_id"`
}

// CorrectMemberID - PATCH /members/:id/member-id (format nombor ahli
// baharu, Task 4). Admin/superadmin SAHAJA (rank >= "admin"), padan
// CorrectStaffID. HANYA betulkan member_id yang SUDAH wujud - query
// sekat `member_id is not null`, jadi ahli belum verify (NULL) pulang
// 409, bukan laluan pintas gate verifikasi.
func (h *ProfileHandler) CorrectMemberID(c *gin.Context) {
	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isAdminUp, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "admin")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor ahli"})
		return
	}
	if !isAdminUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma admin ke atas boleh betulkan nombor ahli"})
		return
	}

	targetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh betulkan nombor ahli akaun sendiri"})
		return
	}

	caller, err := h.queries.GetProfileByUserID(ctx, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor ahli"})
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

	var req correctMemberIDRequest
	if !bindJSON(c, &req) {
		return
	}
	trimmed := strings.TrimSpace(req.MemberID)
	if trimmed == "" || len(trimmed) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nombor ahli tidak sah"})
		return
	}

	updated, err := h.queries.CorrectMemberID(ctx, sqlc.CorrectMemberIDParams{
		MemberID: pgtype.Text{String: trimmed, Valid: true},
		UserID:   targetID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "ahli ni belum ada nombor ahli - sahkan nombor staff dulu (`POST /members/:id/verify-staff-id`)",
		})
		return
	} else if isUniqueViolation(err) {
		c.JSON(http.StatusConflict, gin.H{"error": "nombor ahli ini sudah digunakan ahli lain"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor ahli"})
		return
	}

	actor := auditActor(c, h.queries)
	if err := audit.Record(ctx, h.queries, audit.Entry{
		EntityType: audit.EntityMemberIDCorrection,
		EntityID:   targetID,
		Action:     audit.ActionUpdate,
		Actor:      actor,
		Old:        map[string]any{"member_id": target.MemberID.String},
		New: mergeAuditFields(
			map[string]any{"member_id": updated.MemberID.String},
			actorAuditFields(actor),
		),
	}); err != nil {
		log.Printf("audit betulkan nombor ahli: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor ahli"})
		return
	}

	c.JSON(http.StatusOK, correctMemberIDResponse{
		UserID:   updated.UserID.String(),
		MemberID: updated.MemberID.String,
	})
}

// CancelMemberRegistrationPayment - POST /members/:id/cancel-registration-payment.
// Admin/superadmin batalkan bil yuran pendaftaran 'pending' ahli supaya
// laluan langkau bayaran boleh digunakan (ahli lama migrasi manual).
// Semak gateway DULU - kalau dah bayar, tolak; kalau masih pending,
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
			c.JSON(http.StatusConflict, gin.H{"error": "ahli sudah bayar yuran pendaftaran - luluskan tanpa langkau bayaran"})
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

// recordCancelRegistrationPaymentAudit - jejak siapa batalkan bil pending
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
			"cancelled_by_admin":          true,
			"payment_id":                  pending.ID.String(),
		}, actorAuditFields(actor)),
	})
}

type accountDeletionRequestResponse struct {
	Status      string `json:"status"`
	RequestedAt string `json:"requested_at"`
}

// RequestAccountDeletion - POST /me/deletion-request. Keperluan Google
// Play Console: app yang sokong penciptaan akaun MESTI sediakan cara ahli
// MEMINTA pemadaman akaun + data. v1 sengaja REQUEST-sahaja - rekod
// permintaan + jejak audit, staff tindak secara MANUAL (akses DB terus)
// buat masa ni. TIADA auto-purge post/bayaran/pendaftaran aktiviti dsb -
// lihat TODO.md untuk gap ni sebagai fast-follow.
//
// Sengaja TIDAK di bawah RequireApprovedStatus - ahli pending/rejected pun
// berhak minta akaun dia dipadam.
//
// Idempoten: panggilan berulang oleh ahli sama pulangkan 200 dengan data
// permintaan yang SEDIA ADA (bukan ralat) - padanan pola
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
		// `on conflict do nothing` - dah ada permintaan sedia ada. Ambil
		// baris tu supaya response pulangkan data ASAL (bukan cuba
		// dicipta semula), dan JANGAN tulis catatan audit baharu - tiada
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

// textOrEmpty - padanan textToPtr, tapi pulangkan "" (bukan nil) bila
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
