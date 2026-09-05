package handlers

import (
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

// Task 7 of the staff-id verification plan: `staff_id` and
// `staff_id_verified_at` must appear on the management member list/
// detail JSON responses (memberResponse, memberDetailResponse), sourced
// from the profile row already loaded for those endpoints - no new
// query.
//
// Follows this package's real convention (NOT the plan's illustrative
// httptest/newTestRouterWithQueries sketch, which doesn't exist here):
// invoke ProfileHandler.Members directly against a real Postgres pool,
// mirroring callVerifyStaffID in verify_staff_id_live_test.go.

// callMembersFiltered invokes ProfileHandler.Members directly, mirroring
// callVerifyStaffID (verify_staff_id_live_test.go).
func callMembersFiltered(t *testing.T, pool *pgxpool.Pool, callerID uuid.UUID, statusFilter string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	url := "/members"
	if statusFilter != "" {
		url += "?status=" + statusFilter
	}
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Set("userID", callerID)

	h.Members(c)
	return rec
}

func TestListPendingMembers_IncludesStaffID(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	target := createTestPendingProfile(t, ctx, pool) // has a staff_id, unverified

	rec := callMembersFiltered(t, pool, manager, "pending")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var members []memberResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, rec.Body.String())
	}

	var found *memberResponse
	for i := range members {
		if members[i].UserID == target.String() {
			found = &members[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected pending target in response, got: %s", rec.Body.String())
	}
	// Caller ialah manager (management), jadi staff_id target mesti hadir
	// dengan nilai sebenar - tiering staff_id (Opus verify 2026-09-03)
	// cuma sembunyikan nombor ni drpd ahli BUKAN management.
	if found.StaffID == nil || *found.StaffID == "" {
		t.Fatal("expected non-empty staff_id on pending member")
	}
	if found.StaffIDVerifiedAt != nil {
		t.Fatalf("expected nil staff_id_verified_at for unverified member, got %v", *found.StaffIDVerifiedAt)
	}
	if found.MemberID != nil {
		t.Fatalf("expected JSON null member_id for unverified member, got %q", *found.MemberID)
	}

	// Belt-and-suspenders per the plan's illustrative check: the raw JSON
	// must literally carry the field name (not just be reachable via the
	// Go struct), since that's what the Flutter client (Task 11) parses.
	if !strings.Contains(rec.Body.String(), `"staff_id"`) {
		t.Fatalf("expected \"staff_id\" key in raw JSON, got: %s", rec.Body.String())
	}
}
