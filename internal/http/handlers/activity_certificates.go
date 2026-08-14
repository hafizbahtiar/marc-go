package handlers

import (
	"context"
	"errors"
	"fmt"
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
	"marc/internal/push"
	"marc/internal/storage"
)

const (
	// certificateSerialSequence — kunci dalam jadual `sequences`.
	//
	// Jadual, bukan `create sequence` Postgres: nextval() TIDAK berundur
	// dengan transaksi, jadi satu penerbitan yang gagal separuh jalan akan
	// meninggalkan lompang kekal dalam penomboran sijil. `sequences` ialah
	// `update ... returning` biasa dan berundur bersama transaksinya.
	certificateSerialSequence = "certificate_serial"

	// reasonCertificateRevoked — sebab yang direkod dalam `deleted_uploads`
	// supaya reaper sedia ada memadam PDF sijil yang ditarik balik.
	reasonCertificateRevoked = "certificate_revoked"

	// certificateUploadTimeout — had bagi SATU muat naik R2.
	//
	// R2Client.PutObject tidak menetapkan tempoh tamat sendiri. Tanpa had
	// ini, satu sambungan yang tersekat menahan keseluruhan pusingan fasa 2
	// selama-lamanya; dengan ia, pusingan itu gagal, baris kekal tanpa
	// r2_key, dan panggilan berikutnya menyambung semula.
	certificateUploadTimeout = 30 * time.Second
)

var (
	errActivityNotFinished       = errors.New("aktiviti belum tamat")
	errCertificateNotFound       = errors.New("sijil tidak dijumpai")
	errCertificateAlreadyRevoked = errors.New("sijil sudah ditarik balik")

	// errUnprintableCertificateField — nama/tajuk yang akan hilang aksara
	// bila dicetak. Dibungkus (bukan dipulangkan terus) supaya mesejnya
	// membawa medan dan nilai yang menyinggung sampai ke pengurusan.
	errUnprintableCertificateField = errors.New("medan sijil tidak boleh dicetak")
)

type CertificateHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	r2      *storage.R2Client
	push    *push.Service
	baseURL string
}

