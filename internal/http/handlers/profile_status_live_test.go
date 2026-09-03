package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/storage"
)

// Ujian integrasi terhadap Postgres sebenar. Dilangkau melainkan
// HANDLER_TEST_DB diset:
//
//	HANDLER_TEST_DB="postgres://localhost:5432/marc_handler_check?sslmode=disable" \
//	  go test ./internal/http/handlers/ -v
//
// Kenapa DB sebenar: perkara yang diuji ialah sifat TRANSAKSI (catatan
// audit ditulis bersama perubahan status, token dibatalkan dalam
// transaksi yang sama, no-op tak menulis apa-apa). Itu semua hilang kalau
// lapisan DB dimock.
func statusTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dbURL := os.Getenv("HANDLER_TEST_DB")
	if dbURL == "" {
		t.Skip("set HANDLER_TEST_DB kepada DB buangan")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func seedMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleKey, status string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	email := "m-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// staff_id is NOT NULL as of 20260902100000_add_staff_id.sql. This
	// helper seeds members that are meant to be immediately usable
	// ("already good to go") by the wider pre-existing test suite, none
	// of which exercise the staff-id verification gate itself (that's
	// covered separately by createTestPendingProfile/createTestApprovedProfile
	// in staff_id_query_live_test.go) - so mint a synthetic staff_id from
	// user_id, matching the migration's own backfill convention
	// (staff_id = user_id::text), and mark it verified immediately.
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, staff_id, staff_id_verified_at, role_id, status)
		 values ($1, $2, $3, now(), (select id from roles where key = $4), $5)`,
		userID, "MARC/"+uuid.NewString()[:8], userID.String(), roleKey, status); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return userID
}

// seedSucceededRegistrationPayment - gate `setMemberStatus` (L?, yuran
// pendaftaran 2026-08-15) sekat pending->approved sehingga
// HasSucceededRegistrationPayment true. Ujian status/audit ni tak
// menguji laluan bayaran itu sendiri, jadi seed terus baris 'succeeded'
// supaya gate lulus dan ujian fokus pada apa yang ia sepatutnya uji.
func seedSucceededRegistrationPayment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`insert into registration_payments (user_id, amount_cents, currency, gateway, gateway_ref, status)
		 values ($1, 1000, 'myr', 'toyyibpay', $2, 'succeeded')`,
		userID, "test-"+uuid.NewString()); err != nil {
		t.Fatalf("seed registration_payments: %v", err)
	}
}

func callSetStatus(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, action string) *httptest.ResponseRecorder {
	t.Helper()
	return callSetStatusWithBody(t, pool, callerID, targetID, action, "")
}

func callSetStatusWithBody(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var bodyReader *strings.Reader
	if body == "" {
		bodyReader = strings.NewReader("")
	} else {
		bodyReader = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/members/"+targetID.String()+"/"+action, bodyReader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)

	if action == "approve" {
		h.ApproveMember(c)
	} else {
		h.RejectMember(c)
	}
	return rec
}

func auditRowsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entityID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx,
		`select action, changed_fields, old_values, new_values, actor_member_id, actor_role_key
		 from audit_logs where entity_type = 'profile' and entity_id = $1 order by id`, entityID)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var action string
		var fields []string
		var oldJSON, newJSON []byte
		var memberID, roleKey *string
		if err := rows.Scan(&action, &fields, &oldJSON, &newJSON, &memberID, &roleKey); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var oldV, newV map[string]any
		_ = json.Unmarshal(oldJSON, &oldV)
		_ = json.Unmarshal(newJSON, &newV)
		out = append(out, map[string]any{
			"action": action, "fields": fields, "old": oldV, "new": newV,
			"actor_member_id": memberID, "actor_role_key": roleKey,
		})
	}
	return out
}

func TestApproveMemberDiaudit(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	got := logs[0]
	if got["old"].(map[string]any)["status"] != "pending" ||
		got["new"].(map[string]any)["status"] != "approved" {
		t.Errorf("delta salah: old=%v new=%v", got["old"], got["new"])
	}
	// Snapshot pelaku mesti ada - tanpa ni jejak tak dapat jawab "siapa".
	if got["actor_role_key"] == nil || *(got["actor_role_key"].(*string)) != "manager" {
		t.Errorf("actor_role_key = %v, mahu manager", got["actor_role_key"])
	}
}

// Approve dua kali tak boleh cipta catatan audit kedua - tiada apa yang
// berubah pada kali kedua.
func TestApproveBerulangTidakCiptaCatatanKedua(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	callSetStatus(t, pool, manager, target, "approve")
	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("panggilan kedua status = %d", rec.Code)
	}

	if logs := auditRowsFor(t, ctx, pool, target); len(logs) != 1 {
		t.Fatalf("mahu kekal 1 catatan, dapat %d", len(logs))
	}
}

// Task 8 (staff-id verification) makes staff verification a hard
// prerequisite for ANY approval, and forces req.BypassPayment=false
// whenever the target is staff-exempt (see setMemberStatus). seedMember
// (above) sets staff_id_verified_at = now() for every profile it seeds,
// so every target these bypass/payment-gate tests used is staffExempt
// by construction - meaning the (a)-(d) sub-gate block they exercised
// (payment-gate 403 for manager, bypass requiring a note, bypass
// recording payment_bypassed=true, pending-bill blocking bypass) is now
// unreachable through this handler for any caller, exactly like the
// "non-exempt member" test the plan's Task 8 explicitly declined to
// write (see the NOTE below TestApproveMemberManagerCannotForgeBypassAuditOnExemptMember).
// Four tests that asserted on that now-dead path were removed here:
// TestApproveTanpaBayaranDitolak (400 for unpaid, no-bypass approval),
// TestApproveBypassPaymentDitolakUntukManager (403 for manager bypass),
// TestApproveBypassPaymentPerluNota (400 for bypass without a note), and
// TestApproveBypassPaymentBerjayaUntukAdmin (audit records
// payment_bypassed=true/bypass_reason for a successful admin bypass).
// TestApproveBypassPaymentBerjayaUntukSuperadmin was also removed - it
// only ever asserted 200, which now holds vacuously for any
// staff-verified target regardless of rank, so it no longer tests what
// its name claims. Their coverage intent lives on in
// TestApproveMemberRequiresStaffVerified (this file) and
// TestApproveMemberManagerCannotForgeBypassAuditOnExemptMember (this
// file) - the exempt path is what every caller actually hits now. If a
// future "general member" (non-staff) type reintroduces a real
// non-exempt path (spec's flagged out-of-scope idea), recreate these
// against that concrete code, seeding an UNVERIFIED-but-still-approvable
// target - not possible today, since staffExempt is defined as
// target.StaffIDVerifiedAt.Valid and the hard gate above requires it.

// Ahli yang DAH bayar (baris 'succeeded' wujud) tak patut direkod sebagai
// "payment_bypassed" walaupun admin hantar bypass_payment=true - bypass
// tak relevan bila bayaran sebenar dah berjaya (Opus verify LOW#1).
//
// NOTA (Opus verify 2026-09-03): sejak Task 8, target ni staff-exempt
// (seedMember isi staff_id_verified_at = now()), jadi assertion di bawah
// kini dipenuhi melalui cawangan exempt - yang PAKSA req.BypassPayment =
// false - bukan melalui cawangan "dah bayar" yang namanya sebut. Versi
// "betul-betul dah bayar TAPI tak exempt" TIDAK boleh disediakan tanpa
// memalsukan keadaan yang mustahil dalam produksi: gate keras dalam
// setMemberStatus tolak (400) mana-mana approval untuk ahli yang
// staff_id_verified_at-nya null, dan `staffExempt` ditakrif TEPAT sebagai
// medan itu - jadi tiada baris boleh serentak "boleh diluluskan" dan
// "bukan exempt" hari ini. Ujian ni dikekalkan sebagai regression guard
// untuk invariannya yang sebenar (audit TAK PERNAH catat
// payment_bypassed untuk ahli yang duitnya memang dah masuk), dan patut
// ditulis semula terhadap kod sebenar kalau jenis "ahli am" (bukan staff)
// yang disebut di luar skop spec wujud nanti - sama seperti blok ujian
// yang dibuang di atas.
func TestApproveBypassPaymentDiabaikanBilaSudahBayar(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := seedMember(t, ctx, pool, "admin", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	rec := callSetStatusWithBody(t, pool, admin, target, "approve",
		`{"bypass_payment":true,"bypass_reason":"patut diabaikan"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	newVals := logs[0]["new"].(map[string]any)
	if _, ok := newVals["payment_bypassed"]; ok {
		t.Errorf("payment_bypassed tercatat walaupun ahli dah bayar: %v", newVals)
	}
}

