package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/onesignal"
	"marc/internal/push"
)

// activityTestPool — sama corak dengan handler test sedia ada: di-skip
// melainkan DB ujian ditetapkan. Guna DB BUANGAN.
//
// ACTIVITY_TEST_DB diutamakan, tapi HANDLER_TEST_DB diterima sebagai
// sandaran: ujian pendaftaran (Task 7) perlukan KEDUA-DUA seedActivity dan
// seedMember, dan memaksa dua pemboleh ubah persekitaran menunjuk ke DB
// yang sama cuma menjemput mereka menyimpang. Migrate dipanggil di sini
// atas sebab yang sama seperti statusTestPool — DB yang basi patut
// dinaik taraf sendiri, bukan gagal dengan ralat scan yang mengelirukan.
//
// Dikongsi dengan ujian modul aktiviti yang lain (Task 7-9) dalam pakej
// ini — jangan tukar tandatangan tanpa periksa pemanggil lain.
func activityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ACTIVITY_TEST_DB")
	if dsn == "" {
		dsn = os.Getenv("HANDLER_TEST_DB")
	}
	if dsn == "" {
		t.Skip("tetapkan ACTIVITY_TEST_DB (atau HANDLER_TEST_DB) untuk jalankan ujian ini")
	}
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("sambung: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedActivity cipta satu aktiviti (kategori 'badminton' daripada seed
// migration) berserta satu sesi awal, dan daftarkan pembersihannya. DB
// ujian dikongsi antara ujian dalam pakej ini, jadi setiap seed mesti
// membuang apa yang ia cipta.
func seedActivity(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var categoryID uuid.UUID
	if err := pool.QueryRow(ctx,
		`select id from activity_categories where key = 'badminton'`).Scan(&categoryID); err != nil {
		t.Fatalf("kategori seed tiada — jalankan migration atas DB ujian: %v", err)
	}

	start := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	var activityID uuid.UUID
	err := pool.QueryRow(ctx, `
		insert into activities (category_id, title, location_name, starts_at, ends_at,
		  registration_closes_at)
		values ($1, 'Ujian Aktiviti', 'Dewan A', $2, $3, $2)
		returning id`, categoryID, start, start.Add(2*time.Hour)).Scan(&activityID)
	if err != nil {
		t.Fatalf("seed aktiviti: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		insert into activity_sessions (activity_id, seq, starts_at, ends_at)
		values ($1, 1, $2, $3)`, activityID, start, start.Add(2*time.Hour)); err != nil {
		t.Fatalf("seed sesi: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from activities where id = $1`, activityID)
	})
	return activityID
}

// TestReplaceSessionsMengekalkanInvarianTetingkap — harga yang kita bayar
// untuk mendenormalisasi activities.starts_at/ends_at. Kalau ujian ini
// tiada, invarian itu hanya niat baik.
func TestReplaceSessionsMengekalkanInvarianTetingkap(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	activityID := seedActivity(t, pool)

	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	sessions := []sessionInput{
		{Seq: 1, StartsAt: base.Add(48 * time.Hour), EndsAt: base.Add(50 * time.Hour)},
		{Seq: 2, StartsAt: base, EndsAt: base.Add(2 * time.Hour)}, // paling awal, seq kemudian
		{Seq: 3, StartsAt: base.Add(24 * time.Hour), EndsAt: base.Add(27 * time.Hour)},
	}
	if err := replaceSessionsTx(ctx, pool, activityID, sessions); err != nil {
		t.Fatalf("replaceSessions: %v", err)
	}

	got, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		t.Fatalf("GetActivityByID: %v", err)
	}
	if !got.StartsAt.Time.Equal(base) {
		t.Errorf("starts_at = %v, mahu %v (min sesi, bukan sesi pertama ikut seq)", got.StartsAt.Time, base)
	}
	wantEnd := base.Add(50 * time.Hour)
	if !got.EndsAt.Time.Equal(wantEnd) {
		t.Errorf("ends_at = %v, mahu %v", got.EndsAt.Time, wantEnd)
	}

	// Buang sesi paling awal — tetingkap mesti mengecut, bukan kekal basi.
	if err := replaceSessionsTx(ctx, pool, activityID, sessions[:1]); err != nil {
		t.Fatalf("replaceSessions kedua: %v", err)
	}
	got, err = q.GetActivityByID(ctx, activityID)
	if err != nil {
		t.Fatalf("GetActivityByID kedua: %v", err)
	}
	if !got.StartsAt.Time.Equal(base.Add(48 * time.Hour)) {
		t.Errorf("selepas buang sesi terawal, starts_at = %v, mahu %v",
			got.StartsAt.Time, base.Add(48*time.Hour))
	}
	if !got.EndsAt.Time.Equal(base.Add(50 * time.Hour)) {
		t.Errorf("selepas buang sesi terawal, ends_at = %v, mahu %v",
			got.EndsAt.Time, base.Add(50*time.Hour))
	}
}

