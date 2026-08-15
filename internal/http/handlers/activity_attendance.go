package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/certificate"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type AttendanceHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewAttendanceHandler(pool *pgxpool.Pool) *AttendanceHandler {
	return &AttendanceHandler{pool: pool, queries: sqlc.New(pool)}
}

var (
	errOutsideCheckinWindow = errors.New("di luar tetingkap check-in")
	errNotRegistered        = errors.New("tidak berdaftar untuk aktiviti ini")
	errSessionNotFound      = errors.New("sesi tidak dijumpai")
	errAttendanceNotFound   = errors.New("kehadiran tidak dijumpai")
)

// Kaedah yang ada UI. Skema membenarkan 'code' juga, tetapi ia tiada
// laluan klien lagi — menerimanya sekarang bermakna menerima nilai yang
// tiada siapa boleh hasilkan secara sah. 'self_scan' DITAMBAH 2026-08-15
// — lihat komen `Mark` utk reka bentuk keselamatan penuh (kenapa ia
// TIDAK memerlukan `checkin_token` berputar seperti yang pernah
// dibimbangkan TODO.md).
var validAttendanceMethods = map[string]bool{"manual": true, "scan": true, "self_scan": true}

type markResult struct {
	Created bool
	Row     sqlc.ActivityAttendance
}

// attendanceAmendment — pindaan kehadiran di LUAR tetingkap check-in.
//
// Nilai bukan-nil melangkau SATU semakan sahaja (tetingkap masa) dan
// menandakan catatan audit sebagai pindaan. Sebab wajib: pindaan yang tidak
// dapat dibezakan daripada check-in biasa dalam jejak audit lebih buruk
// daripada tiada laluan pindaan langsung.
type attendanceAmendment struct {
	Reason string
}

// markAttendanceTx menanda satu kehadiran.
//
// Menerima registration_id (skrin senarai, method 'manual') ATAU token yang
// sudah diselesaikan kepada pendaftaran (scanner, method 'scan') — pemanggil
// yang menyelesaikan token, jadi fungsi ini melihat satu bentuk input
// sahaja.
//
// SEMUA semakan berlaku DALAM transaksi, selepas kunci baris aktiviti
// diambil: sesi, tetingkap masa dan pemilikan pendaftaran semuanya boleh
// berubah di bawah kita kalau dibaca di luar.
//
// amend bukan-nil = pindaan di luar tetingkap. Ia melangkau semakan
// tetingkap masa DAN TIADA YANG LAIN — kunci aktiviti, semakan
// sesi-milik-aktiviti-sama, penolakan pendaftaran yang dibatalkan dan
// `on conflict do nothing` semuanya kekal di laluan yang sama.
func markAttendanceTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	sessionID, registrationID uuid.UUID,
	method string,
	actor audit.Actor,
	amend *attendanceAmendment,
) (markResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return markResult{}, err
	}
	// Setiap `return` di bawah berlaku SEBELUM Commit — tiada laluan yang
	// boleh menyimpan kehadiran tanpa melepasi semakan tetingkap dan
	// pemilikan.
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	// Bacaan pertama hanya untuk tahu aktiviti mana yang perlu dikunci.
	session, err := q.GetActivitySessionByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markResult{}, errSessionNotFound
		}
		return markResult{}, err
	}

	// Kunci baris aktiviti — separuh lagi bagi interlock yang
	// replaceSessionsAudited (Task 6) sudah pegang. activity_attendances
	// .session_id ialah `on delete cascade`, jadi penggantian set sesi
	// MEMUSNAHKAN kehadiran dan bukan gagal kerananya; check-in yang commit
	// di tengah-tengah penggantian akan hilang tanpa jejak. Kunci pada satu
	// belah sahaja tidak menutup lubang itu.
	if _, err := q.LockActivityForRegistration(ctx, session.ActivityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markResult{}, errActivityNotFound
		}
		return markResult{}, err
	}

	// Baca semula selepas kunci: kalau penggantian sesi mendahului kita,
	// sesi tadi sudah tiada dan insert di bawah hanya akan melanggar FK.
	session, err = q.GetActivitySessionByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markResult{}, errSessionNotFound
		}
		return markResult{}, err
	}

	// SATU-SATUNYA tempat `amend` mengubah kelakuan. Diletakkan di sini,
	// pada syarat itu sendiri, dan bukan sebagai pulangan awal di atas —
	// pulangan awal akan turut memintas kunci, semakan pemilikan sesi dan
	// penolakan pendaftaran yang dibatalkan.
	if amend == nil && !certificate.WithinCheckinWindow(time.Now(), session.StartsAt.Time, session.EndsAt.Time) {
		return markResult{}, errOutsideCheckinWindow
	}

	reg, err := q.GetRegistrationByID(ctx, registrationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markResult{}, errNotRegistered
		}
		return markResult{}, err
	}
	// Tiada FK yang merentasi kedua-dua hubungan — activity_attendances
	// merujuk pendaftaran dan sesi secara berasingan. Tanpa semakan ini,
	// pepijat di tempat lain boleh merekod kehadiran aktiviti A atas sesi
	// aktiviti B, dan kiraan kelayakan sijil terlebih kira secara senyap.
	if reg.ActivityID != session.ActivityID {
		return markResult{}, errNotRegistered
	}
	if reg.Status == "cancelled" {
		return markResult{}, errNotRegistered
	}

	row, err := q.MarkAttendance(ctx, sqlc.MarkAttendanceParams{
		RegistrationID: registrationID,
		SessionID:      sessionID,
		Method:         method,
		MarkedBy:       pgUUID(actor.UserID),
	})
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		// on conflict do nothing → tiada baris dipulangkan. Sudah hadir.
		created = false
	} else if err != nil {
		return markResult{}, err
	}

	// Hanya tanda yang BENAR-BENAR mencipta baris diaudit; imbasan berulang
	// tiada perubahan untuk direkod, dan QR yang dipegang di depan lens akan
	// membanjiri jejak dengan baris kosong.
	if created {
		newValues := map[string]any{
			"registration_id": registrationID.String(),
			"session_id":      sessionID.String(),
			"method":          method,
		}
		// Pembeza pindaan. Inilah keseluruhan justifikasi bagi membenarkan
		// tetingkap dilangkau: pindaan dibenarkan KERANA ia boleh dibezakan
		// daripada check-in biasa semasa membaca jejak.
		if amend != nil {
			newValues["amendment"] = true
			newValues["reason"] = amend.Reason
		}
		if err := audit.Record(ctx, q, audit.Entry{
			EntityType: audit.EntityAttendance,
			EntityID:   row.ID,
			Action:     audit.ActionCreate,
			Actor:      actor,
			New:        newValues,
		}); err != nil {
			return markResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return markResult{}, err
	}
	return markResult{Created: created, Row: row}, nil
}