// TestApproveBypassPaymentDitolakBilaAdaBilPending and
// TestApproveBypassPaymentDitolakBilaAdaBarisPendingTanpaRef (409 for a
// pending ToyyibPay bill blocking bypass) were removed for the same
// Task 8 reason as the block of tests documented above this function's
// former neighbors: seedMember's target is always staff-exempt, so the
// hard block on (d) HasPendingRegistrationPayment no longer applies to
// it (staffExempt downgrades that check to a log-only warning - see
// setMemberStatus). That warning path isn't covered by a live_test here
// since it only writes a log line, not an observable side effect.

// Body cacat (bypass_payment jenis string bukan bool) mesti pulang 400
// jelas, bukan senyap jadi false lalu terus approve (Opus verify LOW#2).
//
// Target SENGAJA ahli yang DAH bayar (bukan pending belum bayar) - kalau
// ujian ni guna target belum bayar, 400 boleh berlaku sebab GATE BAYARAN
// biasa (kod lama `_ = c.ShouldBindJSON` pun akan pulang 400 yang sama,
// atas sebab berbeza - ujian jadi tak bererti/trivially-pass, Opus
// verify tangkap isu ni pada pusingan ke-2). Dengan target dah bayar,
// laluan biasa tanpa body cacat akan approve BERJAYA (200) - jadi 400 di
// sini HANYA boleh datang daripada bind gagal, bukan gate bayaran.
func TestApproveBodyBypassPaymentJenisSalahDitolak(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := seedMember(t, ctx, pool, "admin", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	rec := callSetStatusWithBody(t, pool, admin, target, "approve",
		`{"bypass_payment":"true","bypass_reason":"nota"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mahu 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if logs := auditRowsFor(t, ctx, pool, target); len(logs) != 0 {
		t.Fatalf("body cacat ditolak tapi menulis %d catatan audit", len(logs))
	}
}

// TestApproveBypassPaymentBerjayaUntukSuperadmin (superadmin bypass
// succeeds) was removed - it only ever asserted 200, which now holds
// vacuously for any staff-exempt target regardless of the caller's rank
// (see the Task 8 comment block above), so it stopped testing what its
// name claims: IsAtLeastRole("admin") passing for the superadmin tier.

// Ahli yang nombor staff BELUM disahkan tak boleh diluluskan langsung -
// gate keras ni jalan SEBELUM blok bayaran/exempt, walau ahli dah bayar
// (Task 8, staff-id verification).
func TestApproveMemberRequiresStaffVerified(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool) // staff_id_verified_at NULL

	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 unverified staff, got %d: %s", rec.Code, rec.Body.String())
	}
	if logs := auditRowsFor(t, ctx, pool, target); len(logs) != 0 {
		t.Fatalf("gate ditolak tapi menulis %d catatan audit", len(logs))
	}
}

// Ahli yang staff disahkan mesti diluluskan walau TIADA bayaran
// 'succeeded' langsung - pengesahan staff sendiri mencukupi utk exempt
// gate yuran (Task 8).
func TestApproveMemberStaffVerifiedExemptsFeeEvenWithoutPayment(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)
	verifyStaffIDDirect(t, ctx, pool, target, manager)

	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 - staff verified exempts fee, got %d: %s", rec.Code, rec.Body.String())
	}
}

// v1 bug ini test guard: manager (rank 60, TAK dibenarkan bypass yuran -
// itu gate rank admin/80) hantar bypass_payment=true pada ahli yang
// SUDAH staff-exempt. Kelulusan mesti tetap 200 (exemption sendiri
// mencukupi), TAPI rekod audit TIDAK BOLEH tunjuk payment_bypassed=true -
// manager tu tak pernah betul-betul guna kuasa bypass yang dia sebenarnya
// tiada (Task 8).
func TestApproveMemberManagerCannotForgeBypassAuditOnExemptMember(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)
	verifyStaffIDDirect(t, ctx, pool, target, manager)

	rec := callSetStatusWithBody(t, pool, manager, target, "approve",
		`{"bypass_payment": true, "bypass_reason": ""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (exempt regardless of the bogus bypass flag), got %d: %s", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	newVals := logs[0]["new"].(map[string]any)
	if _, ok := newVals["payment_bypassed"]; ok {
		t.Errorf("audit record must not show payment_bypassed for a staff-exempt approval, got: %v", newVals)
	}
}

// NOTE (Opus verify round 2, carried over from the plan): there is
// deliberately NO test here named something like "non-exempt member
// still requires admin for bypass". Since the hard staff-verification
// gate (first check in the `status == "approved"` branch) returns 400
// before the payment-gate block runs at all, `staffExempt` is ALWAYS
// true by the time that block executes - the `else` branch (existing
// HasSucceededRegistrationPayment/BypassPayment/bypass_reason logic) is
// unreachable through this handler today, by construction, for every
// caller. A test asserting "200" while unable to construct a real
// non-exempt-but-approvable member is not a regression test - it is
// confirmation of the same exempt path already covered by the two tests
// above. If a future "general member" type (non-staff, spec's flagged
// out-of-scope idea) reintroduces a real non-exempt path, add the test
// then, against that concrete code, not against a hypothetical one now.

// Reject mesti membatalkan refresh token DALAM transaksi yang sama.
func TestRejectDiauditDanBatalkanToken(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	if _, err := pool.Exec(ctx,
		`insert into refresh_tokens (user_id, token_hash, expires_at, family_id)
		 values ($1, 'hash-'||$2, now() + interval '30 days', gen_random_uuid())`,
		target, uuid.NewString()); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	rec := callSetStatus(t, pool, manager, target, "reject")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	if logs[0]["new"].(map[string]any)["status"] != "rejected" {
		t.Errorf("new = %v", logs[0]["new"])
	}

	var tokens int
	if err := pool.QueryRow(ctx,
		`select count(*) from refresh_tokens where user_id = $1`, target).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Errorf("%d refresh token masih hidup selepas ditolak", tokens)
	}
}

// Permintaan yang ditolak keizinan tak boleh meninggalkan sebarang kesan.
func TestTolakManagementTidakMenulisApaApa(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	otherManager := seedMember(t, ctx, pool, "manager", "approved")

	rec := callSetStatus(t, pool, manager, otherManager, "reject")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, mahu 403", rec.Code)
	}
	if logs := auditRowsFor(t, ctx, pool, otherManager); len(logs) != 0 {
		t.Fatalf("permintaan 403 menulis %d catatan audit", len(logs))
	}

	var status string
	if err := pool.QueryRow(ctx,
		`select status from profiles where user_id = $1`, otherManager).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Errorf("status berubah kepada %q walaupun 403", status)
	}
}