func NewCertificateHandler(
	pool *pgxpool.Pool, r2 *storage.R2Client, pushSvc *push.Service, baseURL string,
) *CertificateHandler {
	return &CertificateHandler{
		pool:    pool,
		queries: sqlc.New(pool),
		r2:      r2,
		push:    pushSvc,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// certificateR2Key — kunci objek bagi satu sijil.
//
// Berasaskan id sijil dan bukan nombor rawak: fasa 2 boleh diulang, dan
// pusingan kedua mesti menulis ganti objek yang SAMA. Kunci rawak akan
// meninggalkan yatim dalam bucket setiap kali muat naik gagal separuh jalan.
func certificateR2Key(id uuid.UUID) string {
	return "certificates/" + id.String() + ".pdf"
}

// ---- FASA 1 ----

// issueCertificatesTx ialah FASA 1: kira yang layak, ambil nombor siri,
// masukkan baris dengan r2_key null, catat audit, komit.
//
// Menjana PDF dan memuat naik ke R2 SENGAJA tiada di sini. Ia kerja luaran:
// transaksi yang menahan kunci selama ratusan muat naik ialah masalah, dan
// rollback tidak memadam objek R2. Fasa 2 (fillPendingCertificateFiles)
// berjalan selepas komit dan boleh disambung semula.
func issueCertificatesTx(
	ctx context.Context, pool *pgxpool.Pool, activityID uuid.UUID, actor audit.Actor,
) ([]sqlc.ActivityCertificate, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Setiap `return` ralat di bawah berlaku SEBELUM Commit — termasuk
	// selepas NextSequence dipanggil. Itu yang menjadikan kaunter siri
	// berundur bersama baris yang gagal.
	defer tx.Rollback(ctx)
	qtx := sqlc.New(pool).WithTx(tx)

	// Kunci baris aktiviti DAHULU, sebelum satu bacaan pun.
	//
	// Dahulu keempat-empat bacaan di bawah (aktiviti, bilangan sesi, calon
	// layak, siapa sudah bersijil) berjalan di luar transaksi — empat
	// snapshot pada empat masa berlainan — dan pool.Begin hanya dicapai
	// selepasnya. Dua akibatnya:
	//
	//  1. Pengurus yang membuang tanda kehadiran antara bacaan kelayakan
	//     dan insert menyebabkan sijil diterbitkan kepada orang yang sudah
	//     TIDAK layak — dan `unique (activity_id, user_id)` bukan partial,
	//     jadi ia tak boleh diterbitkan semula selepas ditarik balik.
	//  2. Dua pengurus menekan Terbitkan serentak: kedua-duanya
	//     mensnapshot `sudahBersijil` kosong, kedua-duanya melukis
	//     NextSequence bagi setiap calon, yang kalah jatuh ke
	//     `on conflict do nothing` → `continue` → dan TETAP komit dengan
	//     kaunter sudah bergerak. Itulah lompang siri yang jadual
	//     `sequences` wujud untuk dielakkan.
	//
	// Setiap laluan tulis lain pada aktiviti ini mengambil kunci yang sama
	// (pendaftaran, PATCH, check-in, pindaan); penerbitan ialah satu-satunya
	// pengecualian sebelum ini.
	if _, err := qtx.LockActivityForRegistration(ctx, activityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errActivityNotFound
		}
		return nil, err
	}

	activity, err := qtx.GetActivityByID(ctx, activityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errActivityNotFound
		}
		return nil, err
	}
	// activities.ends_at ialah max(sesi.ends_at) — RecomputeActivityWindow
	// satu-satunya penulisnya. Jadi semakan ini betul-betul bermaksud "sesi
	// terakhir sudah tamat", bukan sekadar tarikh yang ditaip pengurusan.
	if time.Now().Before(activity.EndsAt.Time) {
		return nil, errActivityNotFinished
	}

	totalSessions, err := qtx.CountActivitySessions(ctx, activityID)
	if err != nil {
		return nil, err
	}

	// Medan yang SAMA untuk setiap penerima disemak sekali, di sini, sebelum
	// mana-mana baris dicipta.
	//
	// Bukan sekadar penjimatan: fasa 1 MENSNAPSHOT activity.Title ke dalam
	// baris sijil, dan fasa 2 membaca snapshot itu — bukan aktiviti semasa.
	// Tajuk yang tidak boleh dicetak dan sempat masuk ke baris akan
	// menggagalkan setiap pusingan fasa 2 selama-lamanya, dan membetulkan
	// tajuk aktiviti melalui API TIDAK menyentuh snapshot itu. Satu-satunya
	// tempat semakan ini berguna ialah sebelum baris wujud.
	for _, f := range []struct{ nama, nilai string }{
		{"ActivityTitle", activity.Title},
		{"CategoryName", activity.CategoryName},
	} {
		if !certificate.EncodableName(f.nilai) {
			return nil, fmt.Errorf("%w: %s %q", errUnprintableCertificateField, f.nama, f.nilai)
		}
	}

	candidates, err := qtx.ListEligibleForCertificate(ctx, activityID)
	if err != nil {
		return nil, err
	}

	// Siapa yang SUDAH ada sijil untuk aktiviti ini.
	//
	// Tanpa senarai ini, penerbitan ulangan (iaitu laluan menyambung fasa 2
	// yang direka bentuk endpoint ini) melukis satu nombor siri bagi setiap
	// calon, memasukkan sifar baris kerana `on conflict do nothing`, dan
	// tetap KOMIT — kaunter bergerak 200 setiap percubaan semula. Lompang
	// itulah yang jadual `sequences` wujud untuk dielakkan.
	certified, err := qtx.ListCertificatesByActivity(ctx, activityID)
	if err != nil {
		return nil, err
	}
	sudahBersijil := make(map[uuid.UUID]bool, len(certified))
	for _, cert := range certified {
		sudahBersijil[cert.UserID] = true
	}

	var issued []sqlc.ActivityCertificate
	for _, cand := range candidates {
		if !certificate.IsEligible(int(cand.Attended), int(totalSessions), int(activity.AttendanceThresholdPct)) {
			continue
		}
		// SEBELUM NextSequence, sengaja. Nombor siri hanya dilukis untuk
		// baris yang benar-benar akan dimasukkan.
		if sudahBersijil[cand.UserID] {
			continue
		}
		// Semakan pra-terbang. Nama yang tidak boleh dicetak akan menjadi
		// deretan titik dalam PDF, dan fasa 2 tidak boleh membetulkannya —
		// lebih baik gagal sebelum apa-apa baris wujud.
		if !certificate.EncodableName(cand.DisplayName) {
			return nil, fmt.Errorf("%w: RecipientName %q", errUnprintableCertificateField, cand.DisplayName)
		}

		token, err := newCheckinToken()
		if err != nil {
			return nil, err
		}

		seq, err := qtx.NextSequence(ctx, certificateSerialSequence)
		if err != nil {
			return nil, err
		}
		// Tahun dalam waktu Malaysia. Aktiviti 1 Januari 00:30 MYT ialah 31
		// Disember 16:30 UTC — nombor siri tahun sebelumnya pada dokumen
		// yang tak boleh dibetulkan selepas diterbitkan.
		serial := fmt.Sprintf("MARC-%d-%06d", activity.StartsAt.Time.In(malaysiaTZ).Year(), seq)

		row, err := qtx.CreateCertificate(ctx, sqlc.CreateCertificateParams{
			ActivityID:    activityID,
			UserID:        cand.UserID,
			Serial:        serial,
			VerifyToken:   token,
			RecipientName: cand.DisplayName,
			ActivityTitle: activity.Title,
			ActivityDate:  pgDate(activity.StartsAt.Time),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// on conflict do nothing → sijil sudah wujud. Bukan ralat;
			// inilah yang menjadikan endpoint boleh diulang.
			continue
		}
		if err != nil {
			return nil, err
		}

		if err := audit.Record(ctx, qtx, audit.Entry{
			EntityType: audit.EntityCertificate,
			EntityID:   row.ID,
			Action:     audit.ActionCreate,
			Actor:      actor,
			New: map[string]any{
				"serial":      serial,
				"activity_id": activityID.String(),
				"user_id":     cand.UserID.String(),
			},
		}); err != nil {
			return nil, err
		}
		issued = append(issued, row)
	}

	if err := qtx.SetActivityCertificatesIssuedAt(ctx, activityID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return issued, nil
}

// ---- FASA 2 ----

// fillPendingCertificateFiles ialah FASA 2: jana PDF dan muat naik untuk
// setiap sijil yang r2_key masih null.
//
// Boleh diulang dengan selamat. Kalau proses mati separuh jalan atau R2
// tersekat, panggil semula endpoint dan ia menyambung dari baris yang
// belum siap.
//
// Berjujukan dengan sengaja: PutObject menyimpan keseluruhan PDF dalam
// memori, jadi memecutkan ini dengan goroutine tak berhad bermakna ratusan
// PDF dipegang serentak.
//
// Memulangkan baris yang failnya SIAP dalam pusingan ini (termasuk bila
// pusingan itu berakhir dengan ralat separuh jalan). Itulah senarai
// penerima yang layak diberitahu "sijil anda sedia": peralihan
// pending→siap berlaku sekali sahaja bagi setiap sijil, jadi panggilan
// menyambung tidak memberitahu orang yang sama dua kali.
func fillPendingCertificateFiles(
	ctx context.Context, pool *pgxpool.Pool, r2 *storage.R2Client,
	baseURL string, activityID uuid.UUID,
) ([]sqlc.ActivityCertificate, error) {
	q := sqlc.New(pool)
	pending, err := q.ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	activity, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		return nil, err
	}

	var completed []sqlc.ActivityCertificate
	for _, cert := range pending {
		pdf, err := certificate.GeneratePDF(certificate.Data{
			Serial:        cert.Serial,
			RecipientName: cert.RecipientName,
			ActivityTitle: cert.ActivityTitle,
			CategoryName:  activity.CategoryName,
			ActivityDate:  cert.ActivityDate.Time,
			VerifyURL:     strings.TrimRight(baseURL, "/") + "/verify/certificates/" + cert.VerifyToken,
		})
		if err != nil {
			// Ralat GeneratePDF menamakan medan yang menyinggung; ia
			// dikekalkan utuh sampai ke pengurusan.
			return completed, fmt.Errorf("jana sijil %s: %w", cert.Serial, err)
		}

		key := certificateR2Key(cert.ID)
		if err := putCertificateObject(ctx, r2, key, pdf); err != nil {
			return completed, fmt.Errorf("muat naik sijil %s: %w", cert.Serial, err)
		}

		// Kemas kini SELEPAS muat naik berjaya. Kalau ia gagal di sini,
		// baris kekal tanpa r2_key dan pusingan seterusnya menulis ganti
		// objek yang sama — muat naik R2 idempoten ikut kunci.
		if err := q.SetCertificateR2Key(ctx, sqlc.SetCertificateR2KeyParams{
			ID: cert.ID, R2Key: pgText(key),
		}); err != nil {
			return completed, err
		}
		completed = append(completed, cert)
	}
	return completed, nil
}

// putCertificateObject bungkus PutObject dengan tempoh tamat per-panggilan.
func putCertificateObject(ctx context.Context, r2 *storage.R2Client, key string, body []byte) error {
	upCtx, cancel := context.WithTimeout(ctx, certificateUploadTimeout)
	defer cancel()
	return r2.PutObject(upCtx, key, "application/pdf", body)
}

// ---- Tarik balik ----

// revokeCertificateTx tandakan sijil sebagai ditarik balik, gilirkan failnya
// untuk dipadam, dan catat audit — SATU transaksi.
//
// Baris sijil tidak dipadam. Sijil yang ditarik balik mesti tetap boleh
// disahkan SEBAGAI ditarik balik; baris yang lenyap hanya menghasilkan
// "tidak dijumpai", yang tak dapat dibezakan daripada sijil palsu.
func revokeCertificateTx(
	ctx context.Context, pool *pgxpool.Pool, certID uuid.UUID, reason string, actor audit.Actor,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := sqlc.New(pool).WithTx(tx)

	// Dibaca dahulu supaya "tidak dijumpai" dan "sudah ditarik balik" boleh
	// dibezakan — RevokeCertificate memulangkan ErrNoRows untuk kedua-duanya.
	before, err := q.GetCertificateByID(ctx, certID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errCertificateNotFound
		}
		return err
	}
	if before.RevokedAt.Valid {
		return errCertificateAlreadyRevoked
	}

	row, err := q.RevokeCertificate(ctx, sqlc.RevokeCertificateParams{
		ID: certID, RevokedReason: pgText(reason),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errCertificateAlreadyRevoked
		}
		return err
	}

	// Objek digilirkan, bukan dipadam terus: reaper deleted_uploads sedia
	// ada yang mencuba semula, dan pemadaman R2 di dalam transaksi tak
	// boleh diundurkan kalau komit gagal selepasnya.
	if row.R2Key.Valid {
		if err := q.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
			R2Key: row.R2Key.String, Reason: reasonCertificateRevoked,
		}); err != nil {
			return err
		}
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityCertificate,
		EntityID:   row.ID,
		Action:     audit.ActionDelete,
		Actor:      actor,
		Old: map[string]any{
			"serial":         row.Serial,
			"activity_id":    row.ActivityID.String(),
			"user_id":        row.UserID.String(),
			"r2_key":         row.R2Key.String,
			"revoked_reason": reason,
		},
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ---- Handler ----

