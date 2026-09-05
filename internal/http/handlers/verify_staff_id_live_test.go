package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/email"
)

// Ujian integrasi terhadap Postgres sebenar untuk POST
// /members/:id/verify-staff-id (Task 5, staff-id verification). Real
// Postgres diperlukan sebab yang diuji ialah sifat TRANSAKSI (member_id
// dijana + staff_id_verified_at ditetapkan + catatan audit ditulis
// bersama, guard `staff_id_verified_at is null` dalam VerifyStaffID) -
// padanan sebab statusTestPool wujud (lihat profile_status_live_test.go).
//
// Ikut konvensyen sebenar pakej ni (BUKAN sketsa ilustrasi pelan): panggil
// fungsi handler terus atas ProfileHandler{pool, queries}, bukan melalui
// router/httptest berasaskan Gin engine penuh - newTestRouterWithQueries/
// testToken/GetProfileByEmail yang disebut dalam pelan TIDAK wujud dalam
// kod pangkalan ni.

// createTestRejectedProfile - mirrors createTestPendingProfile
// (staff_id_query_live_test.go) tapi status 'rejected'.
func createTestRejectedProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	emailAddr := "staffid-rejected-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		emailAddr).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = 'ahli'), 'rejected')`,
		userID, "EMP-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed rejected profile: %v", err)
	}
	return userID
}

// callVerifyStaffID invokes ProfileHandler.VerifyStaffID directly,
// mirroring callSetStatus/callUpdateMe in profile_status_live_test.go.
func callVerifyStaffID(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, body string) *httptest.ResponseRecorder {
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
	c.Request = httptest.NewRequest(http.MethodPost, "/members/"+targetID.String()+"/verify-staff-id", bodyReader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)

	h.VerifyStaffID(c)
	return rec
}

// staffIDVerificationAuditRowsFor - like auditRowsFor
// (profile_status_live_test.go) but for entity_type = 'staff_id_verification'
// (audit.EntityStaffIDVerification), not 'profile'. VerifyStaffID
// deliberately records under its own entity type (see comment on
// audit.EntityStaffIDVerification) rather than audit.EntityProfile, so
// the shared helper - which hardcodes entity_type = 'profile' - would
// always find zero rows here.
func staffIDVerificationAuditRowsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entityID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx,
		`select action, changed_fields, old_values, new_values, actor_member_id, actor_role_key
		 from audit_logs where entity_type = 'staff_id_verification' and entity_id = $1 order by id`, entityID)
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

// verifyStaffIDDirect calls q.VerifyStaffID directly to put a target
// profile into the "already verified" state, needed to set up the
// override-on-already-verified test without going through the handler
// twice (which would exercise idempotency instead of the override gate).
func verifyStaffIDDirect(t *testing.T, ctx context.Context, pool *pgxpool.Pool, targetID, verifierID uuid.UUID) {
	t.Helper()
	q := sqlc.New(pool)
	memberID := "MARC/verify-" + uuid.NewString()[:8]
	if _, err := q.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
		UserID:     targetID,
		VerifiedBy: pgtype.UUID{Bytes: verifierID, Valid: true},
		MemberID:   pgtype.Text{String: memberID, Valid: true},
	}); err != nil {
		t.Fatalf("seed verified staff_id: %v", err)
	}
}