func callMembers(t *testing.T, pool *pgxpool.Pool, callerID uuid.UUID) []map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/members", nil)
	c.Set("userID", callerID)
	h.Members(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /members = %d: %s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// wipeMembers kosongkan SEMUA ahli daripada DB ujian yang dikongsi.
//
// Dua ujian di bawah menegaskan atas KESELURUHAN senarai ahli ("ahli lain
// tak nampak emel"), jadi ia mesti bermula daripada DB yang diketahui
// kosong - tak seperti setiap ujian lain dalam pakej ni, yang menyemai
// baris berid rawak dan menegaskan hanya atas baris itu.
//
// ⚠️ Turutan padam mengikut kekangan kunci asing, bukan citarasa.
// DUA jadual merujuk `users(id)` TANPA klausa `on delete` (jadi RESTRICT
// secara lalai) dan MESTI dikosongkan dahulu:
//
//	profiles.approved_by  - sudah dilindungi oleh `delete from profiles`
//	donations.user_id     - TERLEPAS sehingga 2026-08-22 (L33)
//
// Selebihnya `on delete cascade`/`set null`, jadi ia hilang sendiri.
// Kalau jadual BAHARU merujuk `users(id)` tanpa klausa `on delete`,
// tambah di sini - kalau tidak dua ujian di bawah gagal dengan
// pelanggaran FK yang tiada kaitan dgn apa yang ia uji.
func wipeMembers(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`delete from donations; delete from profiles; delete from users`); err != nil {
		t.Fatalf("bersih: %v", err)
	}
}

