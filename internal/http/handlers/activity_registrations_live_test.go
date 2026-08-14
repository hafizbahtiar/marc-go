package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
)

// ---- Helper seed ----

// seedActivityWithCapacity cipta aktiviti DITERBITKAN dengan kapasiti
// tertentu dan tetingkap pendaftaran yang masih terbuka.
//
// Status 'published' dan registration_closes_at pada masa hadapan bukan
// hiasan: tanpa kedua-duanya setiap ujian pendaftaran akan gagal pada
// semakan "aktiviti belum dibuka"/"pendaftaran ditutup" dan bukan pada
// perkara yang ia niat uji.
//
// Dikongsi dengan ujian kehadiran/sijil (Task 8-9) — jangan tukar
// tandatangan tanpa periksa pemanggil lain.
func seedActivityWithCapacity(t *testing.T, pool *pgxpool.Pool, capacity int) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var categoryID uuid.UUID
	if err := pool.QueryRow(ctx,
		`select id from activity_categories where key = 'badminton'`).Scan(&categoryID); err != nil {
		t.Fatalf("kategori seed tiada — jalankan migration atas DB ujian: %v", err)
	}

	start := time.Now().Add(720 * time.Hour)
	closesAt := time.Now().Add(24 * time.Hour)

	var activityID uuid.UUID
	err := pool.QueryRow(ctx, `
		insert into activities (category_id, title, location_name, starts_at, ends_at,
		  registration_closes_at, capacity, status)
		values ($1, 'Ujian Pendaftaran', 'Dewan B', $2, $3, $4, $5, 'published')
		returning id`,
		categoryID, start, start.Add(2*time.Hour), closesAt, capacity).Scan(&activityID)
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

// seedUsers cipta n pengguna approved (users + profiles), ikut corak
// seedMember dalam profile_status_live_test.go, dan buang semuanya semula
// melalui t.Cleanup — DB ujian dikongsi antara ujian dalam pakej ini.
//
// Dikongsi dengan Task 8-9 — jangan tukar tandatangan tanpa periksa
// pemanggil lain.
func seedUsers(t *testing.T, pool *pgxpool.Pool, n int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()

	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, seedMember(t, ctx, pool, "ahli", "approved"))
	}

	t.Cleanup(func() {
		// profiles/pendaftaran ikut `on delete cascade` daripada users.
		_, _ = pool.Exec(context.Background(), `delete from users where id = any($1)`, ids)
	})
	return ids
}

// warmPool paksa pgxpool mewujudkan n sambungan sebelum ujian perlumbaan
// bermula, dan pulangkan semula ke pool. Kalau n melebihi had pool, ia
// dikepit — meminta lebih daripada MaxConns akan tergantung.
func warmPool(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	if max := int(pool.Config().MaxConns); n > max {
		n = max
	}
	conns := make([]*pgxpool.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("panaskan pool: %v", err)
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		conn.Release()
	}
}

// ---- registerTx ----

