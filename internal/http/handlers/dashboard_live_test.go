package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// dashboardTestFeeCents - fi tetap utk semua panggilan callDashboard dlm
// fail ni. Sengaja BUKAN 100 (gatewayChargeCents yang minePayments guna
// melalui NewPaymentsHandler(pool, 100)) - dua nombor tak berkaitan, guna
// nilai lain mengelak salah anggap ia mesti sama.
const dashboardTestFeeCents = 1000

// TestDashboardOpenActivitiesKecualikanYangSudahDidaftar - open_activities
// senaraikan aktiviti terbitan yang pemanggil BELUM daftar; sebaik sahaja
// dia daftar, ia hilang daripada senarai itu.
func TestDashboardOpenActivitiesKecualikanYangSudahDidaftar(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivity(t, pool)

	// Terbitkan DAN alih ke masa depan supaya ia layak muncul dalam
	// open_activities. seedActivity guna tarikh tetap 2026-09-01 yang
	// kini (2026-09-05) sudah lepas - query dashboard menapis
	// `ends_at >= now()`, jadi tanpa ini ujian gagal walaupun logik
	// betul. seedActivity dikongsi 15 lokasi lain, jadi tarikhnya tidak
	// diubah - ujian ini sahaja yang mengalihkannya.
	if _, err := pool.Exec(ctx,
		`update activities set status = 'published',
		   starts_at = now() + interval '1 day',
		   ends_at = now() + interval '1 day 2 hours'
		 where id = $1`, activityID); err != nil {
		t.Fatalf("terbitkan aktiviti: %v", err)
	}

	_, sebelum := callDashboard(t, pool, userID)
	if !mengandungiAktiviti(sebelum, "open_activities", activityID) {
		t.Fatalf("open_activities sepatutnya mengandungi aktiviti yang belum didaftar")
	}

	// checkin_token wajib (NOT NULL, tiada default) - lihat
	// seedRegistrationFor (registration_cancel_live_test.go:44).
	if _, err := pool.Exec(ctx,
		`insert into activity_registrations (activity_id, user_id, status, checkin_token)
		 values ($1, $2, 'registered', $3)`, activityID, userID, uuid.NewString()); err != nil {
		t.Fatalf("daftar: %v", err)
	}

	_, selepas := callDashboard(t, pool, userID)
	if mengandungiAktiviti(selepas, "open_activities", activityID) {
		t.Errorf("open_activities masih mengandungi aktiviti yang sudah didaftar")
	}
}

// mengandungiAktiviti - `open_activities` membawa id aktiviti di bawah
// kunci `id`. (Senarai `upcoming_registrations` dibuang daripada endpoint
// ini apabila skrin Utama berhenti memaparkannya; `/my-activities` kekal
// tempat kanonik untuk pendaftaran sendiri.)
func mengandungiAktiviti(body map[string]any, senarai string, activityID uuid.UUID) bool {
	member, ok := body["member"].(map[string]any)
	if !ok {
		return false
	}
	items, ok := member[senarai].([]any)
	if !ok {
		return false
	}
	const kunci = "id"
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && item[kunci] == activityID.String() {
			return true
		}
	}
	return false
}

