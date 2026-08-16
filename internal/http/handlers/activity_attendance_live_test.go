package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
)

// ---- Helper seed ----

// seedSession tambah satu sesi kepada aktiviti sedia ada dengan tetingkap
// masa yang ditentukan pemanggil — itulah yang membolehkan ujian meletakkan
// sesi di dalam atau di luar tetingkap check-in.
//
// seq dikira daripada sesi sedia ada kerana unique(activity_id, seq):
// seedActivityWithCapacity sudah memasukkan seq 1, jadi seq tetap akan
// berlanggar pada pemanggil kedua.
//
// Dikongsi dengan Task 9 — jangan tukar tandatangan tanpa periksa pemanggil
// lain.
func seedSession(t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID, start, end time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var sessionID uuid.UUID
	err := pool.QueryRow(ctx, `
		insert into activity_sessions (activity_id, seq, title, starts_at, ends_at)
		values ($1,
		  (select coalesce(max(seq), 0) + 1 from activity_sessions where activity_id = $1),
		  'Sesi Ujian', $2, $3)
		returning id`, activityID, start, end).Scan(&sessionID)
	if err != nil {
		t.Fatalf("seed sesi: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from activity_sessions where id = $1`, sessionID)
	})
	return sessionID
}

// attendanceAuditRows baca jejak audit kehadiran untuk satu pendaftaran.
// entity_id ialah id baris kehadiran, yang hilang selepas Unmark — jadi
// carian dibuat melalui new_values/old_values.registration_id.
func attendanceAuditRows(t *testing.T, pool *pgxpool.Pool, registrationID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		select action, old_values, new_values, actor_member_id, actor_role_key, ip_address
		from audit_logs
		where entity_type = 'activity_attendance'
		  and coalesce(new_values ->> 'registration_id', old_values ->> 'registration_id') = $1::text
		order by id`, registrationID.String())
	if err != nil {
		t.Fatalf("query audit kehadiran: %v", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var action string
		var oldJSON, newJSON []byte
		var memberID, roleKey, ip *string
		if err := rows.Scan(&action, &oldJSON, &newJSON, &memberID, &roleKey, &ip); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		var oldV, newV map[string]any
		_ = json.Unmarshal(oldJSON, &oldV)
		_ = json.Unmarshal(newJSON, &newV)
		out = append(out, map[string]any{
			"action": action, "old": oldV, "new": newV,
			"actor_member_id": memberID, "actor_role_key": roleKey, "ip": ip,
		})
	}
	return out
}

// seedKehadiran sediakan gabungan yang hampir setiap ujian di bawah perlukan:
// aktiviti + sesi dalam tetingkap + seorang ahli berdaftar.
func seedKehadiran(t *testing.T, pool *pgxpool.Pool) (activityID, sessionID uuid.UUID, reg sqlc.ActivityRegistration) {
	t.Helper()
	activityID = seedActivityWithCapacity(t, pool, 10)
	sessionID = seedSession(t, pool, activityID,
		time.Now().Add(-30*time.Minute), time.Now().Add(time.Hour))
	userID := seedUsers(t, pool, 1)[0]

	var err error
	reg, err = registerTx(context.Background(), pool, activityID, userID)
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	return activityID, sessionID, reg
}

// ---- markAttendanceTx ----

func TestTandaKehadiranDiLuarTetingkapDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// Sesi yang tamat tiga hari lalu — jauh di luar padding 2 jam.
	activityID := seedActivityWithCapacity(t, pool, 10)
	sessionID := seedSession(t, pool, activityID,
		time.Now().Add(-72*time.Hour), time.Now().Add(-70*time.Hour))
	userID := seedUsers(t, pool, 1)[0]
	reg, err := registerTx(ctx, pool, activityID, userID)
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}

	_, err = markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual", audit.Actor{UserID: userID}, nil)
	if !errors.Is(err, errOutsideCheckinWindow) {
		t.Errorf("err = %v, mahu errOutsideCheckinWindow", err)
	}
}

func TestTandaKehadiranDuaKaliIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	_, sessionID, reg := seedKehadiran(t, pool)

	first, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "scan", audit.Actor{UserID: reg.UserID}, nil)
	if err != nil {
		t.Fatalf("tanda pertama: %v", err)
	}
	if !first.Created {
		t.Error("tanda pertama sepatutnya mencipta baris")
	}

	// QR dipegang di depan lens menghantar permintaan berulang. Yang kedua
	// bukan ralat — ia hanya tiada kerja, dan UI perlu tahu bezanya supaya
	// ia boleh menunjukkan "sudah hadir" berbanding "✓ baru ditanda".
	second, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "scan", audit.Actor{UserID: reg.UserID}, nil)
	if err != nil {
		t.Fatalf("tanda kedua: %v", err)
	}
	if second.Created {
		t.Error("tanda kedua sepatutnya melaporkan Created=false")
	}

	q := sqlc.New(pool)
	n, _ := q.CountAttendanceByRegistration(ctx, reg.ID)
	if n != 1 {
		t.Errorf("baris kehadiran = %d, mahu 1", n)
	}

	// Satu tanda = satu catatan audit. Imbasan berulang tidak boleh
	// membanjiri jejak dengan baris yang tak mewakili sebarang perubahan.
	if entries := attendanceAuditRows(t, pool, reg.ID); len(entries) != 1 {
		t.Errorf("catatan audit = %d, mahu 1", len(entries))
	}
}

// Tiada FK yang merentasi kedua-dua hubungan: `activity_attendances` merujuk
// registration dan session secara berasingan, jadi tiada apa dalam skema
// menghalang kehadiran aktiviti A direkod atas sesi aktiviti B. Semakan itu
// hidup dalam markAttendanceTx sahaja — kalau ia hilang, kiraan kelayakan
// sijil akan terlebih kira secara senyap.
func TestTandaKehadiranSesiAktivitiLain(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	_, _, reg := seedKehadiran(t, pool)

	// Aktiviti kedua, sesi kedua — pendaftaran di atas langsung tiada
	// kaitan dengannya.
	lainID := seedActivityWithCapacity(t, pool, 10)
	sesiLain := seedSession(t, pool, lainID,
		time.Now().Add(-30*time.Minute), time.Now().Add(time.Hour))

	if _, err := markAttendanceTx(ctx, pool, sesiLain, reg.ID, "manual",
		audit.Actor{UserID: reg.UserID}, nil); !errors.Is(err, errNotRegistered) {
		t.Errorf("err = %v, mahu errNotRegistered", err)
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}
}

// Kunci baris aktiviti ialah separuh lagi bagi interlock yang
// replaceSessionsAudited (Task 6) sudah pegang. activity_attendances.session_id
// ialah `on delete cascade`, jadi check-in yang commit di tengah-tengah
// penggantian sesi hilang tanpa jejak. Kunci pada SATU belah sahaja tidak
// menutup lubang itu.
//
// Ujian memegang `for update` atas baris aktiviti — persis apa yang laluan
// penggantian sesi buat — dan menuntut markAttendanceTx TERSEKAT di situ.
func TestTandaKehadiranMenungguKunciAktiviti(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessionID, reg := seedKehadiran(t, pool)

	// Tanpa ini pgxpool mewujudkan sambungan secara malas dan pemegang
	// kunci di bawah boleh menghabiskan kerjanya sebelum markAttendanceTx
	// sempat memintanya — ujian yang lulus tanpa sebarang pertandingan.
	warmPool(t, pool, 4)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mula transaksi pemegang: %v", err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx,
		`select 1 from activities where id = $1 for update`, activityID); err != nil {
		t.Fatalf("kunci aktiviti: %v", err)
	}

	tersekat, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()

	_, err = markAttendanceTx(tersekat, pool, sessionID, reg.ID, "scan", audit.Actor{UserID: reg.UserID}, nil)
	if err == nil {
		t.Fatal("tanda berjaya semasa baris aktiviti dikunci — " +
			"markAttendanceTx tidak mengambil LockActivityForRegistration")
	}
	if tersekat.Err() == nil {
		t.Fatalf("tanda gagal atas sebab lain, bukan kerana menunggu kunci: %v", err)
	}

	// Lepaskan kunci: tanda yang sama mesti berjaya sekarang.
	_ = holder.Rollback(ctx)
	res, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "scan", audit.Actor{UserID: reg.UserID}, nil)
	if err != nil {
		t.Fatalf("tanda selepas kunci dilepaskan: %v", err)
	}
	if !res.Created {
		t.Error("tanda selepas kunci dilepaskan sepatutnya mencipta baris")
	}
}

// Pendaftaran yang dibatalkan bukan lagi pendaftaran — kehadirannya akan
// menjadi bukti untuk sijil yang ahli itu sudah tarik diri daripadanya.
func TestTandaKehadiranPendaftaranDibatalkan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessionID, reg := seedKehadiran(t, pool)

	q := sqlc.New(pool)
	if _, err := q.CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID, UserID: reg.UserID,
	}); err != nil {
		t.Fatalf("batal: %v", err)
	}

	if _, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual",
		audit.Actor{UserID: reg.UserID}, nil); !errors.Is(err, errNotRegistered) {
		t.Errorf("err = %v, mahu errNotRegistered", err)
	}
}

// ---- Handler ----

func attendanceCall(
	t *testing.T,
	pool *pgxpool.Pool,
	callerID uuid.UUID,
	method, target string,
	params gin.Params,
	body any,
	fn func(*AttendanceHandler, *gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode badan: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("userID", callerID)

	fn(NewAttendanceHandler(pool), c)
	return rec
}

func attendanceParams(activityID, sessionID uuid.UUID) gin.Params {
	return gin.Params{
		{Key: "id", Value: activityID.String()},
		{Key: "sid", Value: sessionID.String()},
	}
}

// Kehadiran ialah bukti yang menentukan siapa dapat sijil — ahli biasa tidak
// boleh menandanya, untuk dirinya mahupun untuk orang lain.
func TestTandaKehadiranPerluPengurusan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessionID, reg := seedKehadiran(t, pool)

	rec := attendanceCall(t, pool, reg.UserID, http.MethodPost, "/attendance",
		attendanceParams(activityID, sessionID),
		gin.H{"registration_id": reg.ID.String(), "method": "manual"},
		(*AttendanceHandler).Mark)
	if rec.Code != http.StatusForbidden {
		t.Errorf("ahli biasa: status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}
}

// Skrin senarai menghantar registration_id, scanner menghantar token QR.
// Kedua-duanya laluan yang sama; hanya method dan hasil audit berbeza.
func TestTandaKehadiranMelaluiTokenDanID(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	t.Run("token dari QR", func(t *testing.T) {
		activityID, sessionID, reg := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"checkin_token": reg.CheckinToken, "method": "scan"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusOK {
			t.Fatalf("imbas pertama: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
		}
		body := decodeBody(t, rec)
		if body["created"] != true {
			t.Errorf("created = %v, mahu true", body["created"])
		}
		member, ok := body["member"].(map[string]any)
		if !ok || member["member_id"] == nil {
			t.Errorf("member = %v, mahu objek dengan member_id", body["member"])
		}

		// Imbasan kedua: masih 200 dan masih hijau di UI, cuma created=false.
		rec = attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"checkin_token": reg.CheckinToken, "method": "scan"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusOK {
			t.Fatalf("imbas kedua: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
		}
		if body := decodeBody(t, rec); body["created"] != false {
			t.Errorf("created imbasan kedua = %v, mahu false", body["created"])
		}

		q := sqlc.New(pool)
		if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 1 {
			t.Errorf("baris kehadiran = %d, mahu 1", n)
		}

		// Jejak audit mesti membawa snapshot pelaku, bukan hanya user id.
		entries := attendanceAuditRows(t, pool, reg.ID)
		if len(entries) != 1 {
			t.Fatalf("catatan audit = %d, mahu 1", len(entries))
		}
		if entries[0]["action"] != "create" {
			t.Errorf("action = %v, mahu create", entries[0]["action"])
		}
		if entries[0]["actor_role_key"] == nil || entries[0]["actor_member_id"] == nil {
			t.Errorf("audit tanpa snapshot pelaku: %v", entries[0])
		}
	})

	t.Run("registration_id dari skrin senarai", func(t *testing.T) {
		activityID, sessionID, reg := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"registration_id": reg.ID.String(), "method": "manual"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
		}
		if body := decodeBody(t, rec); body["created"] != true {
			t.Errorf("created = %v, mahu true", body["created"])
		}
	})

	t.Run("token tidak dikenali", func(t *testing.T) {
		activityID, sessionID, _ := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"checkin_token": "token-yang-tidak-wujud", "method": "scan"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("kedua-dua pengenalan sekali", func(t *testing.T) {
		activityID, sessionID, reg := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"registration_id": reg.ID.String(), "checkin_token": reg.CheckinToken, "method": "scan"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("tiada pengenalan langsung", func(t *testing.T) {
		activityID, sessionID, _ := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"method": "manual"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
		}
	})
}

// Pemetaan ralat: setiap satu mesti sampai kepada klien sebagai kodnya
// sendiri, bukan 500.
func TestTandaKehadiranHandlerMemetakanRalat(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	t.Run("di luar tetingkap", func(t *testing.T) {
		activityID := seedActivityWithCapacity(t, pool, 10)
		sessionID := seedSession(t, pool, activityID,
			time.Now().Add(-72*time.Hour), time.Now().Add(-70*time.Hour))
		userID := seedUsers(t, pool, 1)[0]
		reg, err := registerTx(ctx, pool, activityID, userID)
		if err != nil {
			t.Fatalf("daftar: %v", err)
		}

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"registration_id": reg.ID.String(), "method": "manual"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, mahu 422 (badan: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("pendaftaran aktiviti lain", func(t *testing.T) {
		_, _, reg := seedKehadiran(t, pool)
		lainID := seedActivityWithCapacity(t, pool, 10)
		sesiLain := seedSession(t, pool, lainID,
			time.Now().Add(-30*time.Minute), time.Now().Add(time.Hour))

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(lainID, sesiLain),
			gin.H{"registration_id": reg.ID.String(), "method": "manual"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("sesi tidak dijumpai", func(t *testing.T) {
		activityID, _, reg := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, uuid.New()),
			gin.H{"registration_id": reg.ID.String(), "method": "manual"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("method tidak sah", func(t *testing.T) {
		activityID, sessionID, reg := seedKehadiran(t, pool)

		rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
			attendanceParams(activityID, sessionID),
			gin.H{"registration_id": reg.ID.String(), "method": "telepati"},
			(*AttendanceHandler).Mark)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
		}
	})
}

func TestBuangKehadiran(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	activityID, sessionID, reg := seedKehadiran(t, pool)
	params := append(attendanceParams(activityID, sessionID),
		gin.Param{Key: "rid", Value: reg.ID.String()})

	// Belum ditanda → 404.
	rec := attendanceCall(t, pool, manager, http.MethodDelete, "/attendance", params, nil,
		(*AttendanceHandler).Unmark)
	if rec.Code != http.StatusNotFound {
		t.Errorf("buang tanpa kehadiran: status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}

	if _, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual",
		audit.Actor{UserID: manager}, nil); err != nil {
		t.Fatalf("tanda: %v", err)
	}

	// Ahli biasa tidak boleh membuang bukti kehadiran.
	rec = attendanceCall(t, pool, reg.UserID, http.MethodDelete, "/attendance", params, nil,
		(*AttendanceHandler).Unmark)
	if rec.Code != http.StatusForbidden {
		t.Errorf("ahli biasa: status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}

	rec = attendanceCall(t, pool, manager, http.MethodDelete, "/attendance", params, nil,
		(*AttendanceHandler).Unmark)
	if rec.Code != http.StatusOK {
		t.Fatalf("buang: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}

	// Buang dua kali → 404, bukan 200 senyap.
	rec = attendanceCall(t, pool, manager, http.MethodDelete, "/attendance", params, nil,
		(*AttendanceHandler).Unmark)
	if rec.Code != http.StatusNotFound {
		t.Errorf("buang kedua: status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}

	// Membuang kehadiran memadam bukti — jejaknya mesti kekal.
	entries := attendanceAuditRows(t, pool, reg.ID)
	if len(entries) != 2 {
		t.Fatalf("catatan audit = %d, mahu 2 (create + delete)", len(entries))
	}
	if entries[1]["action"] != "delete" {
		t.Errorf("action kedua = %v, mahu delete", entries[1]["action"])
	}
	if entries[1]["actor_role_key"] == nil {
		t.Errorf("audit buang tanpa snapshot role: %v", entries[1])
	}
}

// ---- Pindaan di luar tetingkap (Task 11b, Bahagian A) ----

// seedKehadiranLuarTetingkap — aktiviti dengan sesi yang tamat tiga hari
// lalu (jauh di luar padding 2 jam) dan seorang ahli berdaftar. Itulah
// keadaan yang laluan pindaan wujud untuknya: kehadiran yang terlepas
// ditanda semasa sesi berjalan.
func seedKehadiranLuarTetingkap(
	t *testing.T, pool *pgxpool.Pool,
) (activityID, sessionID uuid.UUID, reg sqlc.ActivityRegistration) {
	t.Helper()
	activityID = seedActivityWithCapacity(t, pool, 10)
	sessionID = seedSession(t, pool, activityID,
		time.Now().Add(-72*time.Hour), time.Now().Add(-70*time.Hour))
	userID := seedUsers(t, pool, 1)[0]

	var err error
	reg, err = registerTx(context.Background(), pool, activityID, userID)
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	return activityID, sessionID, reg
}

// Pindaan berjaya di luar tetingkap DAN meninggalkan jejak yang boleh
// dibezakan daripada check-in biasa. Pembeza itu (amendment + reason) ialah
// keseluruhan justifikasi bagi membenarkan tetingkap dilangkau, jadi ia
// disahkan lawan jadual audit_logs dan bukan hanya kod respons.
func TestPindaanKehadiranDiLuarTetingkapBerjayaDanDiaudit(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	activityID, sessionID, reg := seedKehadiranLuarTetingkap(t, pool)

	rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
		attendanceParams(activityID, sessionID),
		gin.H{"registration_id": reg.ID.String(), "method": "manual",
			"amend": true, "reason": "Ahli hadir tetapi telefon kehabisan bateri"},
		(*AttendanceHandler).Mark)
	if rec.Code != http.StatusOK {
		t.Fatalf("pindaan: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["created"] != true {
		t.Errorf("created = %v, mahu true", body["created"])
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 1 {
		t.Errorf("baris kehadiran = %d, mahu 1", n)
	}

	entries := attendanceAuditRows(t, pool, reg.ID)
	if len(entries) != 1 {
		t.Fatalf("catatan audit = %d, mahu 1", len(entries))
	}
	newValues, _ := entries[0]["new"].(map[string]any)
	if newValues["amendment"] != true {
		t.Errorf("new.amendment = %v, mahu true — pindaan yang kelihatan seperti "+
			"check-in biasa dalam jejak lebih buruk daripada tiada laluan pindaan",
			newValues["amendment"])
	}
	if newValues["reason"] != "Ahli hadir tetapi telefon kehabisan bateri" {
		t.Errorf("new.reason = %v, mahu sebab yang dihantar", newValues["reason"])
	}

	// Laluan token QR menerima pindaan juga: pindaan ialah pembetulan kepada
	// laluan yang SAMA, bukan endpoint lain.
	activityID2, sessionID2, reg2 := seedKehadiranLuarTetingkap(t, pool)
	rec = attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
		attendanceParams(activityID2, sessionID2),
		gin.H{"checkin_token": reg2.CheckinToken, "method": "scan",
			"amend": true, "reason": "scanner mati semasa sesi"},
		(*AttendanceHandler).Mark)
	if rec.Code != http.StatusOK {
		t.Fatalf("pindaan melalui token: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
	entries2 := attendanceAuditRows(t, pool, reg2.ID)
	if len(entries2) != 1 {
		t.Fatalf("catatan audit (token) = %d, mahu 1", len(entries2))
	}
	if nv, _ := entries2[0]["new"].(map[string]any); nv["amendment"] != true {
		t.Errorf("new.amendment (token) = %v, mahu true", nv["amendment"])
	}
}

// Sebab wajib. Pindaan tanpa sebab ialah tepat perkara yang jejak audit
// sepatutnya halang — jadi ia ditolak SEBELUM sebarang baris kehadiran
// wujud, bukan selepas.
func TestPindaanKehadiranTanpaSebabDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	for nama, badan := range map[string]gin.H{
		"tiada reason":      {"amend": true},
		"reason kosong":     {"amend": true, "reason": ""},
		"reason ruang":      {"amend": true, "reason": "   "},
		"reason baris baru": {"amend": true, "reason": "\n\t "},
	} {
		t.Run(nama, func(t *testing.T) {
			activityID, sessionID, reg := seedKehadiranLuarTetingkap(t, pool)

			badan["registration_id"] = reg.ID.String()
			badan["method"] = "manual"
			rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
				attendanceParams(activityID, sessionID), badan,
				(*AttendanceHandler).Mark)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, mahu 400 (badan: %s)", rec.Code, rec.Body.String())
			}

			q := sqlc.New(pool)
			if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
				t.Errorf("baris kehadiran = %d, mahu 0 — 400 yang datang selepas "+
					"baris dicipta tiada nilai", n)
			}
		})
	}
}

// `amend` bukan pintu belakang keizinan: ia melangkau tetingkap masa, bukan
// semakan pengurusan.
func TestPindaanKehadiranOlehBukanPengurusanDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessionID, reg := seedKehadiranLuarTetingkap(t, pool)

	rec := attendanceCall(t, pool, reg.UserID, http.MethodPost, "/attendance",
		attendanceParams(activityID, sessionID),
		gin.H{"registration_id": reg.ID.String(), "method": "manual",
			"amend": true, "reason": "saya memang hadir"},
		(*AttendanceHandler).Mark)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}
}

// Tanpa `amend`, tetingkap masih dikuatkuasakan seperti sebelum ini —
// laluan pindaan tidak boleh melonggarkan laluan biasa.
func TestTanpaPindaanDiLuarTetingkapMasih422(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	activityID, sessionID, reg := seedKehadiranLuarTetingkap(t, pool)

	for nama, badan := range map[string]gin.H{
		"tiada medan amend": {"registration_id": reg.ID.String(), "method": "manual"},
		"amend palsu": {"registration_id": reg.ID.String(), "method": "manual",
			"amend": false, "reason": "sebab yang diabaikan"},
	} {
		t.Run(nama, func(t *testing.T) {
			rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
				attendanceParams(activityID, sessionID), badan,
				(*AttendanceHandler).Mark)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, mahu 422 (badan: %s)", rec.Code, rec.Body.String())
			}
		})
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}
}

// Pindaan melangkau SATU semakan sahaja. Semakan
// sesi-dan-pendaftaran-milik-aktiviti-sama kekal — tanpanya, kiraan
// kelayakan sijil terlebih kira secara senyap.
func TestPindaanKehadiranMasihTertaklukSemakanAktiviti(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	manager := seedMember(t, ctx, pool, "manager", "approved")

	_, _, reg := seedKehadiranLuarTetingkap(t, pool)

	// Aktiviti kedua dengan sesinya sendiri, juga di luar tetingkap.
	lainID := seedActivityWithCapacity(t, pool, 10)
	sesiLain := seedSession(t, pool, lainID,
		time.Now().Add(-72*time.Hour), time.Now().Add(-70*time.Hour))

	amend := &attendanceAmendment{Reason: "cuba pindaan silang aktiviti"}
	if _, err := markAttendanceTx(ctx, pool, sesiLain, reg.ID, "manual",
		audit.Actor{UserID: manager}, amend); !errors.Is(err, errNotRegistered) {
		t.Errorf("err = %v, mahu errNotRegistered", err)
	}

	// Laluan HTTP: pendaftaran aktiviti A atas laluan aktiviti B → 409.
	rec := attendanceCall(t, pool, manager, http.MethodPost, "/attendance",
		attendanceParams(lainID, sesiLain),
		gin.H{"registration_id": reg.ID.String(), "method": "manual",
			"amend": true, "reason": "cuba pindaan silang aktiviti"},
		(*AttendanceHandler).Mark)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	if n, _ := q.CountAttendanceByRegistration(ctx, reg.ID); n != 0 {
		t.Errorf("baris kehadiran = %d, mahu 0", n)
	}
}

// Pendaftaran yang dibatalkan kekal ditolak walaupun melalui pindaan.
func TestPindaanKehadiranPendaftaranDibatalkanDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessionID, reg := seedKehadiranLuarTetingkap(t, pool)
	if _, err := sqlc.New(pool).CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID, UserID: reg.UserID,
	}); err != nil {
		t.Fatalf("batal: %v", err)
	}

	amend := &attendanceAmendment{Reason: "pindaan atas pendaftaran yang ditarik"}
	if _, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual",
		audit.Actor{UserID: reg.UserID}, amend); !errors.Is(err, errNotRegistered) {
		t.Errorf("err = %v, mahu errNotRegistered", err)
	}
}