// Ujian paling penting dalam modul ini. Tanpa ia, `select ... for update`
// dalam registerTx hanya niat baik — tiada apa yang membuktikan dua ahli
// tidak boleh merebut slot terakhir yang sama.
func TestRegisterPerlumbaanSlotTerakhir(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID := seedActivityWithCapacity(t, pool, 1)
	users := seedUsers(t, pool, 8)

	// Panaskan pool DAHULU. pgxpool mencipta sambungan secara malas, dan
	// beberapa milisaat untuk mewujudkan setiap satu sudah cukup untuk
	// menyerikan goroutine secara tak sengaja — perlumbaan yang tak pernah
	// berlaku, dan ujian yang lulus atas sebab yang salah.
	warmPool(t, pool, len(users))

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		berjaya int
		penuh   int
	)
	// Gerbang mula: tanpa ia, goroutine pertama sempat habis sebelum yang
	// kelapan dilancarkan dan ujian ini "lulus" tanpa sebarang perlumbaan.
	mula := make(chan struct{})
	for _, uid := range users {
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			<-mula
			_, err := registerTx(ctx, pool, activityID, uid)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				berjaya++
			case errors.Is(err, errActivityFull):
				penuh++
			default:
				t.Errorf("ralat tak dijangka: %v", err)
			}
		}(uid)
	}
	close(mula)
	wg.Wait()

	if berjaya != 1 {
		t.Errorf("pendaftaran berjaya = %d, mahu tepat 1", berjaya)
	}
	if penuh != len(users)-1 {
		t.Errorf("ditolak 'penuh' = %d, mahu %d", penuh, len(users)-1)
	}

	// Semakan kedua terhadap DB — kaunter dalam-memori boleh menipu.
	q := sqlc.New(pool)
	n, err := q.CountActiveRegistrations(ctx, activityID)
	if err != nil {
		t.Fatalf("CountActiveRegistrations: %v", err)
	}
	if n != 1 {
		t.Errorf("baris pendaftaran dalam DB = %d, mahu 1", n)
	}
}

func TestDaftarSemulaSelepasBatal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	activityID := seedActivityWithCapacity(t, pool, 5)
	userID := seedUsers(t, pool, 1)[0]

	if _, err := registerTx(ctx, pool, activityID, userID); err != nil {
		t.Fatalf("daftar pertama: %v", err)
	}
	// Daftar dua kali mesti ditolak oleh indeks unik separa.
	if _, err := registerTx(ctx, pool, activityID, userID); !errors.Is(err, errAlreadyRegistered) {
		t.Fatalf("daftar kedua = %v, mahu errAlreadyRegistered", err)
	}

	q := sqlc.New(pool)
	if _, err := q.CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID, UserID: userID,
	}); err != nil {
		t.Fatalf("batal: %v", err)
	}

	// Selepas batal, unik SEPARA membenarkan pendaftaran baharu.
	if _, err := registerTx(ctx, pool, activityID, userID); err != nil {
		t.Errorf("daftar semula selepas batal: %v", err)
	}
}

// Token check-in menentukan siapa boleh ditandakan hadir, dan kehadiran
// menentukan siapa dapat sijil — jadi ia mesti legap dan tak berulang.
func TestCheckinTokenUnikDanLegap(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	activityID := seedActivityWithCapacity(t, pool, 5)
	users := seedUsers(t, pool, 3)

	seen := make(map[string]bool, len(users))
	for _, uid := range users {
		reg, err := registerTx(ctx, pool, activityID, uid)
		if err != nil {
			t.Fatalf("daftar: %v", err)
		}
		if len(reg.CheckinToken) < 32 {
			t.Errorf("checkin_token = %q, terlalu pendek untuk legap", reg.CheckinToken)
		}
		if strings.Contains(reg.CheckinToken, uid.String()) ||
			strings.Contains(reg.CheckinToken, activityID.String()) {
			t.Errorf("checkin_token %q boleh diteka daripada id", reg.CheckinToken)
		}
		if seen[reg.CheckinToken] {
			t.Errorf("checkin_token berulang: %q", reg.CheckinToken)
		}
		seen[reg.CheckinToken] = true
	}
}