func TestVerifyStaffID_RequiresManagerRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	supervisor := seedMember(t, ctx, pool, "supervisor", "approved")
	target := createTestPendingProfile(t, ctx, pool)

	rec := callVerifyStaffID(t, pool, supervisor, target, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for supervisor rank, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_RejectsSelfVerify(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	rec := callVerifyStaffID(t, pool, manager, manager, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 self-verify rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_RejectsRejectedMember(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestRejectedProfile(t, ctx, pool)

	rec := callVerifyStaffID(t, pool, manager, target, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for rejected member, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_AssignsMemberID(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)

	rec := callVerifyStaffID(t, pool, manager, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	updated, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.MemberID.Valid || updated.MemberID.String == "" {
		t.Fatal("expected member_id assigned after verification")
	}
	if !updated.StaffIDVerifiedAt.Valid {
		t.Fatal("expected staff_id_verified_at set after verification")
	}

	logs := staffIDVerificationAuditRowsFor(t, ctx, pool, target)
	found := false
	for _, l := range logs {
		if l["action"] == "update" {
			found = true
		}
	}
	if !found {
		t.Error("expected an audit_logs row for the verification")
	}
}

func TestVerifyStaffID_IdempotentSecondCall(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)

	first := callVerifyStaffID(t, pool, manager, target, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first verify failed: %d %s", first.Code, first.Body.String())
	}
	var firstResp struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}

	second := callVerifyStaffID(t, pool, manager, target, "")
	if second.Code != http.StatusOK {
		t.Fatalf("expected 200 idempotent re-verify, got %d: %s", second.Code, second.Body.String())
	}
	var secondResp struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
		t.Fatal(err)
	}
	if secondResp.MemberID != firstResp.MemberID {
		t.Fatalf("expected same member_id on idempotent re-verify, got %q vs %q", firstResp.MemberID, secondResp.MemberID)
	}

	// Second call must NOT write a second audit row.
	if logs := staffIDVerificationAuditRowsFor(t, ctx, pool, target); len(logs) != 1 {
		t.Fatalf("expected 1 audit row after idempotent re-verify, got %d", len(logs))
	}
}

func TestVerifyStaffID_UnknownUserReturns404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	rec := callVerifyStaffID(t, pool, manager, uuid.New(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_OverrideOnAlreadyVerifiedReturns409(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)
	verifyStaffIDDirect(t, ctx, pool, target, manager) // already verified

	rec := callVerifyStaffID(t, pool, manager, target, `{"staff_id":"EMP-TYPO-FIX"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 pointing to the correction endpoint, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestVerifyStaffID_RejectsBackfilledPlaceholderStaffIDWithoutOverride
// seeds staff_id == user_id with member_id still NULL (artificial today;
// post-migration register always supplies a real staff_id). Same guard
// as the pre-migration pending case below: a UUID must never be accepted
// as a verified staff number / fee-exemption key.
func TestVerifyStaffID_RejectsBackfilledPlaceholderStaffIDWithoutOverride(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	var userID uuid.UUID
	emailAddr := "staffid-placeholder-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		emailAddr).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = 'ahli'), 'pending')`,
		userID, userID.String()); err != nil {
		t.Fatalf("seed placeholder-staff_id profile: %v", err)
	}

	rec := callVerifyStaffID(t, pool, manager, userID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for placeholder staff_id without override, got %d: %s", rec.Code, rec.Body.String())
	}

	// Confirm the SAME call WITH an override succeeds (200) and the
	// resulting member_id embeds the override value, not the
	// placeholder UUID.
	// staff_id unik merentas profiles - literal tetap akan berlanggar bila
	// ujian dijalankan semula atas DB ujian yang tak dibuang, jadi jana
	// nilai baharu (padanan seeder lain dalam fail ni).
	overrideStaffID := "EMP-" + uuid.NewString()[:8]
	rec2 := callVerifyStaffID(t, pool, manager, userID, `{"staff_id":"`+overrideStaffID+`"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with override supplied, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.MemberID, overrideStaffID) {
		t.Fatalf("expected member_id to embed override staff_id, got %q", resp.MemberID)
	}
	if strings.Contains(resp.MemberID, userID.String()) {
		t.Fatalf("expected member_id to NOT embed the placeholder UUID, got %q", resp.MemberID)
	}
}

// TestVerifyStaffID_PlaceholderStaffIDWithExistingMemberIDStillRequiresOverride
// mirrors the real pre-migration pending row: staff_id backfilled to
// user_id::text, member_id already assigned in the OLD format, still
// unverified. Verify without override must 400 (a UUID must never become
// the verified staff number / fee-exemption key). With override: 200,
// existing member_id left untouched.
func TestVerifyStaffID_PlaceholderStaffIDWithExistingMemberIDStillRequiresOverride(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	var userID uuid.UUID
	emailAddr := "staffid-placeholder-existing-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		emailAddr).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	oldMemberID := "MARC2026/08/" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, staff_id, role_id, status)
		 values ($1, $2, $3, (select id from roles where key = 'ahli'), 'pending')`,
		userID, oldMemberID, userID.String()); err != nil {
		t.Fatalf("seed pre-migration pending profile: %v", err)
	}

	rec := callVerifyStaffID(t, pool, manager, userID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for placeholder staff_id without override even when member_id already set, got %d: %s", rec.Code, rec.Body.String())
	}

	overrideStaffID := "EMP-" + uuid.NewString()[:8]
	rec2 := callVerifyStaffID(t, pool, manager, userID, `{"staff_id":"`+overrideStaffID+`"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with override supplied, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.MemberID != oldMemberID {
		t.Fatalf("expected existing member_id %q unchanged, got %q", oldMemberID, resp.MemberID)
	}

	updated, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StaffID != overrideStaffID {
		t.Fatalf("expected override staff_id, got %q", updated.StaffID)
	}
	if !updated.MemberID.Valid || updated.MemberID.String != oldMemberID {
		t.Fatalf("member_id changed: want %q, got %+v", oldMemberID, updated.MemberID)
	}
}

// createTestPendingProfileWithRole - createTestPendingProfile
// (staff_id_query_live_test.go) tapi role boleh dipilih, untuk menguji
// semakan hierarki rank VerifyStaffID.
func createTestPendingProfileWithRole(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleKey string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	emailAddr := "staffid-pending-" + roleKey + "-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		emailAddr).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = $3), 'pending')`,
		userID, "EMP-"+uuid.NewString()[:8], roleKey); err != nil {
		t.Fatalf("seed pending profile (%s): %v", roleKey, err)
	}
	return userID
}

// Manager TAK boleh sahkan nombor staff akaun rank LEBIH TINGGI yang
// masih pending (Opus verify 2026-09-03 - HIGH). Tanpa semakan hierarki
// rank, manager boleh cap akaun superadmin pending dengan nombor staff
// pilihan dia sendiri, jana member_id untuknya, dan jadikannya
// fee-exempt - semua handler tulis-ke-ahli-lain yang lain (UpdateMemberRole,
// UpdateMemberActive, CorrectStaffID, CorrectMemberID) dah lama menyekat
// ini.
func TestVerifyStaffID_TargetRankLebihTinggiDitolak(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfileWithRole(t, ctx, pool, "superadmin")

	rec := callVerifyStaffID(t, pool, manager, target, `{"staff_id":"EMP-`+uuid.NewString()[:8]+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mahu 403 untuk target rank lebih tinggi, dapat %d: %s", rec.Code, rec.Body.String())
	}

	// Escalation MESTI tak berlaku langsung - baris target kekal belum
	// disahkan dan tanpa member_id.
	after, err := sqlc.New(pool).GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if after.StaffIDVerifiedAt.Valid || after.MemberID.Valid {
		t.Fatalf("target disahkan/dicap walaupun 403: verified=%v member_id=%+v", after.StaffIDVerifiedAt, after.MemberID)
	}
}

// Rank SETARAF pun ditolak (`<=`, padanan handler adik-beradik).
func TestVerifyStaffID_TargetRankSetarafDitolak(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfileWithRole(t, ctx, pool, "manager")

	rec := callVerifyStaffID(t, pool, manager, target, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mahu 403 untuk target rank setaraf, dapat %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_OverrideStaffIDWithSlashRejected(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool)

	rec := callVerifyStaffID(t, pool, manager, target, `{"staff_id":"EMP/001"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for override staff_id containing '/', got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyStaffID_MalformedIDReturns400(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/members/not-a-uuid/verify-staff-id", strings.NewReader(""))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	c.Set("userID", manager)

	h.VerifyStaffID(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 malformed id, got %d: %s", rec.Code, rec.Body.String())
	}
}