// callDashboard panggil handler terus (corak minePayments dalam
// my_payments_live_test.go) - tiada middleware, jadi ujian 403 di bawah
// menguji gate handler, BUKAN RequireApprovedStatus.
func callDashboard(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	c.Set("userID", userID)

	// 1000 sen - padan REGISTRATION_FEE_CENTS lalai (config.go) dan fi
	// yang minePayments (my_payments_live_test.go) guna secara tak
	// langsung melalui NewPaymentsHandler(pool, 100) gatewayChargeCents -
	// nilai berbeza, medan berbeza; angka ni cuma perlu konsisten dalam
	// fail ujian ni sendiri.
	NewDashboardHandler(pool, dashboardTestFeeCents).Get(c)

	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("nyahsiri respons: %v (badan: %s)", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestDashboardAhliBiasaDapatBlokMemberTanpaAdmin(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	code, body := callDashboard(t, pool, userID)
	if code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %v", code, body)
	}
	if body["admin"] != nil {
		t.Errorf("admin = %v, mahu null untuk ahli biasa", body["admin"])
	}
	member, ok := body["member"].(map[string]any)
	if !ok {
		t.Fatalf("member bukan objek: %v", body["member"])
	}
	for _, key := range []string{"certificates_total", "total_members"} {
		if _, ada := member[key]; !ada {
			t.Errorf("member tiada medan %q", key)
		}
	}
	// Dibuang apabila skrin Utama berhenti memaparkannya - kekal
	// ditegaskan supaya ia tidak menyelinap balik sebagai muatan mati.
	for _, key := range []string{"unread_notifications", "upcoming_registrations"} {
		if _, ada := member[key]; ada {
			t.Errorf("member masih bawa medan %q yang sepatutnya dibuang", key)
		}
	}
	membership, ok := member["membership"].(map[string]any)
	if !ok {
		t.Fatalf("membership bukan objek: %v", member["membership"])
	}
	if membership["status"] != "approved" {
		t.Errorf("membership.status = %v, mahu approved", membership["status"])
	}
}

func TestDashboardTotalMembersKiraAhliApproved(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	_, sebelum := callDashboard(t, pool, userID)
	asal := sebelum["member"].(map[string]any)["total_members"].(float64)

	seedMember(t, ctx, pool, "ahli", "approved")
	seedMember(t, ctx, pool, "ahli", "pending") // TIDAK dikira

	_, selepas := callDashboard(t, pool, userID)
	kini := selepas["member"].(map[string]any)["total_members"].(float64)

	if kini != asal+1 {
		t.Errorf("total_members = %v, mahu %v (pending tidak sepatutnya dikira)", kini, asal+1)
	}
}

func TestDashboardBlokAdminIkutRole(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	kes := []struct {
		roleKey  string
		mahuBlok bool
	}{
		{"ahli", false},
		{"supervisor", false},
		{"manager", false},
		{"admin", true},
		{"superadmin", true},
	}
	for _, k := range kes {
		t.Run(k.roleKey, func(t *testing.T) {
			userID := seedMember(t, ctx, pool, k.roleKey, "approved")
			code, body := callDashboard(t, pool, userID)
			if code != http.StatusOK {
				t.Fatalf("kod = %d, mahu 200", code)
			}
			ada := body["admin"] != nil
			if ada != k.mahuBlok {
				t.Errorf("admin ada = %v, mahu %v untuk role %q", ada, k.mahuBlok, k.roleKey)
			}
		})
	}
}

func TestDashboardPendingApprovalsKiraAhliPending(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	adminID := seedMember(t, ctx, pool, "admin", "approved")

	_, sebelum := callDashboard(t, pool, adminID)
	asal := sebelum["admin"].(map[string]any)["pending_approvals"].(float64)

	seedMember(t, ctx, pool, "ahli", "pending")

	_, selepas := callDashboard(t, pool, adminID)
	kini := selepas["admin"].(map[string]any)["pending_approvals"].(float64)

	if kini != asal+1 {
		t.Errorf("pending_approvals = %v, mahu %v", kini, asal+1)
	}
}

// TestDashboardAttendanceRateBukanNull - bukti hujung-ke-hujung bagi
// laluan attendanceRateToFloat (dashboard.go) yang sebelum ini tidak
// pernah dilalui ujian: sesi SUDAH TAMAT dalam bulan semasa + 1
// pendaftaran aktif + 1 kehadiran = attendance_rate 1.0, bukan nil.
// seedActivity guna tarikh tetap 2026-09-01 yang kini (2026-09-05)
// sudah lepas BULAN INI juga (Sept), jadi tarikh asalnya sudah cukup
// untuk lajur registered_at/checked_in_at (default now()), tetapi sesi
// itu sendiri mesti dialihkan eksplisit supaya ia SUDAH TAMAT (ends_at
// < now()) dan MASIH dalam bulan semasa (ends_at >= awal bulan) - dua
// syarat query ActivityStatsThisMonth.
func TestDashboardAttendanceRateBukanNull(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID := seedActivity(t, pool)

	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx,
		`select id from activity_sessions where activity_id = $1`, activityID).
		Scan(&sessionID); err != nil {
		t.Fatalf("cari sesi seed: %v", err)
	}

	// Alih sesi supaya ia SUDAH TAMAT tetapi masih dalam bulan semasa -
	// bukan default seedActivity (2026-09-01, sudah lepas tapi masih
	// sah utk ujian ini sebenarnya) - eksplisit di sini supaya ujian
	// tidak bergantung diam-diam pada tarikh tetap seedActivity, ikut
	// corak Task 2 (TestDashboardOpenActivitiesKecualikanYangSudahDidaftar).
	if _, err := pool.Exec(ctx,
		`update activity_sessions set starts_at = now() - interval '3 hours',
		   ends_at = now() - interval '1 hour'
		 where id = $1`, sessionID); err != nil {
		t.Fatalf("alih sesi: %v", err)
	}

	memberID := seedMember(t, ctx, pool, "ahli", "approved")

	var registrationID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activity_registrations (activity_id, user_id, status, checkin_token)
		 values ($1, $2, 'registered', $3) returning id`,
		activityID, memberID, uuid.NewString()).Scan(&registrationID); err != nil {
		t.Fatalf("seed pendaftaran: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into activity_attendances (registration_id, session_id, method)
		 values ($1, $2, 'manual')`, registrationID, sessionID); err != nil {
		t.Fatalf("seed kehadiran: %v", err)
	}

	adminID := seedMember(t, ctx, pool, "admin", "approved")
	code, body := callDashboard(t, pool, adminID)
	if code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %v", code, body)
	}
	admin, ok := body["admin"].(map[string]any)
	if !ok {
		t.Fatalf("admin bukan objek: %v", body["admin"])
	}
	activityStats, ok := admin["activity_stats"].(map[string]any)
	if !ok {
		t.Fatalf("activity_stats bukan objek: %v", admin["activity_stats"])
	}
	rate, ok := activityStats["attendance_rate"].(float64)
	if !ok {
		t.Fatalf("attendance_rate bukan nombor (mahu bukan-null): %v", activityStats["attendance_rate"])
	}
	if rate < 0 || rate > 1 {
		t.Errorf("attendance_rate = %v, mahu dalam julat 0..1", rate)
	}
}

func TestDashboardAhliPendingDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	pendingID := seedMember(t, ctx, pool, "ahli", "pending")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Tiru rantaian kumpulan `approved` dalam router.go - RequireAuth
	// digantikan dengan suntikan userID terus supaya ujian ini menguji
	// SATU perkara sahaja: gate status.
	g := r.Group("/", func(c *gin.Context) { c.Set("userID", pendingID) },
		middleware.RequireApprovedStatus(sqlc.New(pool)))
	g.GET("/dashboard", NewDashboardHandler(pool, dashboardTestFeeCents).Get)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("kod = %d, mahu 403. Badan: %s", rec.Code, rec.Body.String())
	}
}

// TestDashboardDermaSuperadminSahaja - DATABASE.md mengunci data derma
// kepada superadmin dalam /admin/payments; kad kutipan dashboard mesti
// menghormati sekatan yang sama - admin biasa tidak sepatutnya menerima
// donation_cents, walaupun secara tidak langsung menerusi total_cents.
func TestDashboardDermaSuperadminSahaja(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	adminID := seedMember(t, ctx, pool, "admin", "approved")
	superID := seedMember(t, ctx, pool, "superadmin", "approved")
	penderma := seedMember(t, ctx, pool, "ahli", "approved")
	seedDonation(t, pool, &penderma, "succeeded", 5000)

	_, badanAdmin := callDashboard(t, pool, adminID)
	revAdmin := badanAdmin["admin"].(map[string]any)["revenue_this_month"].(map[string]any)
	if revAdmin["donation_cents"] != nil {
		t.Errorf("donation_cents = %v untuk admin, mahu null", revAdmin["donation_cents"])
	}

	_, badanSuper := callDashboard(t, pool, superID)
	revSuper := badanSuper["admin"].(map[string]any)["revenue_this_month"].(map[string]any)
	if revSuper["donation_cents"] == nil {
		t.Fatalf("donation_cents null untuk superadmin, mahu angka")
	}
	if revSuper["donation_cents"].(float64) < 5000 {
		t.Errorf("donation_cents = %v, mahu >= 5000", revSuper["donation_cents"])
	}

	// total_cents admin MESTI mengecualikan derma yang superadmin nampak.
	if revAdmin["total_cents"].(float64) >= revSuper["total_cents"].(float64) {
		t.Errorf("total admin (%v) sepatutnya kurang drpd total superadmin (%v)",
			revAdmin["total_cents"], revSuper["total_cents"])
	}
}