// TestReplaceSessionsMenolakSetKosong — RecomputeActivityWindow ada guard
// `s.min_start is not null`, jadi set kosong akan meninggalkan tetingkap
// lama tanpa sebarang ralat. Penolakan mesti berlaku sebelum itu.
func TestReplaceSessionsMenolakSetKosong(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	activityID := seedActivity(t, pool)
	before, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		t.Fatalf("GetActivityByID: %v", err)
	}

	if err := replaceSessionsTx(ctx, pool, activityID, nil); err == nil {
		t.Fatal("replaceSessions dengan set kosong: mahu ralat, dapat nil")
	}

	after, err := q.GetActivityByID(ctx, activityID)
	if err != nil {
		t.Fatalf("GetActivityByID selepas: %v", err)
	}
	if !after.StartsAt.Time.Equal(before.StartsAt.Time) {
		t.Errorf("tetingkap berubah walaupun operasi ditolak: %v -> %v",
			before.StartsAt.Time, after.StartsAt.Time)
	}
	count, err := q.CountActivitySessions(ctx, activityID)
	if err != nil {
		t.Fatalf("CountActivitySessions: %v", err)
	}
	if count != 1 {
		t.Errorf("bilangan sesi = %d, mahu 1 (set asal kekal)", count)
	}
}

// ---- Harness handler ----

// activityCall bina gin.Context bertulang sendiri (tiada engine/middleware)
// dan panggil satu kaedah handler, ikut corak callSetStatus dalam
// profile_status_live_test.go. userID diset terus sebab RequireAuth tak
// dijalankan di sini.
func activityCall(
	t *testing.T,
	pool *pgxpool.Pool,
	callerID uuid.UUID,
	method, target, body string,
	params gin.Params,
	fn func(*ActivityHandler, *gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("userID", callerID)

	fn(NewActivityHandler(pool, testPushService(pool)), c)
	return rec
}

// testPushService — push.Service dengan kelayakan OneSignal kosong, jadi
// NotifyUser jadi no-op senyap (lihat onesignal.Client.Enabled). Sama corak
// dengan storage.NewR2Client("", ...) di ujian lain: laluan notifikasi tetap
// dijalankan, cuma tiada panggilan keluar.
func testPushService(pool *pgxpool.Pool) *push.Service {
	return push.NewService(sqlc.New(pool), onesignal.NewClient("", ""))
}

func idParam(id uuid.UUID) gin.Params {
	return gin.Params{{Key: "id", Value: id.String()}}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("nyahkod badan (%s): %v", rec.Body.String(), err)
	}
	return out
}

func setActivityStatusDirect(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`update activities set status = $2 where id = $1`, id, status); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

// ---- Keizinan ----

