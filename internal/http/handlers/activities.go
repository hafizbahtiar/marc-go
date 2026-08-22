package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/push"
)

type ActivityHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	push    *push.Service
}

func NewActivityHandler(pool *pgxpool.Pool, pushSvc *push.Service) *ActivityHandler {
	return &ActivityHandler{pool: pool, queries: sqlc.New(pool), push: pushSvc}
}

// notifyTimeout — had bagi SATU pusingan fan-out notifikasi di latar
// belakang. Tanpa had, satu panggilan OneSignal yang tersekat menahan
// goroutine itu selama-lamanya.
const notifyTimeout = 2 * time.Minute

// notifyTarget — seorang penerima berserta pautan yang KHUSUS kepadanya.
//
// certificate_ready satu-satunya jenis yang pautannya berbeza per penerima:
// setiap orang ada baris sijilnya sendiri. Untuk jenis lain CertificateID
// kekal kosong.
type notifyTarget struct {
	UserID        uuid.UUID
	CertificateID pgtype.UUID
}

// notification — satu peristiwa fan-out.
//
// Struct dan bukan senarai parameter: selepas deep-link (activity_id,
// certificate_id) dan NotifyActor ditambah, versi kedudukan akan jadi lapan
// argumen dengan dua uuid dan satu bool bersebelahan — tepat bentuk yang
// senyap salah bila ditukar tempat.
type notification struct {
	Targets  []notifyTarget
	ActorID  uuid.UUID
	Type     string
	Title    string
	Message  string
	Activity pgtype.UUID
	// NotifyActor: hantar juga kepada pelaku sendiri. Lalai false —
	// penerbit/pembatal aktiviti tidak perlu diberitahu tentang perbuatannya
	// sendiri. certificate_ready sebaliknya: ia mengenai artifak penerima,
	// dan pengurus yang turut menyertai aktiviti berhak menerima sijilnya
	// sendiri.
	NotifyActor bool
}

// notifyTargets — bentuk sasaran bagi jenis notifikasi yang pautannya sama
// untuk semua penerima (activity_published, activity_cancelled).
func notifyTargets(recipients []uuid.UUID) []notifyTarget {
	out := make([]notifyTarget, 0, len(recipients))
	for _, uid := range recipients {
		out = append(out, notifyTarget{UserID: uid})
	}
	return out
}

// notifyMembers rekod baris notifikasi DAN hantar push kepada setiap
// penerima. Ia TIDAK memulangkan ralat, dengan sengaja.
//
// Kontrak pemanggil: panggil hanya SELEPAS transaksi komit. Aktiviti yang
// sudah diterbitkan tak boleh digulung semula oleh OneSignal yang gagal,
// jadi setiap kegagalan dilog dan tiada satu pun naik ke handler. Dilog,
// bukan dibuang: push yang lenyap tanpa jejak tak dapat dibezakan daripada
// push yang tiada siapa baca.
//
// Bentuk fan-out: SATU goroutine untuk keseluruhan senarai, gelung
// berjujukan di dalamnya — bukan satu goroutine per penerima. Setiap
// penerima ialah satu query device_token + satu panggilan HTTP, jadi
// menjalankannya dalam ctx permintaan akan menambah latensi mengikut saiz
// keahlian; melancarkan satu goroutine setiap seorang pula ialah fan-out
// tak berhad ke OneSignal. Satu goroutine per peristiwa (terbit/batal/
// terbit sijil — semuanya tindakan pengurusan yang jarang) menyelesaikan
// kedua-duanya.
//
// ctx permintaan SENGAJA tidak digunakan: ia dibatalkan sebaik respons
// ditulis, yang akan memotong fan-out di tengah jalan.
func notifyMembers(queries *sqlc.Queries, pushSvc *push.Service, n notification) {
	if len(n.Targets) == 0 || pushSvc == nil {
		// pushSvc nil hanya berlaku kalau handler dibina tanpa servis push.
		// Diguard di sini kerana kegagalan itu akan jadi panic DALAM
		// goroutine latar — iaitu proses mati, bukan satu permintaan gagal.
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()

		for _, target := range n.Targets {
			// Tiada notifikasi kepada diri sendiri — sama seperti
			// notifyOwner pada laluan post. Kecuali NotifyActor.
			if target.UserID == n.ActorID && !n.NotifyActor {
				continue
			}
			if _, err := queries.CreateNotification(ctx, sqlc.CreateNotificationParams{
				RecipientID: target.UserID,
				ActorID:     n.ActorID,
				Type:        n.Type,
				// Notifikasi aktiviti tiada post/comment berkaitan; kedua-dua
				// lajur ini nullable sejak 20260807120100. Deep-linknya
				// melalui activity_id/certificate_id (20260810100700).
				PostID:        pgtype.UUID{},
				CommentID:     pgtype.UUID{},
				ActivityID:    n.Activity,
				CertificateID: target.CertificateID,
			}); err != nil {
				log.Printf("notifikasi %s untuk %s: %v", n.Type, target.UserID, err)
			}
			if err := pushSvc.NotifyUser(ctx, target.UserID, n.Title, n.Message); err != nil {
				log.Printf("push %s kepada %s: %v", n.Type, target.UserID, err)
			}
		}
	}()
}