func (h *CertificateHandler) requireManagement(c *gin.Context) bool {
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

// Issue — POST /activities/:id/certificates.
//
// Menjalankan fasa 1 (transaksi) kemudian fasa 2 (muat naik). Fasa 2 yang
// gagal separuh jalan BUKAN kegagalan permintaan: sijil sudah wujud dan
// sah, cuma failnya belum siap. Panggil semula endpoint untuk menyambung.
func (h *CertificateHandler) Issue(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	activityID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	issued, err := issueCertificatesTx(ctx, h.pool, activityID, auditActor(c, h.queries))
	switch {
	case errors.Is(err, errActivityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "aktiviti tidak dijumpai"})
		return
	case errors.Is(err, errActivityNotFinished):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "sijil hanya boleh diterbitkan selepas sesi terakhir tamat"})
		return
	case errors.Is(err, errUnprintableCertificateField):
		// Mesejnya membawa nama medan dan nilainya — pengurusan perlu tahu
		// rekod MANA yang perlu dibetulkan.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	case err != nil:
		log.Printf("terbit sijil aktiviti %s: %v", activityID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan sijil"})
		return
	}

	ready, err := fillPendingCertificateFiles(ctx, h.pool, h.r2, h.baseURL, activityID)

	// Diberitahu untuk setiap sijil yang failnya SIAP dalam pusingan ini —
	// termasuk pusingan yang gagal separuh jalan, kerana sijil yang sudah
	// siap tetap boleh dimuat turun. Baris sijil sudah komit (fasa 1), jadi
	// ini sudah pun di luar sebarang transaksi.
	h.notifyCertificateReady(ready, middleware.UserID(c))

	if err != nil {
		log.Printf("sediakan fail sijil aktiviti %s: %v", activityID, err)
		// Mesej TETAP. err di sini membalut ralat SDK AWS atau Postgres —
		// nama bucket, hos endpoint, request id — dan tempatnya dalam log,
		// bukan dalam badan respons. files_ready sudah memberitahu pemanggil
		// sejauh mana ia sampai.
		c.JSON(http.StatusAccepted, gin.H{
			"issued":      len(issued),
			"files_ready": h.countFilesReady(ctx, activityID),
			"message":     "sijil sudah dicipta tetapi sebahagian failnya belum siap; panggil semula untuk menyambung",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"issued":      len(issued),
		"files_ready": h.countFilesReady(ctx, activityID),
		"message":     "sijil siap diterbitkan",
	})
}