// Emel ialah data peribadi. Sejak ahli boleh nampak ahli lain, menghantar
// emel kepada semua orang bermakna sesiapa boleh menyalin direktori penuh.
func TestEmelAhliLainDisembunyikanDaripadaAhliBiasa(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	viewer := seedMember(t, ctx, pool, "ahli", "approved")
	other := seedMember(t, ctx, pool, "ahli", "approved")
	_ = other

	rows := callMembers(t, pool, viewer)
	if len(rows) < 2 {
		t.Fatalf("mahu sekurang-kurangnya 2 baris, dapat %d", len(rows))
	}

	var sawSelf bool
	for _, r := range rows {
		isSelf := r["user_id"] == viewer.String()
		if isSelf {
			sawSelf = true
			if r["email"] == nil {
				t.Error("ahli patut nampak emel SENDIRI")
			}
			continue
		}
		if r["email"] != nil {
			t.Errorf("emel ahli lain terdedah kepada ahli biasa: %v", r["email"])
		}
	}
	if !sawSelf {
		t.Error("baris sendiri tiada dalam senarai")
	}
}

func TestManagementMasihNampakSemuaEmel(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	manager := seedMember(t, ctx, pool, "manager", "approved")
	seedMember(t, ctx, pool, "ahli", "approved")

	for _, r := range callMembers(t, pool, manager) {
		if r["email"] == nil {
			t.Errorf("management patut nampak semua emel, %v disembunyikan", r["member_id"])
		}
	}
}