const (
	statusDraft     = "draft"
	statusPublished = "published"
	statusCancelled = "cancelled"
	statusCompleted = "completed"
)

// defaultListStatuses — draf sengaja TIADA di sini: aktiviti yang belum
// diterbitkan bukan untuk mata ahli.
var defaultListStatuses = []string{statusPublished, statusCancelled, statusCompleted}

var validStatuses = map[string]bool{
	statusDraft: true, statusPublished: true, statusCancelled: true, statusCompleted: true,
}

var (
	errNoSessions           = errors.New("aktiviti perlu sekurang-kurangnya satu sesi")
	errSessionTimes         = errors.New("masa tamat sesi mesti selepas masa mula")
	errDuplicateSeq         = errors.New("nombor urutan sesi mesti unik")
	errSessionHasAttendance = errors.New("sesi yang sudah ada kehadiran tidak boleh diganti")
	errActivityNotFound     = errors.New("aktiviti tidak dijumpai")
)

func isSessionInputError(err error) bool {
	return errors.Is(err, errNoSessions) ||
		errors.Is(err, errSessionTimes) ||
		errors.Is(err, errDuplicateSeq)
}

type sessionInput struct {
	Seq      int       `json:"seq" binding:"required,min=1"`
	Title    string    `json:"title" binding:"max=200"`
	StartsAt time.Time `json:"starts_at" binding:"required"`
	EndsAt   time.Time `json:"ends_at" binding:"required"`
}

// validateSessions — semakan yang tak perlukan DB, dijalankan sebelum
// sebarang transaksi dibuka.
//
// Set kosong ditolak DI SINI dan bukan diserahkan kepada
// RecomputeActivityWindow: query itu ada guard `s.min_start is not null`,
// jadi set kosong akan meninggalkan starts_at/ends_at lama tanpa ralat —
// invarian yang pecah secara senyap.
func validateSessions(sessions []sessionInput) error {
	if len(sessions) == 0 {
		return errNoSessions
	}
	seen := make(map[int]bool, len(sessions))
	for _, s := range sessions {
		if !s.EndsAt.After(s.StartsAt) {
			return errSessionTimes
		}
		if seen[s.Seq] {
			return errDuplicateSeq
		}
		seen[s.Seq] = true
	}
	return nil
}