// notifyCertificateReady beritahu setiap penerima yang sijilnya baru siap.
//
// Semua baris di sini milik SATU aktiviti, jadi tajuk aktiviti (yang
// disnapshot pada baris sijil, bukan dibaca semula daripada aktiviti
// semasa) sama untuk semuanya dan satu mesej memadai.
func (h *CertificateHandler) notifyCertificateReady(ready []sqlc.ActivityCertificate, actorID uuid.UUID) {
	if len(ready) == 0 {
		return
	}
	targets := make([]notifyTarget, 0, len(ready))
	for _, cert := range ready {
		// Setiap penerima dipautkan kepada sijilnya SENDIRI — itu perbezaan
		// jenis ini daripada activity_published/activity_cancelled.
		targets = append(targets, notifyTarget{UserID: cert.UserID, CertificateID: pgUUID(cert.ID)})
	}
	notifyMembers(h.queries, h.push, notification{
		Targets:  targets,
		ActorID:  actorID,
		Type:     "certificate_ready",
		Title:    "Sijil Anda Sedia",
		Message:  "Sijil untuk " + ready[0].ActivityTitle + " sudah boleh dimuat turun.",
		Activity: pgUUID(ready[0].ActivityID),
		// Pengurus yang turut menyertai aktiviti ialah penerima sijil seperti
		// orang lain. Peralihan pending→siap berlaku SEKALI sahaja, jadi
		// melangkaunya di sini bermakna dia tidak akan pernah diberitahu.
		NotifyActor: true,
	})
}