// TestDashboardKutipanAbaikanBayaranGagal - hanya derma berstatus
// 'succeeded' dikira; 'failed'/'pending' tidak sepatutnya menyentuh
// jumlah kutipan bulan ini.
func TestDashboardKutipanAbaikanBayaranGagal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	superID := seedMember(t, ctx, pool, "superadmin", "approved")
	penderma := seedMember(t, ctx, pool, "ahli", "approved")

	_, sebelum := callDashboard(t, pool, superID)
	asal := sebelum["admin"].(map[string]any)["revenue_this_month"].(map[string]any)["donation_cents"].(float64)

	seedDonation(t, pool, &penderma, "failed", 9900)
	seedDonation(t, pool, &penderma, "pending", 8800)

	_, selepas := callDashboard(t, pool, superID)
	kini := selepas["admin"].(map[string]any)["revenue_this_month"].(map[string]any)["donation_cents"].(float64)

	if kini != asal {
		t.Errorf("donation_cents = %v, mahu kekal %v (failed/pending tidak dikira)", kini, asal)
	}
}

// TestDashboardYuranTertunggakPadanMePayments - /dashboard dan
// /me/payments MESTI beri jawapan SAMA utk "ahli ni terhutang yuran
// pendaftaran ke tidak" - kedua-dua panggil outstandingRegistrationFee
// (payments.go), satu-satunya tempat logik itu dikira (Task 5 brief).
//
// Unit BERBEZA dengan sengaja (lihat komen OutstandingRegistrationFeeCents,
// dashboard.go): /me/payments pulangkan boolean
// (`outstanding_registration_fee`), /dashboard pulangkan *sen* atau null
// (`membership.outstanding_registration_fee_cents`) supaya kad boleh papar
// jumlah tanpa panggilan kedua. Jadi cross-check ni banding SEMANTIK
// "terhutang?" (nil/bukan-nil vs true/false), bukan nilai mentah yang
// jenisnya memang tak boleh sama (int vs bool) - membanding mentah akan
// SENTIASA gagal walau logik betul, jadi itu bukan ujian yang berguna.
func TestDashboardYuranTertunggakPadanMePayments(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// seedNonExemptMember - staff_id = placeholder (== user_id::text,
	// macam backfill migrasi isi utk baris lama) DAN belum disahkan -
	// satu-satunya bentuk profil yang staffFeeExempt (profile.go) anggap
	// TAK exempt. seedMember (guna di tempat lain dlm fail ni) sengaja
	// sentiasa staff_id_verified_at=now(), jadi ia SENTIASA exempt dan
	// tak boleh guna utk uji cabang "terhutang=true" di sini.
	seedNonExemptMember := func(t *testing.T, status string) uuid.UUID {
		t.Helper()
		var userID uuid.UUID
		e := "yuran-" + uuid.NewString() + "@test.local"
		if err := pool.QueryRow(ctx,
			`insert into users (email, password_hash) values ($1, 'x') returning id`,
			e).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`insert into profiles (user_id, staff_id, role_id, status)
			 values ($1, $2::text, (select id from roles where key = 'ahli'), $3)`,
			userID, userID, status); err != nil {
			t.Fatalf("seed profile tak-exempt: %v", err)
		}
		return userID
	}

	kes := []struct {
		nama              string
		seed              func(t *testing.T) uuid.UUID
		terhutangDijangka bool
	}{
		{"ahli staff-exempt (staff id disahkan) - tak terhutang", func(t *testing.T) uuid.UUID {
			return seedMember(t, ctx, pool, "ahli", "approved")
		}, false},
		// status "approved" (BUKAN "pending") dengan sengaja - /dashboard
		// duduk di belakang RequireApprovedStatus (router.go), jadi
		// seorang ahli "pending" tak PERNAH sampai endpoint ni dalam
		// produksi. callDashboard panggil handler terus tanpa middleware
		// itu, jadi ujian dengan status "pending" akan lulus tanpa
		// membuktikan apa-apa tentang gelagat sebenar - baris lama
		// pra-migrasi (staff_id placeholder, belum disahkan) MEMANG boleh
		// "approved" DAN terhutang (lihat komen staffFeeExempt,
		// profile.go), jadi itulah keadaan sebenar yang perlu diuji.
		{"ahli belum exempt, belum bayar - terhutang", func(t *testing.T) uuid.UUID {
			return seedNonExemptMember(t, "approved")
		}, true},
	}

	for _, k := range kes {
		t.Run(k.nama, func(t *testing.T) {
			userID := k.seed(t)

			_, dash := callDashboard(t, pool, userID)
			membership := dash["member"].(map[string]any)["membership"].(map[string]any)
			dariDashboard := membership["outstanding_registration_fee_cents"]
			dashTerhutang := dariDashboard != nil

			me := minePayments(t, pool, userID)
			dariPayments, ada := me["outstanding_registration_fee"]
			if !ada {
				t.Fatalf("/me/payments tiada medan outstanding_registration_fee - kunci sudah bertukar?")
			}
			paymentsTerhutang, ok := dariPayments.(bool)
			if !ok {
				t.Fatalf("outstanding_registration_fee bukan boolean: %T (%v)", dariPayments, dariPayments)
			}

			if dashTerhutang != paymentsTerhutang {
				t.Errorf("dashboard terhutang=%v (cents=%v), /me/payments terhutang=%v - dua sumber kebenaran menyimpang",
					dashTerhutang, dariDashboard, paymentsTerhutang)
			}
			if paymentsTerhutang != k.terhutangDijangka {
				t.Fatalf("fixture rosak: /me/payments terhutang=%v, mahu %v - semakan silang di atas tak bermakna",
					paymentsTerhutang, k.terhutangDijangka)
			}
			if dashTerhutang {
				sen, ok := dariDashboard.(float64)
				if !ok || int64(sen) != dashboardTestFeeCents {
					t.Errorf("outstanding_registration_fee_cents = %v, mahu %d", dariDashboard, dashboardTestFeeCents)
				}
			}
		})
	}
}

