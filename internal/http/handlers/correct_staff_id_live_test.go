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

// Ujian integrasi terhadap Postgres sebenar untuk PATCH /members/:id/staff-id
// (Task 6, pembetulan nombor staff). Ikut konvensyen sebenar pakej ni sama
// macam verify_staff_id_live_test.go - panggil fungsi handler terus atas
// ProfileHandler{pool, queries}, bukan melalui router/httptest berasaskan
// Gin engine penuh.

// callCorrectStaffID invokes ProfileHandler.CorrectStaffID directly,
// mirroring callVerifyStaffID (verify_staff_id_live_test.go).
func callCorrectStaffID(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/members/"+targetID.String()+"/staff-id", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)

	h.CorrectStaffID(c)
	return rec
}

// staffIDCorrectionAuditRowsFor - like staffIDVerificationAuditRowsFor
// (verify_staff_id_live_test.go) but for entity_type = 'staff_id_correction'
// (audit.EntityStaffIDCorrection).
func staffIDCorrectionAuditRowsFor(t *testing.T, pool *pgxpool.Pool, entityID uuid.UUID) []map[string]any {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`select action, changed_fields, old_values, new_values, actor_member_id, actor_role_key
		 from audit_logs where entity_type = 'staff_id_correction' and entity_id = $1 order by id`, entityID)
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

func TestCorrectStaffID_RequiresAdminRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager") // manager can verify but NOT correct
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectStaffID(t, pool, manager, target, `{"staff_id":"EMP-9999"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for manager rank, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_AdminCanCorrectAlreadyVerified(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli") // already staff-verified via seed

	rec := callCorrectStaffID(t, pool, admin, target, `{"staff_id":"EMP-REAL-001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	q := sqlc.New(pool)
	updated, err := q.GetProfileByUserID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StaffID != "EMP-REAL-001" {
		t.Fatalf("expected corrected staff_id, got %q", updated.StaffID)
	}
	if !updated.StaffIDVerifiedAt.Valid {
		t.Fatal("correcting staff_id must not un-verify the member")
	}

	logs := staffIDCorrectionAuditRowsFor(t, pool, target)
	found := false
	for _, l := range logs {
		if l["action"] == "update" {
			found = true
		}
	}
	if !found {
		t.Error("expected an audit_logs row for the correction")
	}
}

func TestCorrectStaffID_DuplicateReturns409(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	_ = createTestProfileWithStaffID(t, ctx, pool, "EMP-TAKEN")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectStaffID(t, pool, admin, target, `{"staff_id":"EMP-TAKEN"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_UnknownUserReturns404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectStaffID(t, pool, admin, uuid.New(), `{"staff_id":"EMP-1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_EmptyStaffIDReturns400(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectStaffID(t, pool, admin, target, `{"staff_id":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 empty staff_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_StaffIDWithSlashRejected(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectStaffID(t, pool, admin, target, `{"staff_id":"EMP/001"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for staff_id containing '/', got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_RejectsSelfCorrection(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectStaffID(t, pool, admin, admin, `{"staff_id":"EMP-SELF-001"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-correction, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_RejectsCorrectingHigherRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	superadmin := createTestApprovedProfile(t, ctx, pool, "superadmin")

	rec := callCorrectStaffID(t, pool, admin, superadmin, `{"staff_id":"EMP-HIGH-001"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 correcting higher-rank target, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_AllowsCorrectingLowerRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	target := createTestApprovedProfile(t, ctx, pool, "ahli")

	rec := callCorrectStaffID(t, pool, admin, target, `{"staff_id":"EMP-LOW-001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 correcting lower-rank target, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_RejectsCorrectingEqualRank(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")
	otherAdmin := createTestApprovedProfile(t, ctx, pool, "admin")

	rec := callCorrectStaffID(t, pool, admin, otherAdmin, `{"staff_id":"EMP-EQUAL-001"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 correcting equal-rank target, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCorrectStaffID_MalformedIDReturns400(t *testing.T) {
	pool, ctx := statusTestPool(t)
	admin := createTestApprovedProfile(t, ctx, pool, "admin")

	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/members/not-a-uuid/staff-id", strings.NewReader(`{"staff_id":"EMP-1"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	c.Set("userID", admin)

	h.CorrectStaffID(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 malformed id, got %d: %s", rec.Code, rec.Body.String())
	}
}