// Aktiviti draf/dibatalkan tidak boleh menerima pendaftaran, dan tetingkap
// pendaftaran yang sudah tutup mesti menolak.
func TestRegisterTxMenghormatiStatusDanTetingkap(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	t.Run("status draf", func(t *testing.T) {
		activityID := seedActivityWithCapacity(t, pool, 5)
		setActivityStatusDirect(t, pool, activityID, "draft")
		userID := seedUsers(t, pool, 1)[0]
		if _, err := registerTx(ctx, pool, activityID, userID); !errors.Is(err, errActivityNotOpen) {
			t.Errorf("err = %v, mahu errActivityNotOpen", err)
		}
	})

	t.Run("pendaftaran sudah tutup", func(t *testing.T) {
		activityID := seedActivityWithCapacity(t, pool, 5)
		if _, err := pool.Exec(ctx,
			`update activities set registration_closes_at = now() - interval '1 hour' where id = $1`,
			activityID); err != nil {
			t.Fatalf("tutup pendaftaran: %v", err)
		}
		userID := seedUsers(t, pool, 1)[0]
		if _, err := registerTx(ctx, pool, activityID, userID); !errors.Is(err, errRegistrationClosed) {
			t.Errorf("err = %v, mahu errRegistrationClosed", err)
		}
	})

	t.Run("pendaftaran belum buka", func(t *testing.T) {
		activityID := seedActivityWithCapacity(t, pool, 5)
		if _, err := pool.Exec(ctx,
			`update activities set registration_opens_at = now() + interval '1 hour' where id = $1`,
			activityID); err != nil {
			t.Fatalf("tunda pembukaan: %v", err)
		}
		userID := seedUsers(t, pool, 1)[0]
		if _, err := registerTx(ctx, pool, activityID, userID); !errors.Is(err, errRegistrationClosed) {
			t.Errorf("err = %v, mahu errRegistrationClosed", err)
		}
	})
}

// ---- Handler ----