// countFilesReady — berapa sijil aktiviti ini yang failnya sudah ada.
//
// SELURUH aktiviti, bukan hanya baris yang baru diterbitkan panggilan ini:
// pemanggil yang menyambung penerbitan yang gagal separuh jalan mahu tahu
// berapa banyak sijil aktiviti itu yang kini siap, bukan delta pusingan
// semasa (yang boleh jadi 0 sedangkan 199 fail sudah ada).
//
// Best-effort: ralat bacaan tidak menggagalkan respons yang perubahannya
// SUDAH commit.
func (h *CertificateHandler) countFilesReady(ctx context.Context, activityID uuid.UUID) int {
	all, err := h.queries.ListCertificatesByActivity(ctx, activityID)
	if err != nil {
		log.Printf("kira fail sijil siap %s: %v", activityID, err)
		return 0
	}
	n := 0
	for _, cert := range all {
		if cert.R2Key.Valid && !cert.RevokedAt.Valid {
			n++
		}
	}
	return n
}

type revokeCertificateRequest struct {
	Reason string `json:"reason"`
}

// Revoke — POST /certificates/:id/revoke. Pengurusan sahaja.
func (h *CertificateHandler) Revoke(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	certID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req revokeCertificateRequest
	if !bindJSON(c, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sebab tarik balik diperlukan"})
		return
	}

	err := revokeCertificateTx(c.Request.Context(), h.pool, certID, reason, auditActor(c, h.queries))
	switch {
	case errors.Is(err, errCertificateNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sijil tidak dijumpai"})
		return
	case errors.Is(err, errCertificateAlreadyRevoked):
		c.JSON(http.StatusConflict, gin.H{"error": "sijil sudah ditarik balik"})
		return
	case err != nil:
		log.Printf("tarik balik sijil %s: %v", certID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tarik balik sijil"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"revoked": true})
}

// certificateResponse — bentuk sijil seperti yang dilihat pemiliknya.
type certificateResponse struct {
	ID            uuid.UUID `json:"id"`
	ActivityID    uuid.UUID `json:"activity_id"`
	Serial        string    `json:"serial"`
	VerifyToken   string    `json:"verify_token"`
	RecipientName string    `json:"recipient_name"`
	ActivityTitle string    `json:"activity_title"`
	CategoryName  string    `json:"category_name"`
	ActivityDate  string    `json:"activity_date"`
	IssuedAt      time.Time `json:"issued_at"`
	// FileReady, bukan r2_key: kunci objek dalaman tak pernah keluar ke
	// klien — muat turun melalui Download yang menandatangani URL.
	FileReady bool `json:"file_ready"`
}

// ListMine — GET /me/certificates.
func (h *CertificateHandler) ListMine(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.queries.ListMyCertificates(ctx, middleware.UserID(c))
	if err != nil {
		log.Printf("senarai sijil saya: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca sijil"})
		return
	}

	out := make([]certificateResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, certificateResponse{
			ID:            r.ID,
			ActivityID:    r.ActivityID,
			Serial:        r.Serial,
			VerifyToken:   r.VerifyToken,
			RecipientName: r.RecipientName,
			ActivityTitle: r.ActivityTitle,
			CategoryName:  r.CategoryName,
			ActivityDate:  r.ActivityDate.Time.Format("2006-01-02"),
			IssuedAt:      r.IssuedAt.Time,
			FileReady:     r.R2Key.Valid,
		})
	}
	c.JSON(http.StatusOK, gin.H{"certificates": out})
}

const (
	// VerifyCertificateRoute — laluan route pengesahan awam, dieksport supaya
	// router.go dan ujian merujuk string yang SAMA. Drift di sini gagal dengan
	// kuat (pautan QR pada sijil bercetak jadi 404), tetapi sijil yang sudah
	// dicetak tidak boleh dibetulkan — jadi ia tetap satu pemalar.
	VerifyCertificateRoute = "/verify/certificates/:token"

	// VerifyRateLimitBucket — nama baldi had kadar bagi route di atas.
	//
	// Dieksport atas sebab yang LEBIH kuat daripada laluan: nama baldi yang
	// terpesong gagal SENYAP. Trafik pengesahan awam akan mula berkongsi
	// kunci Redis dengan baldi lain dan menghabiskan kuota log masuk ahli —
	// tepat bug yang baldi bernama ini wujud untuk menghalang.
	VerifyRateLimitBucket = "verify"
)

// verifyResponse — bentuk respons AWAM, ditakrifkan sebagai struct eksplisit
// dan bukan gin.H daripada baris DB.
//
// Sebab: mengembalikan baris terus bermakna menambah lajur pada
// activity_certificates secara senyap-senyap menyiarkannya ke internet.
// Struct ini ialah sempadan yang mesti dilalui dengan sengaja — tiada emel,
// tiada user_id, tiada r2_key, tiada status keahlian.
//
// JANGAN tambah `omitempty` pada mana-mana medan: ujian tripwire
// TestVerifyResponseHanyaMedanAwam menegaskan atas set kunci yang HADIR
// dalam JSON, dan medan yang lenyap bila kosong menjadikan penegasan itu
// bergantung pada data dan bukan pada struct.
type verifyResponse struct {
	Serial        string `json:"serial"`
	RecipientName string `json:"recipient_name"`
	ActivityTitle string `json:"activity_title"`
	ActivityDate  string `json:"activity_date"`
	IssuedAt      string `json:"issued_at"`
	Status        string `json:"status"` // "sah" | "ditarik_balik"
}

// Verify — GET /verify/certificates/:token. AWAM, tanpa auth.
//
// Satu-satunya route modul ini yang boleh dicapai sesiapa di internet: ia
// yang menyokong kod QR pada sijil bercetak.
func (h *CertificateHandler) Verify(c *gin.Context) {
	cert, err := h.queries.GetCertificateByVerifyToken(c.Request.Context(), c.Param("token"))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("semak sijil awam: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak sijil"})
			return
		}
		// 404 yang SAMA — status dan badan — untuk token tak wujud dan token
		// cacat. Respons berbeza akan menjadi oracle: penyerang boleh
		// mengesahkan token mana yang pernah wujud.
		c.JSON(http.StatusNotFound, gin.H{"error": "sijil tidak dijumpai"})
		return
	}

	status := "sah"
	if cert.RevokedAt.Valid {
		// Sijil yang ditarik kekal boleh disemak dan dilaporkan sebagai
		// ditarik. Memadam baris akan menjadikannya nampak seperti tidak
		// pernah wujud — lebih buruk bagi orang yang sedang mengesahkan.
		// Sebab tarik balik SENGAJA tidak didedahkan; ia bukan urusan awam.
		status = "ditarik_balik"
	}

	c.JSON(http.StatusOK, verifyResponse{
		Serial:        cert.Serial,
		RecipientName: cert.RecipientName,
		ActivityTitle: cert.ActivityTitle,
		ActivityDate:  cert.ActivityDate.Time.Format("2006-01-02"),
		IssuedAt:      cert.IssuedAt.Time.Format(time.RFC3339),
		Status:        status,
	})
}