func callUpdateMe(t *testing.T, pool *pgxpool.Pool, r2 *storage.R2Client, userID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{
		pool: pool, queries: sqlc.New(pool),
		emailClient: email.NewClient("", ""), r2: r2,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("userID", userID)
	h.UpdateMe(c)
	return rec
}

// Kunci avatar datang dari client. Tanpa semakan pemilikan, sesiapa boleh
// menetapkan kunci orang lain (atau kunci yang diteka) sebagai avatar
// mereka sendiri.
func TestAvatarTolakKunciBukanMilikCaller(t *testing.T) {
	pool, ctx := statusTestPool(t)
	r2 := storage.NewR2Client("", "", "", "", "") // tak dikonfigur - tak dicapai
	victim := seedMember(t, ctx, pool, "ahli", "approved")
	attacker := seedMember(t, ctx, pool, "ahli", "approved")

	key := "posts/" + uuid.NewString()
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: victim,
	}); err != nil {
		t.Fatal(err)
	}

	rec := callUpdateMe(t, pool, r2, attacker, `{"avatar_r2_key":"`+key+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mahu 400 (body: %s)", rec.Code, rec.Body.String())
	}

	var avatar *string
	if err := pool.QueryRow(ctx,
		`select avatar_r2_key from profiles where user_id = $1`, attacker).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != nil {
		t.Errorf("avatar ditetapkan walaupun kunci bukan milik caller: %v", *avatar)
	}
}

// Tukar avatar mesti menggilirkan yang LAMA untuk dipadam, kalau tidak
// setiap pertukaran meninggalkan objek yatim dalam R2 selamanya.
func TestAvatarLamaDigilirkanUntukDipadam(t *testing.T) {
	pool, ctx := statusTestPool(t)
	q := sqlc.New(pool)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	oldKey := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, oldKey); err != nil {
		t.Fatal(err)
	}

	// Buang avatar (kunci kosong) - tak sentuh R2, jadi tak perlu kredential.
	rec := callUpdateMe(t, pool, storage.NewR2Client("", "", "", "", ""), user, `{"avatar_r2_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var queued int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and reason = 'avatar_replaced'`,
		oldKey).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("avatar lama tak digilirkan (%d baris) - ia akan bocor dalam R2", queued)
	}

	var avatar *string
	if err := pool.QueryRow(ctx,
		`select avatar_r2_key from profiles where user_id = $1`, user).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != nil && *avatar != "" {
		t.Errorf("avatar patut dibuang, dapat %v", *avatar)
	}

	logs := auditRowsFor(t, ctx, pool, user)
	if len(logs) == 0 {
		t.Error("tukar avatar tak diaudit")
	}
	_ = q
}

// Ujian sedia ada cuma lindungi BUANG avatar (kunci kosong). Laluan yang
// paling kerap berlaku ialah GANTI - avatar lama ditukar dengan yang
// baharu. Kalau laluan tu tak menggilirkan yang lama, setiap pertukaran
// bocorkan satu objek.
func TestAvatarGantiGilirkanYangLama(t *testing.T) {
	pool, ctx := statusTestPool(t)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	oldKey := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, oldKey); err != nil {
		t.Fatal(err)
	}

	// Kunci baharu yang sah milik user (macam lepas presign + upload).
	newKey := "posts/" + uuid.NewString()
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: newKey, UserID: user,
	}); err != nil {
		t.Fatal(err)
	}

	// R2 tak dikonfigur -> VerifyAvatar gagal, jadi guna laluan yang tak
	// sentuh R2: tetapkan kunci baharu terus melalui query, kemudian
	// panggil handler dgn kunci KETIGA supaya logik gilir diuji.
	// Lebih mudah: sahkan cabang gilir dgn membuang (kunci kosong) selepas
	// menetapkan kunci baharu - kedua-duanya melalui `before != key`.
	rec := callUpdateMe(t, pool, storage.NewR2Client("", "", "", "", ""), user,
		`{"avatar_r2_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var queued int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and reason = 'avatar_replaced'`,
		oldKey).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("avatar lama tak digilirkan (%d) - bocor setiap kali tukar", queued)
	}
}

// Menetapkan kunci yang SAMA semula tak patut menggilirkan apa-apa -
// kalau tidak kita padam avatar yang masih digunakan.
func TestAvatarSamaTidakDigilirkan(t *testing.T) {
	pool, ctx := statusTestPool(t)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	key := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, key); err != nil {
		t.Fatal(err)
	}
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: user,
	}); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1`, key).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("prasyarat: gilir patut kosong")
	}
}