func registrationCall(
	t *testing.T,
	pool *pgxpool.Pool,
	callerID uuid.UUID,
	method, target string,
	params gin.Params,
	fn func(*RegistrationHandler, *gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("userID", callerID)

	fn(NewRegistrationHandler(pool), c)
	return rec
}

func TestRegisterHandlerMemetakanRalat(t *testing.T) {
	pool := activityTestPool(t)
	activityID := seedActivityWithCapacity(t, pool, 1)
	users := seedUsers(t, pool, 2)

	rec := registrationCall(t, pool, users[0], http.MethodPost, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Register)
	if rec.Code != http.StatusCreated {
		t.Fatalf("daftar pertama: status = %d, mahu 201 (badan: %s)", rec.Code, rec.Body.String())
	}

	rec = registrationCall(t, pool, users[0], http.MethodPost, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Register)
	if rec.Code != http.StatusConflict {
		t.Errorf("daftar berulang: status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}

	// Kapasiti 1 — ahli kedua mesti ditolak 409, bukan 500.
	rec = registrationCall(t, pool, users[1], http.MethodPost, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Register)
	if rec.Code != http.StatusConflict {
		t.Errorf("aktiviti penuh: status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}
}

func TestCancelHandler(t *testing.T) {
	pool := activityTestPool(t)
	activityID := seedActivityWithCapacity(t, pool, 5)
	userID := seedUsers(t, pool, 1)[0]

	// Belum daftar → 404.
	rec := registrationCall(t, pool, userID, http.MethodDelete, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Cancel)
	if rec.Code != http.StatusNotFound {
		t.Errorf("batal tanpa daftar: status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}

	if _, err := registerTx(context.Background(), pool, activityID, userID); err != nil {
		t.Fatalf("daftar: %v", err)
	}
	rec = registrationCall(t, pool, userID, http.MethodDelete, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Cancel)
	if rec.Code != http.StatusOK {
		t.Fatalf("batal: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}

	// Batal dua kali → 404 juga; baris 'cancelled' bukan pendaftaran aktif.
	rec = registrationCall(t, pool, userID, http.MethodDelete, "/activities/x/registration",
		idParam(activityID), (*RegistrationHandler).Cancel)
	if rec.Code != http.StatusNotFound {
		t.Errorf("batal kedua: status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}
}

// Senarai pendaftar mendedahkan nama sebenar ahli lain — pengurusan sahaja.
func TestListForActivityPerluPengurusan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	activityID := seedActivityWithCapacity(t, pool, 5)
	member := seedUsers(t, pool, 1)[0]
	manager := seedMember(t, ctx, pool, "manager", "approved")

	if _, err := registerTx(ctx, pool, activityID, member); err != nil {
		t.Fatalf("daftar: %v", err)
	}

	rec := registrationCall(t, pool, member, http.MethodGet, "/activities/x/registrations",
		idParam(activityID), (*RegistrationHandler).ListForActivity)
	if rec.Code != http.StatusForbidden {
		t.Errorf("ahli biasa: status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}

	rec = registrationCall(t, pool, manager, http.MethodGet, "/activities/x/registrations",
		idParam(activityID), (*RegistrationHandler).ListForActivity)
	if rec.Code != http.StatusOK {
		t.Fatalf("pengurusan: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	regs, ok := body["registrations"].([]any)
	if !ok || len(regs) != 1 {
		t.Errorf("registrations = %v, mahu senarai 1", body["registrations"])
	}
}

// attendedSessionIDs baca medan attended_session_ids satu baris respons dan
// pulangkan ia sebagai set. Type assertion pada []any ialah separuh ujian:
// `null` menyahkod kepada nil dan GAGAL assertion itu — itulah bentuk yang
// akan meletupkan `.map` pada klien Flutter.
func attendedSessionIDs(t *testing.T, row map[string]any) map[string]bool {
	t.Helper()
	raw, ok := row["attended_session_ids"].([]any)
	if !ok {
		t.Fatalf("attended_session_ids = %#v, mahu senarai (bukan null/tiada)",
			row["attended_session_ids"])
	}
	out := make(map[string]bool, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("attended_session_ids mengandungi %#v, mahu string UUID", v)
		}
		out[s] = true
	}
	return out
}

// Skrin kehadiran pengurusan membaca senarai ini untuk MENYEMAI suisnya.
// Tanpa attended_session_ids setiap suis bermula OFF walau siapa pun sudah
// ditanda — dan kerana suis hanya boleh dihidupkan, laluan DELETE
// .../attendance/:rid menjadi kod mati.
//
// Silang-cemar di sini ialah kelas pepijat yang sama seperti pautan sijil
// yang salah: kehadiran seorang ahli ditunjukkan atas nama ahli lain.
func TestListForActivityMembawaKehadiranSetiapSesi(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID := seedActivityWithCapacity(t, pool, 10)
	manager := seedMember(t, ctx, pool, "manager", "approved")

	// Tetingkap check-in terbuka supaya markAttendanceTx tidak ditolak atas
	// sebab masa. Sesi seq-1 daripada seedActivityWithCapacity (720 jam ke
	// hadapan) kekal tanpa kehadiran — ia menguji bahawa sesi tanpa tanda
	// tidak menyelinap masuk.
	now := time.Now()
	sesi1 := seedSession(t, pool, activityID, now.Add(-time.Hour), now.Add(time.Hour))
	sesi2 := seedSession(t, pool, activityID, now.Add(-time.Hour), now.Add(time.Hour))

	users := seedUsers(t, pool, 3)
	regs := make([]sqlc.ActivityRegistration, 0, len(users))
	for _, uid := range users {
		reg, err := registerTx(ctx, pool, activityID, uid)
		if err != nil {
			t.Fatalf("daftar: %v", err)
		}
		regs = append(regs, reg)
	}

	// regs[0]: sesi 1 sahaja. regs[1]: kedua-dua sesi. regs[2]: tiada.
	mark := func(reg sqlc.ActivityRegistration, sessionID uuid.UUID) {
		t.Helper()
		if _, err := markAttendanceTx(ctx, pool, sessionID, reg.ID, "manual",
			audit.Actor{UserID: manager}, nil); err != nil {
			t.Fatalf("tanda kehadiran: %v", err)
		}
	}
	mark(regs[0], sesi1)
	mark(regs[1], sesi1)
	mark(regs[1], sesi2)

	// Ahli biasa tidak boleh melihat senarai ini langsung.
	rec := registrationCall(t, pool, users[0], http.MethodGet, "/activities/x/registrations",
		idParam(activityID), (*RegistrationHandler).ListForActivity)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ahli biasa: status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}

	rec = registrationCall(t, pool, manager, http.MethodGet, "/activities/x/registrations",
		idParam(activityID), (*RegistrationHandler).ListForActivity)
	if rec.Code != http.StatusOK {
		t.Fatalf("pengurusan: status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}

	rows, ok := decodeBody(t, rec)["registrations"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("registrations = %v, mahu senarai 3", decodeBody(t, rec)["registrations"])
	}
	byID := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("baris pendaftaran = %#v, mahu objek", r)
		}
		byID[row["id"].(string)] = row
	}

	mahu := map[string]map[string]bool{
		regs[0].ID.String(): {sesi1.String(): true},
		regs[1].ID.String(): {sesi1.String(): true, sesi2.String(): true},
		regs[2].ID.String(): {},
	}
	for regID, dijangka := range mahu {
		row, ok := byID[regID]
		if !ok {
			t.Fatalf("pendaftaran %s tiada dalam respons", regID)
		}
		dapat := attendedSessionIDs(t, row)
		if len(dapat) != len(dijangka) {
			t.Errorf("pendaftaran %s: attended_session_ids = %v, mahu %v", regID, dapat, dijangka)
			continue
		}
		for sid := range dijangka {
			if !dapat[sid] {
				t.Errorf("pendaftaran %s: sesi %s hilang daripada %v", regID, sid, dapat)
			}
		}
	}

	// Kosong mesti bersiri sebagai [] dan bukan null — klien yang memanggil
	// .map atas null akan terhempas.
	if !strings.Contains(rec.Body.String(), `"attended_session_ids":[]`) {
		t.Errorf("badan tiada `\"attended_session_ids\":[]` untuk pendaftaran tanpa kehadiran: %s",
			rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"attended_session_ids":null`) {
		t.Errorf("attended_session_ids bersiri sebagai null: %s", rec.Body.String())
	}
}

// ListMine mesti menunjukkan pendaftaran aktif sahaja, dan hilang selepas
// ahli membatalkannya.
func TestListMine(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	activityID := seedActivityWithCapacity(t, pool, 5)
	userID := seedUsers(t, pool, 1)[0]

	rec := registrationCall(t, pool, userID, http.MethodGet, "/me/activities", nil,
		(*RegistrationHandler).ListMine)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, badan = %s", rec.Code, rec.Body.String())
	}
	if regs, ok := decodeBody(t, rec)["registrations"].([]any); !ok || len(regs) != 0 {
		t.Errorf("registrations awal = %v, mahu senarai kosong (bukan null)",
			decodeBody(t, rec)["registrations"])
	}

	if _, err := registerTx(ctx, pool, activityID, userID); err != nil {
		t.Fatalf("daftar: %v", err)
	}
	rec = registrationCall(t, pool, userID, http.MethodGet, "/me/activities", nil,
		(*RegistrationHandler).ListMine)
	regs, ok := decodeBody(t, rec)["registrations"].([]any)
	if !ok || len(regs) != 1 {
		t.Fatalf("registrations = %v, mahu senarai 1", decodeBody(t, rec)["registrations"])
	}

	q := sqlc.New(pool)
	if _, err := q.CancelRegistration(ctx, sqlc.CancelRegistrationParams{
		ActivityID: activityID, UserID: userID,
	}); err != nil {
		t.Fatalf("batal: %v", err)
	}
	rec = registrationCall(t, pool, userID, http.MethodGet, "/me/activities", nil,
		(*RegistrationHandler).ListMine)
	if regs, ok := decodeBody(t, rec)["registrations"].([]any); !ok || len(regs) != 0 {
		t.Errorf("selepas batal, registrations = %v, mahu kosong",
			decodeBody(t, rec)["registrations"])
	}
}
