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
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/email"
)

// Ujian integrasi terhadap Postgres sebenar untuk PATCH /members/:id/member-id
// (Task 4, pembetulan nombor ahli). Ikut konvensyen sebenar pakej ni sama
// macam correct_staff_id_live_test.go - panggil fungsi handler terus atas
// ProfileHandler{pool, queries}.

func callCorrectMemberID(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/members/"+targetID.String()+"/member-id", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)

	h.CorrectMemberID(c)
	return rec
}

func memberIDCorrectionAuditRowsFor(t *testing.T, pool *pgxpool.Pool, entityID uuid.UUID) []map[string]any {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`select action, changed_fields, old_values, new_values, actor_member_id, actor_role_key
		 from audit_logs where entity_type = 'member_id_correction' and entity_id = $1 order by id`, entityID)
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

func TestCorrectMemberID_RequiresAdminRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectMemberID(t, pool, manager, target, `{"member_id":"MARC-0110/2026-0001"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for manager rank, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_AdminCanCorrectExistingValue(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	q := sqlc.New(pool)
	before, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}

	const want = "MARC-0110/2026-9999"
	rec := callCorrectMemberID(t, pool, admin, target, `{"member_id":"`+want+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.MemberID.Valid || updated.MemberID.String != want {
		t.Fatalf("expected member_id %q, got %+v", want, updated.MemberID)
	}

	logs := memberIDCorrectionAuditRowsFor(t, pool, target)
	found := false
	for _, l := range logs {
		if l["action"] != "update" {
			continue
		}
		oldV := l["old"].(map[string]any)
		newV := l["new"].(map[string]any)
		if oldV["member_id"] != before.MemberID.String {
			t.Fatalf("audit old member_id: got %v want %q", oldV["member_id"], before.MemberID.String)
		}
		if newV["member_id"] != want {
			t.Fatalf("audit new member_id: got %v want %q", newV["member_id"], want)
		}
		found = true
	}
	if !found {
		t.Error("expected an audit_logs row for the correction")
	}
}

func TestCorrectMemberID_NullMemberIDReturns409(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestPendingProfile(t, ctx, pool)

	rec := callCorrectMemberID(t, pool, admin, target, `{"member_id":"MARC-0110/2026-0001"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for null member_id, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "belum ada nombor ahli") {
		t.Fatalf("expected 409 message pointing at verify, got %s", rec.Body.String())
	}
}

func TestCorrectMemberID_DuplicateReturns409(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	first := createTestApprovedProfile(t, ctx, pool, "ahli")
	second := createTestApprovedProfile(t, ctx, pool, "ahli")

	q := sqlc.New(pool)
	taken, err := q.GetProfileByUserID(ctx, second)
	if err != nil {
		t.Fatal(err)
	}

	rec := callCorrectMemberID(t, pool, admin, first, `{"member_id":"`+taken.MemberID.String+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 duplicate member_id, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sudah digunakan") {
		t.Fatalf("expected duplicate message, got %s", rec.Body.String())
	}
}

func TestCorrectMemberID_UnknownUserReturns404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectMemberID(t, pool, admin, uuid.New(), `{"member_id":"MARC-0110/2026-0001"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_MalformedIDReturns400(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/members/not-a-uuid/member-id", strings.NewReader(`{"member_id":"MARC-0110/2026-0001"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	c.Set("userID", admin)

	h.CorrectMemberID(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 malformed id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_RejectsSelfCorrection(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectMemberID(t, pool, admin, admin, `{"member_id":"MARC-SELF/2026-0001"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-correction, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_RejectsCorrectingHigherRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	superadmin := createTestApprovedProfile(t, ctx, pool, "superadmin")

	rec := callCorrectMemberID(t, pool, admin, superadmin, `{"member_id":"MARC-HIGH/2026-0001"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 correcting higher-rank target, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_RejectsCorrectingEqualRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	otherAdmin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectMemberID(t, pool, admin, otherAdmin, `{"member_id":"MARC-EQUAL/2026-0001"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 correcting equal-rank target, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectMemberID_DoesNotTouchStaffID(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	q := sqlc.New(pool)
	before, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}

	rec := callCorrectMemberID(t, pool, admin, target, `{"member_id":"MARC-KEEP/2026-0001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if after.StaffID != before.StaffID {
		t.Fatalf("staff_id changed: %q -> %q", before.StaffID, after.StaffID)
	}
	if after.StaffIDVerifiedAt.Valid != before.StaffIDVerifiedAt.Valid ||
		(after.StaffIDVerifiedAt.Valid && !after.StaffIDVerifiedAt.Time.Equal(before.StaffIDVerifiedAt.Time)) {
		t.Fatal("staff_id_verified_at must not change when correcting member_id")
	}
}