// requireManagement — sama seperti ActivityHandler/RegistrationHandler:
// semakan dibuat DALAM handler (authz.IsManagement), bukan middleware.
func (h *AttendanceHandler) requireManagement(c *gin.Context) bool {
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

type markAttendanceRequest struct {
	RegistrationID string `json:"registration_id"`
	CheckinToken   string `json:"checkin_token"`
	Method         string `json:"method"`
	// Amend/Reason — laluan pindaan. Diterima pada kedua-dua bentuk
	// pengenalan (registration_id dan checkin_token) kerana pindaan ialah
	// pembetulan kepada laluan yang SAMA, bukan endpoint lain.
	Amend  bool   `json:"amend"`
	Reason string `json:"reason" binding:"omitempty,max=500"`
}

// memberSummary — apa yang skrin scanner perlukan untuk mengesahkan bahawa
// orang di hadapannya itulah yang baru ditanda.
type memberSummary struct {
	DisplayName string `json:"display_name"`
	MemberID    string `json:"member_id"`
}

// Mark — POST /activities/:id/sessions/:sid/attendance.
//
// Pengurusan sahaja untuk method 'manual'/'scan': kehadiran ialah bukti
// yang menentukan siapa menerima sijil, jadi ia tidak boleh ditanda oleh
// orang yang menerimanya (staff tandakan ahli LAIN).
//
// 'self_scan' (2026-08-15) PENGECUALIAN SENGAJA — ahli tandakan
// kehadiran SENDIRI, tiada gate management. Reka bentuk keselamatan:
// TODO.md pernah nyatakan self_scan "memerlukan token berputar" (elak
// checkin_token statik jadi kelayakan pembawa yang boleh diedarkan via
// tangkapan skrin). Reka bentuk di sini elak keperluan itu SEPENUHNYA
// dengan tidak menggunakan checkin_token/registration_id LANGSUNG utk
// self_scan — identiti datang drpd JWT pemanggil (`middleware.UserID`),
// bukan drpd apa-apa dalam body permintaan. QR yang diimbas ahli
// (`checkin_qr_session.dart`) cuma mengekod PASANGAN aktiviti+sesi
// (data awam venue, bukan kelayakan peribadi) — tangkapan skrinnya
// tidak berguna kepada sesiapa: ia cuma "sesi apa", server tetap kira
// SIAPA drpd token JWT log masuk sebenar peng-imbas.
func (h *AttendanceHandler) Mark(c *gin.Context) {
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	sessionID, ok := parseUUIDParam(c, "sid")
	if !ok {
		return
	}

	var req markAttendanceRequest
	if !bindJSON(c, &req) {
		return
	}
	if !validAttendanceMethods[req.Method] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kaedah kehadiran tidak sah"})
		return
	}

	ctx := c.Request.Context()
	var reg sqlc.ActivityRegistration
	var err error

	if req.Method == "self_scan" {
		// Dua medan ni TIADA makna utk self_scan (identiti drpd JWT) — kalau
		// dibenarkan lalu diabaikan senyap, caller mungkin salah anggap ia
		// dihormati. Tolak eksplisit supaya salah faham tak berlaku, dan
		// elak laluan ni pernah jadi "cara kedua" tanda org LAIN tanpa gate
		// management (kalau kelak seseorang cuba hantar registration_id
		// org lain sekali dgn method=self_scan, ia mesti ditolak, bukan
		// senyap diabaikan/diterima).
		if req.RegistrationID != "" || req.CheckinToken != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "self_scan tidak menerima registration_id/checkin_token"})
			return
		}
		// Pindaan (amend) ialah alat PEMBETULAN management (rekod SIAPA
		// meluluskan pengecualian tetingkap masa, dgn sebab beraudit) —
		// bukan sesuatu ahli patut boleh buat atas rekod kehadiran sendiri.
		if req.Amend {
			c.JSON(http.StatusBadRequest, gin.H{"error": "self_scan tidak menyokong pindaan"})
			return
		}
		reg, err = h.queries.GetRegistrationByActivityAndUser(ctx, sqlc.GetRegistrationByActivityAndUserParams{
			ActivityID: activityID,
			UserID:     middleware.UserID(c),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusConflict, gin.H{"error": "anda tidak berdaftar untuk aktiviti ini"})
				return
			}
			log.Printf("baca pendaftaran sendiri (self_scan) aktiviti %s: %v", activityID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda kehadiran"})
			return
		}
	} else {
		if !h.requireManagement(c) {
			return
		}
		// TEPAT satu pengenalan. Menerima kedua-duanya bermakna memilih satu
		// secara senyap bila ia bercanggah — dan yang tidak dipilih itu ahli
		// yang sebenarnya berdiri di depan scanner.
		if (req.RegistrationID == "") == (req.CheckinToken == "") {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "berikan registration_id atau checkin_token, satu sahaja"})
			return
		}
		// Token diselesaikan DI SINI supaya markAttendanceTx melihat satu
		// bentuk input sahaja.
		if req.CheckinToken != "" {
			reg, err = h.queries.GetRegistrationByCheckinToken(ctx, req.CheckinToken)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					c.JSON(http.StatusNotFound, gin.H{"error": "QR tidak dikenali"})
					return
				}
				log.Printf("selesaikan token check-in: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda kehadiran"})
				return
			}
		} else {
			registrationID, parseErr := uuid.Parse(req.RegistrationID)
			if parseErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "registration_id tidak sah"})
				return
			}
			reg, err = h.queries.GetRegistrationByID(ctx, registrationID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					c.JSON(http.StatusConflict, gin.H{"error": "ahli ini tidak berdaftar untuk aktiviti ini"})
					return
				}
				log.Printf("baca pendaftaran %s: %v", registrationID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda kehadiran"})
				return
			}
		}
	}

	// Pendaftaran mesti milik aktiviti dalam laluan; markAttendanceTx pula
	// mengesahkan sesi milik aktiviti yang sama, di bawah kunci.
	if reg.ActivityID != activityID {
		c.JSON(http.StatusConflict, gin.H{"error": "ahli ini tidak berdaftar untuk aktiviti ini"})
		return
	}

	// Ditolak SEBELUM apa-apa sentuhan DB: pindaan tanpa sebab ialah tepat
	// perkara yang jejak audit sepatutnya halang, jadi ia tidak boleh
	// meninggalkan baris kehadiran di belakangnya. (self_scan dah tolak
	// req.Amend=true di atas, jadi cawangan ni tak dicapai utk self_scan.)
	var amend *attendanceAmendment
	if req.Amend {
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sebab pindaan diperlukan"})
			return
		}
		amend = &attendanceAmendment{Reason: reason}
	}

	res, err := markAttendanceTx(ctx, h.pool, sessionID, reg.ID, req.Method, auditActor(c, h.queries), amend)
	switch {
	case errors.Is(err, errOutsideCheckinWindow):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "di luar tetingkap check-in"})
		return
	case errors.Is(err, errNotRegistered):
		c.JSON(http.StatusConflict, gin.H{"error": "ahli ini tidak berdaftar untuk aktiviti ini"})
		return
	case errors.Is(err, errSessionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sesi tidak dijumpai"})
		return
	case errors.Is(err, errActivityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	case err != nil:
		log.Printf("tanda kehadiran sesi %s: %v", sessionID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda kehadiran"})
		return
	}

	// created=false BUKAN ralat: imbasan berulang ialah kelakuan biasa, dan
	// UI menunjukkan "sudah ditanda hadir" — juga hijau, bukan merah.
	c.JSON(http.StatusOK, gin.H{"created": res.Created, "member": h.memberOf(ctx, reg.UserID)})
}

// memberOf — nama untuk dipaparkan pada skrin scanner. Profil yang gagal
// dibaca tidak menggagalkan check-in yang SUDAH commit; ia hanya kembali
// kosong.
func (h *AttendanceHandler) memberOf(ctx context.Context, userID uuid.UUID) memberSummary {
	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		log.Printf("baca profil %s untuk respons kehadiran: %v", userID, err)
		return memberSummary{}
	}
	return memberSummary{DisplayName: profile.DisplayName.String, MemberID: profile.MemberID}
}