func sessionsSnapshot(sessions []sqlc.ActivitySession) []map[string]any {
	out := make([]map[string]any, len(sessions))
	for i, s := range sessions {
		out[i] = map[string]any{
			"seq": s.Seq, "title": s.Title,
			"starts_at": s.StartsAt.Time.UTC().Format(time.RFC3339),
			"ends_at":   s.EndsAt.Time.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// replaceSessionsTx menggantikan KESELURUHAN set sesi dalam satu transaksi,
// kemudian mengira semula tetingkap aktiviti.
//
// Ganti-semua, bukan CRUD per-sesi: invarian starts_at/ends_at perlu dikira
// semula setiap kali set berubah, dan satu laluan kod bermakna satu tempat
// yang boleh melanggarnya.
func replaceSessionsTx(ctx context.Context, pool *pgxpool.Pool, activityID uuid.UUID, sessions []sessionInput) error {
	return replaceSessionsAudited(ctx, pool, activityID, sessions, nil)
}

// replaceSessionsAudited — sama, tapi turut menulis catatan audit dalam
// transaksi yang sama bila `actor` diberi. Laluan HTTP sentiasa memberi
// actor; ujian memanggil replaceSessionsTx tanpa satu.
func replaceSessionsAudited(
	ctx context.Context,
	pool *pgxpool.Pool,
	activityID uuid.UUID,
	sessions []sessionInput,
	actor *audit.Actor,
) error {
	if err := validateSessions(sessions); err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	// Kunci baris aktiviti DAHULU. activity_attendances.session_id ialah
	// `on delete cascade`, jadi DeleteActivitySessions di bawah MEMUSNAHKAN
	// kehadiran, bukan gagal kerananya. Tanpa kunci ini, di bawah READ
	// COMMITTED satu check-in yang commit antara kiraan dan padam akan
	// terhapus senyap — betul-betul bukti yang komen di bawah kata mesti
	// dilindungi. Laluan check-in mengambil kunci yang sama.
	if _, err := q.LockActivityForRegistration(ctx, activityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errActivityNotFound
		}
		return err
	}

	// Sesi yang sudah ada kehadiran tak boleh dibuang — kehadiran itu bukti
	// yang menyokong sijil.
	withAttendance, err := q.CountSessionsWithAttendance(ctx, activityID)
	if err != nil {
		return err
	}
	if withAttendance > 0 {
		return errSessionHasAttendance
	}

	var before []sqlc.ActivitySession
	if actor != nil {
		if before, err = q.ListActivitySessions(ctx, activityID); err != nil {
			return err
		}
	}

	if err := q.DeleteActivitySessions(ctx, activityID); err != nil {
		return err
	}
	for _, s := range sessions {
		if _, err := q.CreateActivitySession(ctx, sqlc.CreateActivitySessionParams{
			ActivityID: activityID,
			Seq:        int32(s.Seq),
			Title:      s.Title,
			StartsAt:   pgTimestamptz(s.StartsAt),
			EndsAt:     pgTimestamptz(s.EndsAt),
		}); err != nil {
			return err
		}
	}

	if err := q.RecomputeActivityWindow(ctx, activityID); err != nil {
		return err
	}

	if actor != nil {
		after, err := q.ListActivitySessions(ctx, activityID)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, q, audit.Entry{
			EntityType: audit.EntityActivity,
			EntityID:   activityID,
			Action:     audit.ActionUpdate,
			Actor:      *actor,
			Old:        map[string]any{"sessions": sessionsSnapshot(before)},
			New:        map[string]any{"sessions": sessionsSnapshot(after)},
		}); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// requireManagement — semakan management dibuat DALAM handler, ikut corak
// sedia ada (lihat audit.go, profile.go). Tiada middleware RequireManagement
// dalam repo ini; jangan cipta satu.
func (h *ActivityHandler) requireManagement(c *gin.Context) bool {
	ok, err := authz.IsManagement(c.Request.Context(), h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk pengurusan sahaja"})
		return false
	}
	return true
}

// managerRoleKey — siling "manager ke atas" utk kawalan lebih ketat drpd
// IsManagement (yang termasuk supervisor). Kategori aktiviti ialah
// infrastruktur dikongsi semua aktiviti, bukan tindakan pengurusan harian.
const managerRoleKey = "manager"

func (h *ActivityHandler) requireManagerOrAbove(c *gin.Context) bool {
	ok, err := authz.IsAtLeastRole(c.Request.Context(), h.queries, middleware.UserID(c), managerRoleKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk manager ke atas sahaja"})
		return false
	}
	return true
}

// ---- Baca ----

// ListCategories — lalai cuma pulangkan kategori AKTIF (utk borang cipta
// aktiviti, semua ahli approved). `?all=true` pulangkan semua termasuk
// tidak aktif, utk skrin pengurusan CRUD — dikawal manager ke atas sahaja,
// sama corak dengan status=draft dalam List() di bawah.
func (h *ActivityHandler) ListCategories(c *gin.Context) {
	ctx := c.Request.Context()
	includeInactive := c.Query("all") == "true"
	if includeInactive && !h.requireManagerOrAbove(c) {
		return
	}

	var (
		categories []sqlc.ActivityCategory
		err        error
	)
	if includeInactive {
		categories, err = h.queries.ListAllActivityCategories(ctx)
	} else {
		categories, err = h.queries.ListActivityCategories(ctx)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat kategori"})
		return
	}
	if categories == nil {
		categories = []sqlc.ActivityCategory{}
	}
	c.JSON(http.StatusOK, gin.H{"categories": categories})
}

// categoryKeyPattern — huruf kecil, nombor, garis bawah sahaja, mesti
// bermula huruf. Padanan gaya `key` role/kategori sedia ada (cth
// 'badminton', 'bola_tampar').
var categoryKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,49}$`)

type createCategoryRequest struct {
	Key       string `json:"key" binding:"required"`
	Name      string `json:"name" binding:"required"`
	SortOrder int32  `json:"sort_order"`
}

func categorySnapshot(cat sqlc.ActivityCategory) map[string]any {
	return map[string]any{
		"key":        cat.Key,
		"name":       cat.Name,
		"sort_order": cat.SortOrder,
		"is_active":  cat.IsActive,
	}
}

func (h *ActivityHandler) CreateCategory(c *gin.Context) {
	if !h.requireManagerOrAbove(c) {
		return
	}

	var req createCategoryRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	req.Name = strings.TrimSpace(req.Name)
	if !categoryKeyPattern.MatchString(req.Key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kunci kategori mesti huruf kecil, nombor dan garis bawah sahaja"})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nama kategori diperlukan"})
		return
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta kategori"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	category, err := q.CreateActivityCategory(ctx, sqlc.CreateActivityCategoryParams{
		Key:       req.Key,
		Name:      req.Name,
		SortOrder: req.SortOrder,
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "kunci kategori sudah wujud"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta kategori"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivityCategory,
		EntityID:   category.ID,
		Action:     audit.ActionCreate,
		Actor:      auditActor(c, q),
		New:        categorySnapshot(category),
	}); err != nil {
		log.Printf("audit cipta kategori aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta kategori"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta kategori"})
		return
	}

	c.JSON(http.StatusCreated, category)
}

type updateCategoryRequest struct {
	Name      *string `json:"name"`
	SortOrder *int32  `json:"sort_order"`
	IsActive  *bool   `json:"is_active"`
}

func (h *ActivityHandler) UpdateCategory(c *gin.Context) {
	if !h.requireManagerOrAbove(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req updateCategoryRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nama kategori tidak boleh kosong"})
			return
		}
		req.Name = &trimmed
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini kategori"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	before, err := q.GetActivityCategoryByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "kategori tidak dijumpai"})
		return
	}

	name := pgtype.Text{}
	if req.Name != nil {
		name = pgtype.Text{String: *req.Name, Valid: true}
	}
	sortOrder := pgtype.Int4{}
	if req.SortOrder != nil {
		sortOrder = pgtype.Int4{Int32: *req.SortOrder, Valid: true}
	}
	isActive := pgtype.Bool{}
	if req.IsActive != nil {
		isActive = pgtype.Bool{Bool: *req.IsActive, Valid: true}
	}

	updated, err := q.UpdateActivityCategory(ctx, sqlc.UpdateActivityCategoryParams{
		ID:        id,
		Name:      name,
		SortOrder: sortOrder,
		IsActive:  isActive,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini kategori"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivityCategory,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old:        categorySnapshot(before),
		New:        categorySnapshot(updated),
	}); err != nil {
		log.Printf("audit kemas kini kategori aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini kategori"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini kategori"})
		return
	}

	c.JSON(http.StatusOK, updated)
}

func (h *ActivityHandler) List(c *gin.Context) {
	ctx := c.Request.Context()

	statuses := defaultListStatuses
	if raw := c.Query("status"); raw != "" {
		statuses = strings.Split(raw, ",")
		for _, s := range statuses {
			if !validStatuses[s] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "status tidak sah"})
				return
			}
			if s == statusDraft {
				// Draf hanya untuk pengurusan — jangan dedahkan aktiviti
				// yang belum diterbitkan kepada ahli biasa.
				if !h.requireManagement(c) {
					return
				}
			}
		}
	}

	var categoryID pgtype.UUID
	if raw := c.Query("category_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kategori tidak sah"})
			return
		}
		categoryID = pgUUID(id)
	}

	upcoming := true
	if raw := c.Query("upcoming"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parameter upcoming tidak sah"})
			return
		}
		upcoming = v
	}

	// Sengaja 400, bukan jatuh senyap ke lalai: setiap parameter lain di
	// sini menolak input buruk, dan "limit=500 diamkan jadi 20" ialah jenis
	// perbezaan yang klien tak dapat lihat.
	limit := defaultPageLimit
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parameter limit mesti antara 1 dan 100"})
			return
		}
		limit = n
	}

	// Cursor ialah SATU string legap yang membawa (starts_at, id) sekali gus.
	// Sengaja bukan dua parameter berasingan: predikat perbandingan baris
	// dalam ListActivities jadi NULL kalau salah satu hilang, dan query
	// pulangkan sifar baris — jalan mati yang nampak macam "habis senarai".
	var cursorStartsAt pgtype.Timestamptz
	var cursorID pgtype.UUID
	if v := c.Query("cursor"); v != "" {
		t, id, err := decodeCursor(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cursor tidak sah"})
			return
		}
		cursorStartsAt = pgtype.Timestamptz{Time: t, Valid: true}
		cursorID = pgUUID(id)
		if !cursorStartsAt.Valid || !cursorID.Valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cursor tidak sah"})
			return
		}
	}

	rows, err := h.queries.ListActivities(ctx, sqlc.ListActivitiesParams{
		Statuses:       statuses,
		CategoryID:     categoryID,
		Upcoming:       upcoming,
		CursorStartsAt: cursorStartsAt,
		CursorID:       cursorID,
		RowLimit:       int32(limit),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai aktiviti"})
		return
	}
	if rows == nil {
		rows = []sqlc.ListActivitiesRow{}
	}

	var nextCursor *string
	if len(rows) == limit {
		last := rows[len(rows)-1]
		s := encodeCursor(last.StartsAt.Time, last.ID)
		nextCursor = &s
	}

	c.JSON(http.StatusOK, gin.H{"activities": rows, "next_cursor": nextCursor})
}

// activityDetailResponse — baris aktiviti diratakan pada aras atas, plus
// tiga medan yang klien bergantung padanya.
type activityDetailResponse struct {
	sqlc.GetActivityByIDRow
	Sessions          []sqlc.ActivitySession `json:"sessions"`
	RegistrationCount int64                  `json:"registration_count"`
	IsRegistered      bool                   `json:"is_registered"`
}

func (h *ActivityHandler) Get(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	row, err := h.queries.GetActivityByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	}
	if row.Status == statusDraft {
		isManagement, err := authz.IsManagement(ctx, h.queries, middleware.UserID(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
			return
		}
		if !isManagement {
			c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
			return
		}
	}

	detail, err := h.buildActivityDetail(ctx, row, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat aktiviti"})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// buildActivityDetail kumpulkan tiga medan tambahan yang klien bergantung
// padanya: senarai sesi, kiraan pendaftaran, dan sama ada PEMANGGIL sudah
// mendaftar.
func (h *ActivityHandler) buildActivityDetail(
	ctx context.Context, row sqlc.GetActivityByIDRow, viewerID uuid.UUID,
) (activityDetailResponse, error) {
	sessions, err := h.queries.ListActivitySessions(ctx, row.ID)
	if err != nil {
		return activityDetailResponse{}, err
	}
	if sessions == nil {
		sessions = []sqlc.ActivitySession{}
	}

	count, err := h.queries.CountActiveRegistrations(ctx, row.ID)
	if err != nil {
		return activityDetailResponse{}, err
	}

	// Tiada baris = belum daftar. Itu jawapan, bukan ralat.
	isRegistered := true
	if _, err := h.queries.GetRegistrationByActivityAndUser(ctx, sqlc.GetRegistrationByActivityAndUserParams{
		ActivityID: row.ID, UserID: viewerID,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return activityDetailResponse{}, err
		}
		isRegistered = false
	}

	return activityDetailResponse{
		GetActivityByIDRow: row,
		Sessions:           sessions,
		RegistrationCount:  count,
		IsRegistered:       isRegistered,
	}, nil
}

// ---- Tulis (pengurusan sahaja) ----

type activityRequest struct {
	CategoryID             uuid.UUID `json:"category_id" binding:"required"`
	Title                  string    `json:"title" binding:"required,max=200"`
	Description            string    `json:"description" binding:"max=2000"`
	LocationName           string    `json:"location_name" binding:"required,max=300"`
	LocationAddress        string    `json:"location_address" binding:"max=500"`
	RegistrationOpensAt    time.Time `json:"registration_opens_at"`
	RegistrationClosesAt   time.Time `json:"registration_closes_at" binding:"required"`
	Capacity               *int32    `json:"capacity"`
	FeeCents               int32     `json:"fee_cents"`
	AttendanceThresholdPct int16     `json:"attendance_threshold_pct"`
}

// validateFeeCents — aktiviti berbayar kini disokong (ToyyibPay wired,
// lihat ActivityRegistrationPaymentHandler). Cuma nilai negatif yang tak
// masuk akal ditolak di sini.
func validateFeeCents(fee int32) error {
	if fee < 0 {
		return errors.New("yuran tidak boleh negatif")
	}
	return nil
}

// normalise isi lalai dan sahkan julat yang DB akan tolak dengan mesej
// Postgres mentah kalau dibiarkan lalu.
func (r *activityRequest) normalise() error {
	if r.AttendanceThresholdPct == 0 {
		r.AttendanceThresholdPct = 100
	}
	if r.AttendanceThresholdPct < 1 || r.AttendanceThresholdPct > 100 {
		return errors.New("ambang kehadiran mesti antara 1 dan 100 peratus")
	}
	if err := validateFeeCents(r.FeeCents); err != nil {
		return err
	}
	if r.Capacity != nil && *r.Capacity <= 0 {
		return errors.New("kapasiti mesti lebih daripada sifar")
	}
	return nil
}

func (r *activityRequest) capacity() pgtype.Int4 {
	if r.Capacity == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *r.Capacity, Valid: true}
}

// optional[T] membezakan TIGA keadaan yang PATCH perlukan dan yang `*T`
// sendiri tak boleh bawa:
//
//	medan tiada langsung dalam badan → Set=false        → kekalkan nilai sedia ada
//	medan hadir dengan nilai null    → Set=true, Val=nil → kosongkan (lajur nullable)
//	medan hadir dengan nilai         → Set=true, Val≠nil → tetapkan
//
// Tanpa ini, PATCH yang membawa {"title": "..."} sahaja akan memadam
// description, capacity dan fee_cents — dan jejak audit akan merekodkan
// pemusnahan itu sebagai perubahan yang disengajakan.
type optional[T any] struct {
	Set bool
	Val *T
}

// UnmarshalJSON dipanggil walaupun untuk literal `null` (dijamin oleh
// encoding/json bagi jenis yang melaksanakan Unmarshaler), yang itulah
// caranya "hadir tetapi null" dapat dibezakan daripada "tiada".
func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Val = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Val = &v
	return nil
}

// value untuk lajur NOT NULL: null eksplisit ialah ralat, bukan "kosongkan".
func (o optional[T]) value(field string, current T) (T, error) {
	if !o.Set {
		return current, nil
	}
	if o.Val == nil {
		var zero T
		return zero, errors.New("medan " + field + " tidak boleh null")
	}
	return *o.Val, nil
}

type updateActivityRequest struct {
	CategoryID             optional[uuid.UUID] `json:"category_id"`
	Title                  optional[string]    `json:"title"`
	Description            optional[string]    `json:"description"`
	LocationName           optional[string]    `json:"location_name"`
	LocationAddress        optional[string]    `json:"location_address"`
	RegistrationOpensAt    optional[time.Time] `json:"registration_opens_at"`
	RegistrationClosesAt   optional[time.Time] `json:"registration_closes_at"`
	Capacity               optional[int32]     `json:"capacity"`
	FeeCents               optional[int32]     `json:"fee_cents"`
	AttendanceThresholdPct optional[int16]     `json:"attendance_threshold_pct"`
}

// merge bina parameter UpdateActivity daripada baris SEDIA ADA (dibaca di
// bawah `for update`), dengan hanya medan yang benar-benar dihantar
// ditindih. UpdateActivity menulis kesebelas-belas lajur tanpa syarat, jadi
// gabungan mesti berlaku di sini.
func (r *updateActivityRequest) merge(before sqlc.Activity) (sqlc.UpdateActivityParams, error) {
	out := sqlc.UpdateActivityParams{
		ID:                   before.ID,
		RegistrationOpensAt:  before.RegistrationOpensAt,
		RegistrationClosesAt: before.RegistrationClosesAt,
		Capacity:             before.Capacity,
	}

	var err error
	if out.CategoryID, err = r.CategoryID.value("category_id", before.CategoryID); err != nil {
		return out, err
	}
	if out.Title, err = r.Title.value("title", before.Title); err != nil {
		return out, err
	}
	if out.Description, err = r.Description.value("description", before.Description); err != nil {
		return out, err
	}
	if out.LocationName, err = r.LocationName.value("location_name", before.LocationName); err != nil {
		return out, err
	}
	if out.LocationAddress, err = r.LocationAddress.value("location_address", before.LocationAddress); err != nil {
		return out, err
	}
	if out.FeeCents, err = r.FeeCents.value("fee_cents", before.FeeCents); err != nil {
		return out, err
	}
	if out.AttendanceThresholdPct, err = r.AttendanceThresholdPct.value(
		"attendance_threshold_pct", before.AttendanceThresholdPct); err != nil {
		return out, err
	}

	if r.RegistrationClosesAt.Set {
		if r.RegistrationClosesAt.Val == nil {
			return out, errors.New("medan registration_closes_at tidak boleh null")
		}
		out.RegistrationClosesAt = pgTimestamptz(*r.RegistrationClosesAt.Val)
	}

	// Dua lajur nullable: null eksplisit BERMAKNA sesuatu di sini —
	// "buang tarikh buka pendaftaran" dan "tiada had kapasiti".
	if r.RegistrationOpensAt.Set {
		if r.RegistrationOpensAt.Val == nil {
			out.RegistrationOpensAt = pgtype.Timestamptz{}
		} else {
			out.RegistrationOpensAt = pgTimestamptz(*r.RegistrationOpensAt.Val)
		}
	}
	if r.Capacity.Set {
		if r.Capacity.Val == nil {
			out.Capacity = pgtype.Int4{}
		} else {
			out.Capacity = pgtype.Int4{Int32: *r.Capacity.Val, Valid: true}
		}
	}

	// title dan location_name secara konsep WAJIB, tapi kedua-duanya cuma
	// `text not null` tanpa CHECK panjang — tiada sandaran di DB, tak
	// seperti capacity/fee_cents/attendance_threshold_pct. Tag
	// binding:"required" pada struct tak boleh menggantikannya di sini:
	// `required` juga menolak medan yang TIADA, sedangkan tiada bermakna
	// "kekalkan". Jadi yang ditolak hanyalah rentetan kosong yang dihantar
	// secara eksplisit; ruang kosong dipangkas dahulu supaya " " tak lolos.
	if r.Title.Set && strings.TrimSpace(out.Title) == "" {
		return out, errors.New("medan title tidak boleh kosong")
	}
	if r.LocationName.Set && strings.TrimSpace(out.LocationName) == "" {
		return out, errors.New("medan location_name tidak boleh kosong")
	}

	if out.AttendanceThresholdPct < 1 || out.AttendanceThresholdPct > 100 {
		return out, errors.New("ambang kehadiran mesti antara 1 dan 100 peratus")
	}
	// Yuran hanya disemak bila ia benar-benar DIHANTAR. Menyemak nilai
	// hasil gabungan akan mengunci sepenuhnya mana-mana baris yang sudah
	// membawa yuran bukan sifar (baris warisan/benih): setiap PATCH ke
	// atasnya — termasuk PATCH yang cuba menetapkannya semula kepada 0 —
	// akan ditolak. Menyemak medan yang dihantar sahaja menutup satu-satunya
	// laluan yang boleh MENCIPTA yuran, sambil membiarkan laluan
	// pembetulan (`{"fee_cents": 0}`) terbuka.
	if r.FeeCents.Set {
		if err := validateFeeCents(out.FeeCents); err != nil {
			return out, err
		}
	} else if out.FeeCents < 0 {
		return out, errors.New("yuran tidak boleh negatif")
	}
	if out.Capacity.Valid && out.Capacity.Int32 <= 0 {
		return out, errors.New("kapasiti mesti lebih daripada sifar")
	}
	return out, nil
}

func activitySnapshot(a sqlc.Activity) map[string]any {
	snap := map[string]any{
		"category_id":              a.CategoryID.String(),
		"title":                    a.Title,
		"description":              a.Description,
		"location_name":            a.LocationName,
		"location_address":         a.LocationAddress,
		"starts_at":                a.StartsAt.Time.UTC().Format(time.RFC3339),
		"ends_at":                  a.EndsAt.Time.UTC().Format(time.RFC3339),
		"registration_closes_at":   a.RegistrationClosesAt.Time.UTC().Format(time.RFC3339),
		"fee_cents":                a.FeeCents,
		"attendance_threshold_pct": a.AttendanceThresholdPct,
		"status":                   a.Status,
	}
	if a.RegistrationOpensAt.Valid {
		snap["registration_opens_at"] = a.RegistrationOpensAt.Time.UTC().Format(time.RFC3339)
	}
	if a.Capacity.Valid {
		snap["capacity"] = a.Capacity.Int32
	}
	if a.CancelledReason.Valid {
		snap["cancelled_reason"] = a.CancelledReason.String
	}
	return snap
}

type createActivityRequest struct {
	activityRequest
	Sessions []sessionInput `json:"sessions" binding:"required,min=1,dive"`
}

func (h *ActivityHandler) Create(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}

	var req createActivityRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := req.normalise(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateSessions(req.Sessions); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	activity, err := q.CreateActivity(ctx, sqlc.CreateActivityParams{
		CategoryID:      req.CategoryID,
		Title:           req.Title,
		Description:     req.Description,
		LocationName:    req.LocationName,
		LocationAddress: req.LocationAddress,
		// Nilai sementara: lajur ini NOT NULL, jadi insert perlu sesuatu.
		// RecomputeActivityWindow di bawah yang menetapkan nilai sebenar —
		// ia kekal SATU-SATUNYA penulis invarian ini.
		StartsAt:               pgTimestamptz(req.Sessions[0].StartsAt),
		EndsAt:                 pgTimestamptz(req.Sessions[0].EndsAt),
		RegistrationOpensAt:    pgTimestamptz(req.RegistrationOpensAt),
		RegistrationClosesAt:   pgTimestamptz(req.RegistrationClosesAt),
		Capacity:               req.capacity(),
		FeeCents:               req.FeeCents,
		AttendanceThresholdPct: req.AttendanceThresholdPct,
		CreatedBy:              pgUUID(middleware.UserID(c)),
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kategori tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}

	for _, s := range req.Sessions {
		if _, err := q.CreateActivitySession(ctx, sqlc.CreateActivitySessionParams{
			ActivityID: activity.ID,
			Seq:        int32(s.Seq),
			Title:      s.Title,
			StartsAt:   pgTimestamptz(s.StartsAt),
			EndsAt:     pgTimestamptz(s.EndsAt),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta sesi aktiviti"})
			return
		}
	}

	if err := q.RecomputeActivityWindow(ctx, activity.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}
	// Baca semula supaya snapshot audit membawa tetingkap sebenar, bukan
	// nilai sementara yang di-insert di atas.
	created, err := q.GetActivityByID(ctx, activity.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}
	activity.StartsAt, activity.EndsAt = created.StartsAt, created.EndsAt

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivity,
		EntityID:   activity.ID,
		Action:     audit.ActionCreate,
		Actor:      auditActor(c, q),
		New:        activitySnapshot(activity),
	}); err != nil {
		log.Printf("audit cipta aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta aktiviti"})
		return
	}

	h.respondActivity(c, http.StatusCreated, activity.ID)
}

func (h *ActivityHandler) Update(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req updateActivityRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini aktiviti"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	// Baca di bawah `for update` DAHULU: baris ini ialah asas gabungan
	// PATCH, jadi ia tak boleh berubah antara dibaca dan ditulis.
	before, err := q.LockActivityForRegistration(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	}

	params, err := req.merge(before)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// optional[string] bypasses gin's `binding` validator entirely (custom
	// UnmarshalJSON, no struct tags for the validator to act on), so the
	// same length limits enforced on POST (activityRequest) are checked
	// manually here on the merged output. MESTI kira RUNE (utf8.RuneCount),
	// bukan bait (len()) — go-playground/validator punya `max` pada POST
	// kira rune. Title 200 aksara Melayu/emoji/CJK boleh sampai 800 bait;
	// kalau semakan ni guna len() bait, title yang LULUS di POST akan
	// GAGAL di sini pada SETIAP PATCH akan datang (termasuk PATCH yang tak
	// sentuh title — `merge` salin balik nilai lama), kunci baris tu
	// kekal tak boleh di-PATCH selama-lamanya.
	if utf8.RuneCountInString(params.Title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tajuk terlalu panjang (maksimum 200 aksara)"})
		return
	}
	if utf8.RuneCountInString(params.LocationName) > 300 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nama lokasi terlalu panjang (maksimum 300 aksara)"})
		return
	}
	if utf8.RuneCountInString(params.Description) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keterangan terlalu panjang (maksimum 2000 aksara)"})
		return
	}
	if utf8.RuneCountInString(params.LocationAddress) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alamat lokasi terlalu panjang (maksimum 500 aksara)"})
		return
	}

	updated, err := q.UpdateActivity(ctx, params)
	if err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kategori tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini aktiviti"})
		return
	}

	// Old/New penuh — audit.Diff yang kira deltanya.
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivity,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old:        activitySnapshot(before),
		New:        activitySnapshot(updated),
	}); err != nil {
		log.Printf("audit kemas kini aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini aktiviti"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini aktiviti"})
		return
	}

	h.respondActivity(c, http.StatusOK, id)
}

func (h *ActivityHandler) Publish(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan aktiviti"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	before, err := q.LockActivityForRegistration(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	}
	if before.Status != statusDraft {
		c.JSON(http.StatusConflict, gin.H{"error": "hanya aktiviti draf boleh diterbitkan"})
		return
	}

	sessionCount, err := q.CountActivitySessions(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan aktiviti"})
		return
	}
	if sessionCount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNoSessions.Error()})
		return
	}

	updated, err := q.SetActivityStatus(ctx, sqlc.SetActivityStatusParams{
		ID: id, Status: statusPublished, CancelledReason: before.CancelledReason,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan aktiviti"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivity,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old:        map[string]any{"status": before.Status},
		New:        map[string]any{"status": updated.Status},
	}); err != nil {
		log.Printf("audit terbit aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan aktiviti"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan aktiviti"})
		return
	}

	// SELEPAS komit. Senarai penerima dibaca di sini (di luar transaksi)
	// dan bukan di dalam goroutine, supaya kegagalan bacaan yang biasa
	// masih dilog dengan konteks handler ini.
	recipients, err := h.queries.ListApprovedUserIDs(ctx)
	if err != nil {
		log.Printf("push aktiviti %s: senarai ahli: %v", id, err)
	} else {
		notifyMembers(h.queries, h.push, notification{
			Targets:  notifyTargets(recipients),
			ActorID:  middleware.UserID(c),
			Type:     "activity_published",
			Title:    "Aktiviti Baharu",
			Message:  updated.Title,
			Activity: pgUUID(id),
		})
	}

	h.respondActivity(c, http.StatusOK, id)
}

func (h *ActivityHandler) Cancel(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Reason string `json:"reason" binding:"required,max=500"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sebab pembatalan diperlukan"})
		return
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batal aktiviti"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	before, err := q.LockActivityForRegistration(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	}
	if before.Status == statusCancelled {
		c.JSON(http.StatusConflict, gin.H{"error": "aktiviti ini sudah dibatalkan"})
		return
	}

	updated, err := q.SetActivityStatus(ctx, sqlc.SetActivityStatusParams{
		ID: id, Status: statusCancelled, CancelledReason: pgText(strings.TrimSpace(req.Reason)),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batal aktiviti"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityActivity,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, q),
		Old:        map[string]any{"status": before.Status, "cancelled_reason": before.CancelledReason.String},
		New:        map[string]any{"status": updated.Status, "cancelled_reason": updated.CancelledReason.String},
	}); err != nil {
		log.Printf("audit batal aktiviti: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batal aktiviti"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal batal aktiviti"})
		return
	}

	// SELEPAS komit, dan hanya kepada yang BERDAFTAR — pembatalan ialah
	// berita untuk orang yang merancang hadir, bukan siaran seluruh kelab.
	// ListRegistrationsByActivity sudah menapis status 'cancelled'.
	regs, err := h.queries.ListRegistrationsByActivity(ctx, id)
	if err != nil {
		log.Printf("push batal aktiviti %s: senarai pendaftaran: %v", id, err)
	} else {
		recipients := make([]uuid.UUID, 0, len(regs))
		for _, r := range regs {
			recipients = append(recipients, r.UserID)
		}
		notifyMembers(h.queries, h.push, notification{
			Targets:  notifyTargets(recipients),
			ActorID:  middleware.UserID(c),
			Type:     "activity_cancelled",
			Title:    "Aktiviti Dibatalkan",
			Message:  updated.Title + " telah dibatalkan: " + updated.CancelledReason.String,
			Activity: pgUUID(id),
		})
	}

	h.respondActivity(c, http.StatusOK, id)
}

func (h *ActivityHandler) ReplaceSessions(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Sessions []sessionInput `json:"sessions" binding:"required,min=1,dive"`
	}
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	if _, err := h.queries.GetActivityByID(ctx, activityID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	}

	actor := auditActor(c, h.queries)
	err := replaceSessionsAudited(ctx, h.pool, activityID, req.Sessions, &actor)
	switch {
	case errors.Is(err, errActivityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": errActivityNotFound.Error()})
		return
	case errors.Is(err, errSessionHasAttendance):
		c.JSON(http.StatusConflict, gin.H{"error": errSessionHasAttendance.Error()})
		return
	case isSessionInputError(err):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		log.Printf("ganti sesi aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini sesi"})
		return
	}

	sessions, err := h.queries.ListActivitySessions(ctx, activityID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca sesi"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// respondActivity muat semula aktiviti (berserta nama kategori) supaya
// setiap mutasi memulangkan bentuk yang sama seperti GET /activities/:id.
func (h *ActivityHandler) respondActivity(c *gin.Context, status int, id uuid.UUID) {
	ctx := c.Request.Context()
	row, err := h.queries.GetActivityByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "aktiviti disimpan tetapi gagal dimuat semula"})
		return
	}
	detail, err := h.buildActivityDetail(ctx, row, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "aktiviti disimpan tetapi gagal dimuat semula"})
		return
	}
	c.JSON(status, detail)
}