// Setiap laluan tulis mesti disekat untuk ahli biasa, bukan hanya yang
// pertama ditulis.
func TestLaluanTulisMenolakAhliBiasa(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	member := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivity(t, pool)

	cases := []struct {
		name, method, body string
		fn                 func(*ActivityHandler, *gin.Context)
	}{
		{"Create", http.MethodPost, `{}`, (*ActivityHandler).Create},
		{"Update", http.MethodPatch, `{"title":"x"}`, (*ActivityHandler).Update},
		{"Publish", http.MethodPost, ``, (*ActivityHandler).Publish},
		{"Cancel", http.MethodPost, `{"reason":"hujan"}`, (*ActivityHandler).Cancel},
		{"ReplaceSessions", http.MethodPut, `{"sessions":[]}`, (*ActivityHandler).ReplaceSessions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := activityCall(t, pool, member, tc.method, "/activities", tc.body,
				idParam(activityID), tc.fn)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// Aktiviti draf bukan untuk mata ahli — dan 404, bukan 403, supaya
// kewujudannya pun tak bocor.
func TestGetAktivitiDrafTersembunyiDaripadaAhli(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	member := seedMember(t, ctx, pool, "ahli", "approved")
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool) // status lalai = draft

	rec := activityCall(t, pool, member, http.MethodGet, "/activities/x", "",
		idParam(activityID), (*ActivityHandler).Get)
	if rec.Code != http.StatusNotFound {
		t.Errorf("ahli biasa: status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}

	rec = activityCall(t, pool, manager, http.MethodGet, "/activities/x", "",
		idParam(activityID), (*ActivityHandler).Get)
	if rec.Code != http.StatusOK {
		t.Errorf("pengurusan: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
}

// ---- Bentuk respons GET /activities/:id ----

// Tiga medan tambahan yang klien Flutter (Task 12) ditulis untuknya.
func TestGetMemulangkanSesiKiraanDanStatusPendaftaran(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	member := seedMember(t, ctx, pool, "ahli", "approved")
	other := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivity(t, pool)
	setActivityStatusDirect(t, pool, activityID, "published")

	// Orang lain sudah daftar: kiraan mesti 1, tapi is_registered pemanggil
	// mesti kekal false. Kalau kedua-duanya dibaca daripada sumber yang
	// sama, ujian ini yang menangkapnya.
	if _, err := pool.Exec(ctx, `
		insert into activity_registrations (activity_id, user_id, checkin_token)
		values ($1, $2, $3)`, activityID, other, uuid.NewString()); err != nil {
		t.Fatalf("seed pendaftaran: %v", err)
	}

	rec := activityCall(t, pool, member, http.MethodGet, "/activities/x", "",
		idParam(activityID), (*ActivityHandler).Get)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, badan = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)

	sessions, ok := body["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %v, mahu senarai 1 sesi", body["sessions"])
	}
	if body["registration_count"] != float64(1) {
		t.Errorf("registration_count = %v, mahu 1", body["registration_count"])
	}
	if body["is_registered"] != false {
		t.Errorf("is_registered = %v, mahu false (pemanggil belum daftar)", body["is_registered"])
	}
	// Baris aktiviti diratakan pada aras atas, bukan disarangkan.
	if body["title"] != "Ujian Aktiviti" {
		t.Errorf("title = %v, mahu medan aktiviti pada aras atas", body["title"])
	}

	// Sekarang pemanggil sendiri daftar.
	if _, err := pool.Exec(ctx, `
		insert into activity_registrations (activity_id, user_id, checkin_token)
		values ($1, $2, $3)`, activityID, member, uuid.NewString()); err != nil {
		t.Fatalf("seed pendaftaran pemanggil: %v", err)
	}

	rec = activityCall(t, pool, member, http.MethodGet, "/activities/x", "",
		idParam(activityID), (*ActivityHandler).Get)
	body = decodeBody(t, rec)
	if body["is_registered"] != true {
		t.Errorf("is_registered = %v, mahu true selepas daftar", body["is_registered"])
	}
	if body["registration_count"] != float64(2) {
		t.Errorf("registration_count = %v, mahu 2", body["registration_count"])
	}
}

// ---- Parameter senarai ----

// Cursor membawa (starts_at, id) sekali gus. Separuh cursor mesti 400,
// bukan senarai kosong yang nampak macam "habis".
func TestListMenolakParameterTidakSah(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	member := seedMember(t, ctx, pool, "ahli", "approved")

	cases := []struct{ name, query string }{
		{"cursor tanpa id", "?cursor=2026-09-01T08:00:00Z"},
		{"cursor tanpa masa", "?cursor=|" + uuid.NewString()},
		{"cursor karut", "?cursor=abc"},
		{"limit bukan nombor", "?limit=abc"},
		{"limit terlalu besar", "?limit=500"},
		{"limit sifar", "?limit=0"},
		{"status tidak dikenali", "?status=entah"},
		{"category_id rosak", "?category_id=bukan-uuid"},
		{"upcoming rosak", "?upcoming=mungkin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := activityCall(t, pool, member, http.MethodGet, "/activities"+tc.query, "",
				nil, (*ActivityHandler).List)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// Draf tak boleh diminta oleh ahli biasa melalui ?status=draft.
func TestListStatusDrafPerluPengurusan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	member := seedMember(t, ctx, pool, "ahli", "approved")

	rec := activityCall(t, pool, member, http.MethodGet, "/activities?status=draft", "",
		nil, (*ActivityHandler).List)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}
}

// ---- PATCH separa ----

// Bukti bahawa gabungan PATCH berfungsi: badan yang membawa `title` sahaja
// tak boleh menyentuh apa-apa lagi. Sebelum pembetulan ini, UpdateActivity
// menulis kesebelas-belas lajur tanpa syarat dan medan yang ditinggalkan
// dipadam senyap — dengan jejak audit merekodkannya sebagai disengajakan.
func TestUpdateSeparaTidakMemadamMedanLain(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	if _, err := pool.Exec(ctx, `
		update activities set description = 'Penerangan asal', capacity = 30,
		  fee_cents = 1500, location_address = 'Jalan A', attendance_threshold_pct = 75
		where id = $1`, activityID); err != nil {
		t.Fatalf("sediakan nilai asal: %v", err)
	}

	rec := activityCall(t, pool, manager, http.MethodPatch, "/activities/x",
		`{"title":"Tajuk Baharu"}`, idParam(activityID), (*ActivityHandler).Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	var title, description, address string
	var capacity *int32
	var fee int32
	var threshold int16
	if err := pool.QueryRow(ctx, `
		select title, description, location_address, capacity, fee_cents, attendance_threshold_pct
		from activities where id = $1`, activityID).
		Scan(&title, &description, &address, &capacity, &fee, &threshold); err != nil {
		t.Fatalf("baca semula: %v", err)
	}

	if title != "Tajuk Baharu" {
		t.Errorf("title = %q, mahu diubah", title)
	}
	if description != "Penerangan asal" {
		t.Errorf("description = %q, mahu kekal", description)
	}
	if address != "Jalan A" {
		t.Errorf("location_address = %q, mahu kekal", address)
	}
	if capacity == nil || *capacity != 30 {
		t.Errorf("capacity = %v, mahu kekal 30", capacity)
	}
	if fee != 1500 {
		t.Errorf("fee_cents = %d, mahu kekal 1500", fee)
	}
	if threshold != 75 {
		t.Errorf("attendance_threshold_pct = %d, mahu kekal 75", threshold)
	}
}

// null eksplisit ialah "kosongkan", berbeza daripada tiada langsung.
func TestUpdateNullEksplisitMengosongkanLajurNullable(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	if _, err := pool.Exec(ctx,
		`update activities set capacity = 30, registration_opens_at = now() where id = $1`,
		activityID); err != nil {
		t.Fatalf("sediakan nilai asal: %v", err)
	}

	rec := activityCall(t, pool, manager, http.MethodPatch, "/activities/x",
		`{"capacity":null,"registration_opens_at":null}`, idParam(activityID),
		(*ActivityHandler).Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	var capacity *int32
	var opensAt *time.Time
	if err := pool.QueryRow(ctx,
		`select capacity, registration_opens_at from activities where id = $1`, activityID).
		Scan(&capacity, &opensAt); err != nil {
		t.Fatalf("baca semula: %v", err)
	}
	if capacity != nil {
		t.Errorf("capacity = %v, mahu NULL", *capacity)
	}
	if opensAt != nil {
		t.Errorf("registration_opens_at = %v, mahu NULL", *opensAt)
	}
}

// null pada lajur NOT NULL ialah ralat klien, bukan "kosongkan".
func TestUpdateNullPadaLajurWajibDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	rec := activityCall(t, pool, manager, http.MethodPatch, "/activities/x",
		`{"title":null}`, idParam(activityID), (*ActivityHandler).Update)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
	}
}

// ---- Peralihan status ----

func TestPublishHanyaDaripadaDraf(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	rec := activityCall(t, pool, manager, http.MethodPost, "/activities/x/publish", "",
		idParam(activityID), (*ActivityHandler).Publish)
	if rec.Code != http.StatusOK {
		t.Fatalf("terbit pertama: status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	rec = activityCall(t, pool, manager, http.MethodPost, "/activities/x/publish", "",
		idParam(activityID), (*ActivityHandler).Publish)
	if rec.Code != http.StatusConflict {
		t.Errorf("terbit kedua: status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}
}

func TestCancelPerluSebabDanTolakUlangan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	rec := activityCall(t, pool, manager, http.MethodPost, "/activities/x/cancel", `{}`,
		idParam(activityID), (*ActivityHandler).Cancel)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("tanpa sebab: status = %d, mahu 400", rec.Code)
	}

	rec = activityCall(t, pool, manager, http.MethodPost, "/activities/x/cancel",
		`{"reason":"padang banjir"}`, idParam(activityID), (*ActivityHandler).Cancel)
	if rec.Code != http.StatusOK {
		t.Fatalf("batal pertama: status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	rec = activityCall(t, pool, manager, http.MethodPost, "/activities/x/cancel",
		`{"reason":"padang banjir"}`, idParam(activityID), (*ActivityHandler).Cancel)
	if rec.Code != http.StatusConflict {
		t.Errorf("batal kedua: status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}
}

// ---- Sesi berkehadiran ----

// Kehadiran ialah bukti yang menyokong sijil: penggantian set sesi mesti
// gagal dengan 409, bukan memadamnya melalui cascade.
func TestReplaceSessionsMenolakSesiBerkehadiran(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	member := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivity(t, pool)

	var registrationID, sessionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		insert into activity_registrations (activity_id, user_id, checkin_token)
		values ($1, $2, $3) returning id`,
		activityID, member, uuid.NewString()).Scan(&registrationID); err != nil {
		t.Fatalf("seed pendaftaran: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`select id from activity_sessions where activity_id = $1`, activityID).
		Scan(&sessionID); err != nil {
		t.Fatalf("baca sesi: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into activity_attendances (session_id, registration_id, method, marked_by)
		values ($1, $2, 'manual', $3)`, sessionID, registrationID, manager); err != nil {
		t.Fatalf("seed kehadiran: %v", err)
	}

	body := `{"sessions":[{"seq":1,"starts_at":"2026-10-01T08:00:00Z","ends_at":"2026-10-01T10:00:00Z"}]}`
	rec := activityCall(t, pool, manager, http.MethodPut, "/activities/x/sessions", body,
		idParam(activityID), (*ActivityHandler).ReplaceSessions)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}

	var attendances int
	if err := pool.QueryRow(ctx,
		`select count(*) from activity_attendances where session_id = $1`, sessionID).
		Scan(&attendances); err != nil {
		t.Fatal(err)
	}
	if attendances != 1 {
		t.Errorf("%d kehadiran tinggal, mahu 1 (cascade tak boleh berlaku)", attendances)
	}
}

// Rentetan kosong eksplisit pada lajur yang konsepnya wajib. Tiada CHECK
// panjang di DB untuk title/location_name, jadi kalau semakan ini hilang
// tiada apa yang menahan ” daripada ditulis.
func TestUpdateRentetanKosongPadaMedanWajibDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	cases := []struct{ name, body string }{
		{"title kosong", `{"title":""}`},
		{"title ruang kosong", `{"title":"   "}`},
		{"location_name kosong", `{"location_name":""}`},
		{"location_name ruang kosong", `{"location_name":"\t "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := activityCall(t, pool, manager, http.MethodPatch, "/activities/x", tc.body,
				idParam(activityID), (*ActivityHandler).Update)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
			}
		})
	}

	// Tiada satu pun daripadanya boleh menyentuh baris yang tersimpan.
	var title, locationName string
	if err := pool.QueryRow(ctx,
		`select title, location_name from activities where id = $1`, activityID).
		Scan(&title, &locationName); err != nil {
		t.Fatalf("baca semula: %v", err)
	}
	if title != "Ujian Aktiviti" {
		t.Errorf("title = %q, mahu kekal", title)
	}
	if locationName != "Dewan A" {
		t.Errorf("location_name = %q, mahu kekal", locationName)
	}
}

// ---- Deep-link notifikasi (Task 11b, Bahagian B) ----

// notifikasiUntuk baca baris notifikasi satu penerima mengikut jenis.
//
// notifyMembers menulis dalam goroutine latar (kontraknya: selepas komit,
// tidak pernah dalam transaksi), jadi bacaan ditinjau sehingga tempoh tamat
// dan bukan sekali sahaja. Tempoh tamat, bukan tidur tetap: ujian yang
// lulus sepatutnya laju.
func notifikasiUntuk(
	t *testing.T, pool *pgxpool.Pool, recipientID uuid.UUID, notifType string,
) (activityID, certificateID *uuid.UUID, jumpa bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var a, cid *uuid.UUID
		err := pool.QueryRow(context.Background(), `
			select activity_id, certificate_id from notifications
			where recipient_id = $1 and type = $2
			order by created_at desc limit 1`, recipientID, notifType).Scan(&a, &cid)
		if err == nil {
			return a, cid, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("baca notifikasi: %v", err)
		}
		if time.Now().After(deadline) {
			return nil, nil, false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Notifikasi aktiviti mesti boleh diketuk. Setiap jenis lain dalam jadual
// deep-link melalui post_id; tanpa activity_id, notifikasi aktiviti jadi
// satu-satunya item mati dalam senarai bercampur.
func TestTerbitAktivitiMenulisNotifikasiDenganActivityID(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	penerima := seedUsers(t, pool, 1)[0]
	activityID := seedActivity(t, pool)

	rec := activityCall(t, pool, manager, http.MethodPost, "/activities/x/publish", "",
		idParam(activityID), (*ActivityHandler).Publish)
	if rec.Code != http.StatusOK {
		t.Fatalf("terbit: status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	gotActivity, gotCert, jumpa := notifikasiUntuk(t, pool, penerima, "activity_published")
	if !jumpa {
		t.Fatal("tiada baris notifikasi activity_published untuk penerima")
	}
	if gotActivity == nil || *gotActivity != activityID {
		t.Errorf("activity_id = %v, mahu %v", gotActivity, activityID)
	}
	if gotCert != nil {
		t.Errorf("certificate_id = %v, mahu null untuk activity_published", gotCert)
	}

	// Lajur yang diisi tetapi tidak dihantar kepada klien tidak
	// menyelesaikan apa-apa: senarai notifikasi ialah tempat ketukan itu
	// berlaku.
	rec = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/notifications", nil)
	c.Set("userID", penerima)
	NewNotificationHandler(pool).List(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("senarai notifikasi: status = %d (badan: %s)", rec.Code, rec.Body.String())
	}
	var senarai struct {
		Notifications []notificationResponse `json:"notifications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &senarai); err != nil {
		t.Fatalf("nyahkod senarai notifikasi (%s): %v", rec.Body.String(), err)
	}
	if len(senarai.Notifications) == 0 {
		t.Fatal("senarai notifikasi kosong")
	}
	item := senarai.Notifications[0]
	if item.Type != "activity_published" {
		t.Fatalf("jenis = %q, mahu activity_published", item.Type)
	}
	if item.ActivityID == nil || *item.ActivityID != activityID.String() {
		t.Errorf("respons activity_id = %v, mahu %s", item.ActivityID, activityID)
	}
}

func TestBatalAktivitiMenulisNotifikasiDenganActivityID(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	activityID := seedActivityWithCapacity(t, pool, 10)
	penerima := seedUsers(t, pool, 1)[0]
	if _, err := registerTx(ctx, pool, activityID, penerima); err != nil {
		t.Fatalf("daftar: %v", err)
	}

	rec := activityCall(t, pool, manager, http.MethodPost, "/activities/x/cancel",
		`{"reason":"padang banjir"}`, idParam(activityID), (*ActivityHandler).Cancel)
	if rec.Code != http.StatusOK {
		t.Fatalf("batal: status = %d, badan = %s", rec.Code, rec.Body.String())
	}

	gotActivity, gotCert, jumpa := notifikasiUntuk(t, pool, penerima, "activity_cancelled")
	if !jumpa {
		t.Fatal("tiada baris notifikasi activity_cancelled untuk penerima berdaftar")
	}
	if gotActivity == nil || *gotActivity != activityID {
		t.Errorf("activity_id = %v, mahu %v", gotActivity, activityID)
	}
	if gotCert != nil {
		t.Errorf("certificate_id = %v, mahu null untuk activity_cancelled", gotCert)
	}
}

// ---- Perangkap fee_cents ----

// Tiada dalam modul ini yang boleh memungut yuran: RegisterForActivity
// menetapkan payment_status='not_required' tanpa syarat, dan klausa
// kelayakan sijil `(a.fee_cents = 0 or r.payment_status = 'paid')` menjadi
// palsu untuk SETIAP pendaftar aktiviti berbayar — jadi
// ListEligibleForCertificate memulangkan sifar baris dan pengurus nampak
// "0 sijil diterbitkan" tanpa sebarang ralat. Satu-satunya tempat perkara
// itu boleh dihentikan ialah di pintu masuk.
func TestCreateMenolakYuranBukanSifar(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	var categoryID uuid.UUID
	if err := pool.QueryRow(ctx,
		`select id from activity_categories where key = 'badminton'`).Scan(&categoryID); err != nil {
		t.Fatalf("kategori seed tiada: %v", err)
	}

	body := func(fee int) string {
		return fmt.Sprintf(`{
			"category_id": %q,
			"title": "Aktiviti Berbayar",
			"location_name": "Dewan A",
			"registration_closes_at": "2026-09-01T00:00:00Z",
			"fee_cents": %d,
			"sessions": [{"seq":1,"starts_at":"2026-09-01T08:00:00Z","ends_at":"2026-09-01T10:00:00Z"}]
		}`, categoryID, fee)
	}

	rec := activityCall(t, pool, manager, http.MethodPost, "/activities", body(1500),
		nil, (*ActivityHandler).Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
	}
	if msg, _ := decodeBody(t, rec)["error"].(string); !strings.Contains(msg, "pembayaran") {
		t.Errorf("mesej = %q, mahu menyebut integrasi pembayaran", msg)
	}

	// Dan tiada baris yang tercicir daripada percubaan yang ditolak.
	var n int
	if err := pool.QueryRow(ctx,
		`select count(*) from activities where title = 'Aktiviti Berbayar'`).Scan(&n); err != nil {
		t.Fatalf("kira aktiviti: %v", err)
	}
	if n != 0 {
		t.Errorf("aktiviti tercipta = %d, mahu 0", n)
	}

	// fee_cents = 0 (dan yang tiada langsung) kekal dibenarkan.
	rec = activityCall(t, pool, manager, http.MethodPost, "/activities", body(0),
		nil, (*ActivityHandler).Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("yuran sifar: status = %d, mahu 201 (badan: %s)", rec.Code, rec.Body.String())
	}
	if id, ok := decodeBody(t, rec)["id"].(string); ok {
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `delete from activities where id = $1`, id)
		})
	}
}

func TestUpdateMenolakYuranBukanSifar(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")
	activityID := seedActivity(t, pool)

	rec := activityCall(t, pool, manager, http.MethodPatch, "/activities/x",
		`{"fee_cents":2000}`, idParam(activityID), (*ActivityHandler).Update)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
	}
	if msg, _ := decodeBody(t, rec)["error"].(string); !strings.Contains(msg, "pembayaran") {
		t.Errorf("mesej = %q, mahu menyebut integrasi pembayaran", msg)
	}

	var fee int32
	if err := pool.QueryRow(ctx,
		`select fee_cents from activities where id = $1`, activityID).Scan(&fee); err != nil {
		t.Fatalf("baca semula: %v", err)
	}
	if fee != 0 {
		t.Errorf("fee_cents = %d, mahu kekal 0 selepas PATCH ditolak", fee)
	}

	// Baris warisan yang SUDAH berbayar mesti kekal boleh dibetulkan —
	// itulah sebab semakan ini menyekat medan yang DIHANTAR dan bukan nilai
	// hasil gabungan.
	if _, err := pool.Exec(ctx,
		`update activities set fee_cents = 1500 where id = $1`, activityID); err != nil {
		t.Fatalf("tetapkan yuran warisan: %v", err)
	}
	rec = activityCall(t, pool, manager, http.MethodPatch, "/activities/x",
		`{"fee_cents":0}`, idParam(activityID), (*ActivityHandler).Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("pembetulan ke 0: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
}