// Unmark — DELETE /activities/:id/sessions/:sid/attendance/:rid.
//
// Membuang kehadiran memadam bukti sijil, jadi ia pengurusan sahaja dan
// SENTIASA diaudit. Tetingkap check-in tidak dikenakan di sini: pembetulan
// silap tanda datang selepas sesi tamat, itu sebabnya ia wujud.
func (h *AttendanceHandler) Unmark(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	sessionID, ok := parseUUIDParam(c, "sid")
	if !ok {
		return
	}
	registrationID, ok := parseUUIDParam(c, "rid")
	if !ok {
		return
	}

	ctx := c.Request.Context()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	// Kunci yang sama seperti laluan tanda — jejak audit dan baris kehadiran
	// mesti bergerak bersama penggantian sesi, bukan berselang-seli dengannya.
	if _, err := q.LockActivityForRegistration(ctx, activityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
			return
		}
		log.Printf("kunci aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}

	session, err := q.GetActivitySessionByID(ctx, sessionID)
	if err != nil || session.ActivityID != activityID {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("baca sesi %s: %v", sessionID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "sesi tidak dijumpai"})
		return
	}

	// Baca dahulu: id baris ialah entity_id jejak audit, dan snapshotnya
	// satu-satunya rekod yang kekal selepas baris dipadam.
	before, err := q.GetAttendance(ctx, sqlc.GetAttendanceParams{
		RegistrationID: registrationID, SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": errAttendanceNotFound.Error()})
			return
		}
		log.Printf("baca kehadiran %s/%s: %v", registrationID, sessionID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}

	affected, err := q.DeleteAttendance(ctx, sqlc.DeleteAttendanceParams{
		RegistrationID: registrationID, SessionID: sessionID,
	})
	if err != nil {
		log.Printf("buang kehadiran %s/%s: %v", registrationID, sessionID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": errAttendanceNotFound.Error()})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityAttendance,
		EntityID:   before.ID,
		Action:     audit.ActionDelete,
		Actor:      auditActor(c, h.queries),
		Old: map[string]any{
			"registration_id": registrationID.String(),
			"session_id":      sessionID.String(),
			"method":          before.Method,
			"checked_in_at":   before.CheckedInAt.Time.UTC().Format(time.RFC3339),
		},
	}); err != nil {
		log.Printf("audit buang kehadiran: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang kehadiran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