// TestDashboardYuranTertunggakGunaJumlahBilPending - jumlah yang
// dipaparkan MESTI ikut `amount_cents` baris 'pending' SEDIA ADA
// (snapshot pada masa bil dicipta - registration_payment.go), BUKAN
// `REGISTRATION_FEE_CENTS` semasa. Simulasi keadaan sebenar yang
// mendorong pembaikan ni: ahli pegang bil ToyyibPay lama bercaj RM10
// (1000 sen), konfigurasi fi kemudian bertukar ke RM20 (2000 sen) -
// dashboard mesti terus papar RM10 (jumlah yang AKAN dicaj bil
// sedia ada), bukan RM20 (angka yang tak pernah dicaj kepada ahli ni).
func TestDashboardYuranTertunggakGunaJumlahBilPending(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// Ahli belum-exempt (staff_id placeholder, sama corak
	// seedNonExemptMember di atas) supaya outstandingRegistrationFee
	// pulangkan true - fokus ujian ni ialah JUMLAH, bukan "owes?".
	var userID uuid.UUID
	e := "yuran-bil-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		e).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2::text, (select id from roles where key = 'ahli'), 'approved')`,
		userID, userID); err != nil {
		t.Fatalf("seed profil: %v", err)
	}

	const bilLamaCents = 1000
	const konfigFeeCentsBaharu = 2000
	if _, err := pool.Exec(ctx,
		`insert into registration_payments (user_id, amount_cents, currency, gateway, status)
		 values ($1, $2, 'myr', 'toyyibpay', 'pending')`,
		userID, bilLamaCents); err != nil {
		t.Fatalf("seed bil pending: %v", err)
	}

	// Handler tersendiri di sini (bukan callDashboard, yang guna
	// dashboardTestFeeCents tetap) supaya fi KONFIGURASI semasa boleh
	// diset secara eksplisit BEZA drpd jumlah bil (`bilLamaCents`) -
	// itulah keseluruhan perkara yang diuji.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	c.Set("userID", userID)
	NewDashboardHandler(pool, konfigFeeCentsBaharu).Get(c)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("nyahsiri respons: %v (badan: %s)", err, rec.Body.String())
	}

	membership := body["member"].(map[string]any)["membership"].(map[string]any)
	sen, ok := membership["outstanding_registration_fee_cents"].(float64)
	if !ok {
		t.Fatalf("outstanding_registration_fee_cents bukan nombor: %v", membership["outstanding_registration_fee_cents"])
	}
	if int64(sen) != bilLamaCents {
		t.Errorf("outstanding_registration_fee_cents = %v, mahu %d (jumlah BIL PENDING, bukan fi konfigurasi %d)",
			sen, bilLamaCents, konfigFeeCentsBaharu)
	}
}