// Download — GET /me/certificates/:id/file.
//
// Memulangkan URL bertandatangan berumur pendek, bukan bait PDF: R2 yang
// menyampaikan fail, backend tidak menjadi bottleneck lebar jalur.
func (h *CertificateHandler) Download(c *gin.Context) {
	certID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	cert, err := h.queries.GetCertificateByID(ctx, certID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sijil tidak dijumpai"})
			return
		}
		log.Printf("baca sijil %s: %v", certID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca sijil"})
		return
	}
	// 404 dan bukan 403: mengesahkan bahawa sijil ini WUJUD kepada orang
	// yang bukan pemiliknya sudah pun satu kebocoran.
	if cert.UserID != middleware.UserID(c) {
		c.JSON(http.StatusNotFound, gin.H{"error": "sijil tidak dijumpai"})
		return
	}
	if cert.RevokedAt.Valid {
		c.JSON(http.StatusGone, gin.H{"error": "sijil ini telah ditarik balik"})
		return
	}
	if !cert.R2Key.Valid {
		// Fasa 2 belum siap untuk baris ini — keadaan sementara yang normal,
		// bukan ralat.
		c.JSON(http.StatusConflict, gin.H{"error": "sijil sedang disediakan, cuba sebentar lagi"})
		return
	}

	url := h.r2.SignedURL(ctx, cert.R2Key.String)
	if url == "" {
		log.Printf("tandatangan URL sijil %s gagal", certID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan pautan muat turun"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}
