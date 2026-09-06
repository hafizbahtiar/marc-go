# Staff Number Verification Implementation Plan (v2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a mandatory `staff_id` field to registration, gate member-number issuance and management approval on a management member (rank >= manager) verifying that number, make verified staff exempt from the ToyyibPay registration fee, let admin/superadmin (rank >= 80) correct a staff ID at any time, and add an "outstanding fee" banner to the payment history page.

**Architecture:** Backend-only data/logic change in `marc_go` (new columns, two new management endpoints, edits to three existing handlers) plus `marc_flutter` changes: a registration-form field, nullable-`memberId` handling across four models, two management actions (verify / correct) on the pending-members screen, and a banner on the payment history page.

**Tech Stack:** Go/Gin/pgx/sqlc (backend), Flutter/Riverpod (frontend), Postgres migrations.

**Spec:** `docs/superpowers/specs/2026-09-02-staff-id-verification-design.md` (v2) - READ IN FULL FIRST. It documents a v1 design bug found by Opus review (staff-exemption logic could silently defeat the `BypassPayment` admin-rank gate) and the fix baked into Task 8 below; do not re-derive that section from scratch, follow the spec's "Gate yuran - butiran KRITIKAL" section exactly.

## Global Constraints

- `staff_id` has NO format validation, max 64 chars, stored as an opaque string.
- Verify endpoint (`POST /members/:id/verify-staff-id`) gate: rank >= 60 (`manager`). Correct endpoint (`PATCH /members/:id/staff-id`) gate: rank >= 80 (`admin`) - these are DIFFERENT thresholds, do not conflate them.
- `:id` in every route this plan adds is a **`user_id`**, resolved via `GetProfileByUserID` - NOT a `profiles.id` primary key. `GetProfileByID` does not exist in this codebase; do not invent it.
- Routes have **no `/admin` prefix** - they live in the existing `approved` route group exactly like `/members/:id/approve`.
- The staff-verification approval gate has no bypass flag (hard requirement). The existing `BypassPayment` gate is untouched for non-staff-exempt members, and MUST be forced to `false` before it can affect audit logging when a member is staff-exempt (see Task 8).
- `generateMemberID` must run inside the SAME DB transaction as the verify write (mirror `activity_certificates.go:207-224`), never as a bare pre-transaction call - a rolled-back verify must not leak a sequence number silently without at least being logged.
- Every new DB write is idempotent per existing patterns (`ApproveProfile`, `AddBlockedEmailDomain`).
- All new backend code must pass `go build ./...`, `go vet ./...`, `gofmt -l .` (empty output), and `go test ./...` before each commit.
- All new Flutter code must pass `flutter analyze` (clean) and `flutter test` before each commit.

---

### Task 1: Migration - `staff_id` columns + backfill + nullable `member_id`

**Files:**
- Create: `internal/db/migrations/20260902100000_add_staff_id.sql`
- Modify: `DATABASE.md`

**Interfaces:**
- Produces: `profiles.staff_id text not null unique`, `profiles.staff_id_verified_at timestamptz null`, `profiles.staff_id_verified_by uuid null references users(id)`; `profiles.member_id` becomes nullable.

- [ ] **Step 1: Confirm this repo's migration header convention**

Open `internal/db/migrations/20260825120000_add_profile_department_department.sql` and copy its up/down header style verbatim - do not assume `+goose`-style comments without checking.

- [ ] **Step 2: Write the migration**

```sql
alter table profiles
  add column staff_id text,
  add column staff_id_verified_at timestamptz,
  add column staff_id_verified_by uuid references users(id);

alter table profiles
  alter column member_id drop not null;

-- Only 'approved' rows are auto-verified. 'pending'/'rejected' rows
-- get a placeholder staff_id but stay UNVERIFIED - they never
-- went through real verification and some may still owe the fee.
-- Auto-verifying them would silently exempt them from payment AND
-- make them one click from approval with no real check ever done.
update profiles
set staff_id = user_id::text,
    staff_id_verified_at = case when status = 'approved' then now() else null end
where staff_id is null;

alter table profiles
  alter column staff_id set not null;

create unique index profiles_staff_id_key on profiles (staff_id);
```

Down migration: do NOT attempt to restore `member_id`'s `NOT NULL` -
it will fail once any unverified (`member_id IS NULL`) row exists.
Add a comment in the Down section explaining this instead of writing
code that will error on any real rollback attempt.

- [ ] **Step 3: Run the migration against local dev DB**

Run whatever this repo's `Makefile`/`README.md` documents (check
before running). Confirm via
`psql "$DATABASE_URL" -c "select status, count(*), count(staff_id_verified_at) from profiles group by status;"`
that `approved` rows show `count(*) == count(staff_id_verified_at)`
and `pending`/`rejected` rows show `count(staff_id_verified_at) = 0`.

- [ ] **Step 4: Regenerate sqlc code**

Run `sqlc generate`. Confirm `Profile.MemberID` is now `pgtype.Text`
(was `string`) in the generated struct - this is the trigger for
Task 2's blast-radius fix, do not skip acknowledging it here.

- [ ] **Step 5: Update `DATABASE.md`**

Add the three new columns, and note `member_id` is nullable until
staff verification (link to the spec for the full rationale rather
than repeating it).

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/20260902100000_add_staff_id.sql DATABASE.md internal/db/sqlc
git commit -m "feat: add staff_id columns, defer member_id to verification"
```

---

### Task 2: Fix every call site broken by nullable `member_id`

**Files:** (confirm exact list via `grep -rn "\.MemberID\b" --include=*.go .` - the list below is what the spec's research found, treat it as a floor, not a ceiling)
- Modify: `internal/http/handlers/payments.go` (lines ~83, 148, 206)
- Modify: `internal/http/handlers/registration_payment.go` (lines ~132, 187, 425)
- Modify: `internal/http/handlers/activity_registration_payment.go` (lines ~147, 413)
- Modify: `internal/http/handlers/donations.go` (line ~286)
- Modify: `internal/http/handlers/profile.go` (lines ~119, 262, 500, 614, 734, 889 - line 500 is the `Members` list row, confirmed via Opus verify round 2, missing from the original grep)
- Modify: `internal/http/handlers/audit_helpers.go` (lines ~49, ~64 - NOTE: this file lives under `internal/http/handlers/`, NOT `internal/audit/`; the v1 plan had the wrong path. Line 64's `actor.MemberID != ""` string-emptiness check also needs converting to a `.Valid` check.)
- Modify: `internal/http/handlers/activity_attendance.go` (line ~405)
- Modify: `internal/http/handlers/comments.go` (line ~136)
- Modify: `internal/receipt/receipt.go` (wherever it reads `MemberID`)
- Test: whichever of the above already have `_test.go` siblings

**Interfaces:**
- Consumes: `Profile.MemberID` is now `pgtype.Text` (Task 1). Everywhere that read it as a plain `string` must switch to `.String`/`.Valid` handling, with a sensible fallback where `member_id` feeds an external system.

- [ ] **Step 1: Run `go build ./...` to enumerate every compile break**

This is the authoritative list - grep is a starting point, the
compiler is the source of truth. Expect ~15-20 errors of the shape
`cannot use profile.MemberID (variable of type pgtype.Text) as string`.

- [ ] **Step 2: Fix each site by category**

- **Display-only sites** (profile JSON responses, receipts): emit
  `null`/omit the field when `!Valid`, matching how this codebase
  already handles other nullable `pgtype` fields (e.g. `approved_at`)
  - copy that exact pattern rather than inventing a new one.
- **`registration_payment.go`'s `billTo` fallback** (~line 125, comment
  currently says "member_id sentiasa diisi semasa daftar" - that
  invariant is now false): change the fallback chain to
  `display_name → member_id (if valid) → email`. Update the stale
  comment to say why `email` was added as a third fallback.
- **Any site that used `member_id` as a required non-empty value for
  an external call** (ToyyibPay `billTo`, receipt PDFs, notifications):
  audit each one individually - if the flow can be reached by an
  unverified member (no `member_id` yet), it needs a fallback; if it's
  only reachable post-approval (`member_id` guaranteed non-null by
  then, since approval requires verification per Task 8), a comment
  explaining why is enough, no code change needed. Do not blanket-add
  fallbacks to code paths that are provably unreachable pre-verification.

- [ ] **Step 3: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: clean, zero regressions. Any pre-existing test that
constructs a `Profile{MemberID: "..."}` literal will also need updating
to `pgtype.Text{String: "...", Valid: true}` - fix each as found.

- [ ] **Step 4: Commit**

```bash
git add internal/http/handlers internal/audit internal/receipt
git commit -m "fix: handle nullable member_id across payment, receipt, and audit call sites"
```

---

### Task 3: sqlc queries - `VerifyStaffID`, `CorrectStaffID`

**Files:**
- Modify: `queries/profiles.sql` (locate the `ApproveProfile` section via `grep -rl "ApproveProfile" queries/`)

**Interfaces:**
- Produces: `VerifyStaffID(ctx, VerifyStaffIDParams{UserID uuid.UUID, VerifiedBy uuid.UUID, StaffID pgtype.Text, MemberID pgtype.Text}) (Profile, error)` - zero rows affected (not an error - the handler checks `pgconn.CommandTag.RowsAffected()` via `:execrows` or checks the returned row against `sql.ErrNoRows` depending on which sqlc annotation is used; pick `:one` and treat `pgx.ErrNoRows` as "someone else verified first," per Step 1 below).
- Produces: `CorrectStaffID(ctx, CorrectStaffIDParams{UserID uuid.UUID, StaffID string}) (Profile, error)`.

- [ ] **Step 1: Write the queries**

```sql
-- name: VerifyStaffID :one
update profiles
set staff_id = coalesce(sqlc.narg('staff_id'), staff_id),
    staff_id_verified_at = now(),
    staff_id_verified_by = @verified_by,
    member_id = coalesce(member_id, @member_id)
where user_id = @user_id
  and staff_id_verified_at is null
returning *;

-- name: CorrectStaffID :one
update profiles
set staff_id = @staff_id
where user_id = @user_id
returning *;
```

`VerifyStaffID` keys on `user_id` (matching `GetProfileByUserID`,
NOT a `profiles.id` primary key - this was a bug in the v1 plan).
`@member_id` is computed by the handler (Task 5) via the existing
`generateMemberID`, inside the same transaction - the query itself
does not generate sequence numbers.

- [ ] **Step 2: Regenerate sqlc code**

Run `sqlc generate`. Confirm both queries appear on the `Queries`
interface, and confirm whether this repo's sqlc config generates a
`*Queries.WithTx(tx pgx.Tx) *Queries` method (it should, if
`setMemberStatus` already uses transactional queries elsewhere - check
`profile.go`'s existing tx usage for the pattern to match in Task 5).

- [ ] **Step 3: Write query-level tests**

Match whichever test file already covers `ApproveProfile` (likely
`internal/http/handlers/profile_test.go`):

```go
func TestVerifyStaffID_FirstCallSucceeds(t *testing.T) {
    ctx := context.Background()
    q, cleanup := newTestQueries(t)
    defer cleanup()

    target := createTestPendingProfile(t, q) // helper must produce a profile with member_id NULL, staff_id_verified_at NULL
    manager := createTestApprovedProfile(t, q, "manager")

    got, err := q.VerifyStaffID(ctx, VerifyStaffIDParams{
        UserID:     target.UserID,
        VerifiedBy: manager.UserID,
        MemberID:   pgtype.Text{String: "MARC2026/09/0001", Valid: true},
    })
    if err != nil {
        t.Fatalf("first verify: %v", err)
    }
    if !got.StaffIDVerifiedAt.Valid {
        t.Fatal("expected staff_id_verified_at set")
    }
    if got.MemberID.String != "MARC2026/09/0001" {
        t.Fatalf("expected member_id assigned, got %v", got.MemberID)
    }
}

func TestVerifyStaffID_SecondCallReturnsNoRows(t *testing.T) {
    ctx := context.Background()
    q, cleanup := newTestQueries(t)
    defer cleanup()

    target := createTestPendingProfile(t, q)
    manager := createTestApprovedProfile(t, q, "manager")
    _, err := q.VerifyStaffID(ctx, VerifyStaffIDParams{UserID: target.UserID, VerifiedBy: manager.UserID, MemberID: pgtype.Text{String: "MARC2026/09/0001", Valid: true}})
    if err != nil {
        t.Fatalf("first verify: %v", err)
    }

    _, err = q.VerifyStaffID(ctx, VerifyStaffIDParams{UserID: target.UserID, VerifiedBy: manager.UserID, MemberID: pgtype.Text{String: "MARC2026/09/0002", Valid: true}})
    if !errors.Is(err, pgx.ErrNoRows) {
        t.Fatalf("expected pgx.ErrNoRows on second verify, got %v", err)
    }
}

func TestCorrectStaffID_UpdatesEvenWhenVerified(t *testing.T) {
    ctx := context.Background()
    q, cleanup := newTestQueries(t)
    defer cleanup()

    target := createTestApprovedProfile(t, q, "ahli") // already verified per backfill/approval
    got, err := q.CorrectStaffID(ctx, CorrectStaffIDParams{UserID: target.UserID, StaffID: "EMP-9999"})
    if err != nil {
        t.Fatal(err)
    }
    if got.StaffID != "EMP-9999" {
        t.Fatalf("expected corrected staff_id, got %q", got.StaffID)
    }
    if !got.StaffIDVerifiedAt.Valid {
        t.Fatal("correcting staff_id must NOT clear verified_at")
    }
}
```

- [ ] **Step 4: Run tests, confirm fail then pass**

Run: `go test ./... -run 'TestVerifyStaffID|TestCorrectStaffID' -v`

- [ ] **Step 5: Commit**

```bash
git add queries/profiles.sql internal/db/sqlc internal/http/handlers/profile_test.go
git commit -m "feat: add VerifyStaffID and CorrectStaffID queries"
```

---

### Task 4: `POST /auth/register` - require `staff_id`, defer `member_id`, handle conflicts

**Files:**
- Modify: `internal/http/handlers/auth.go` (`Register` handler + request struct, ~lines 250-350)
- Test: `internal/http/handlers/auth_test.go`

**Interfaces:**
- Produces: `RegisterRequest.StaffID string` (json `staff_id`, required, max 64 chars).

- [ ] **Step 1: Write the failing tests**

```go
func TestRegister_RequiresStaffID(t *testing.T) {
    router := newTestRouter(t)
    body := `{"email":"new@example.com","password":"Password1!","display_name":"New User","phone":"0123456789"}`
    req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 missing staff_id, got %d: %s", w.Code, w.Body.String())
    }
}

func TestRegister_DefersMemberID(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    body := `{"email":"staff@example.com","password":"Password1!","display_name":"Staff User","phone":"0123456789","staff_id":"EMP-001"}`
    req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusCreated {
        t.Fatalf("expected success, got %d: %s", w.Code, w.Body.String())
    }
    profile, err := q.GetProfileByEmail(context.Background(), "staff@example.com")
    if err != nil {
        t.Fatal(err)
    }
    if profile.MemberID.Valid {
        t.Fatalf("expected member_id NULL until verification, got %v", profile.MemberID)
    }
    if profile.StaffID != "EMP-001" {
        t.Fatalf("expected staff_id EMP-001, got %q", profile.StaffID)
    }
}

func TestRegister_DuplicateStaffIDReturns409(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    createTestProfileWithStaffID(t, q, "EMP-DUP") // new helper, mirrors createTestPendingProfile

    body := `{"email":"other@example.com","password":"Password1!","display_name":"Other User","phone":"0123456789","staff_id":"EMP-DUP"}`
    req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusConflict {
        t.Fatalf("expected 409 duplicate staff_id, got %d: %s", w.Code, w.Body.String())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/http/handlers/... -run TestRegister -v`

- [ ] **Step 3: Implement**

Add `StaffID string \`json:"staff_id" binding:"required"\`` to
the register request struct. Trim and validate length <=64,
non-empty (mirror the `DisplayName` validation exactly - find and
copy its error-response shape).

Remove the `generateMemberID` call and `MemberID: memberID` field -
replace with `MemberID: pgtype.Text{Valid: false}` and
`StaffID: strings.TrimSpace(req.StaffID)` in
`CreateProfileParams`.

Find how `CreateUser`'s `isUniqueViolation` check works for the email
conflict (same handler, nearby) and add the identical pattern around
the `CreateProfile` call: on a unique-constraint violation (check the
constraint name if pgx exposes it, else check the generic unique-
violation code and the specific column via error message matching
whatever this codebase's existing `isUniqueViolation` helper already
does for other columns), return 409 with a clear message
"nombor staff ini sudah didaftarkan" instead of falling through to the
generic 500.

Do **not** delete `generateMemberID` (auth.go:329) - Task 5 reuses it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/http/handlers/... -run TestRegister -v`

- [ ] **Step 5: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Fix any other test that asserted `member_id` is set immediately after
register.

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/auth.go internal/http/handlers/auth_test.go
git commit -m "feat: require staff_id at registration, defer member_id, handle duplicates"
```

---

### Task 5: `POST /members/:id/verify-staff-id` handler + route

**Files:**
- Modify: `internal/http/handlers/profile.go` (add `VerifyStaffID` near `ApproveMember`/`RejectMember`)
- Modify: `internal/http/router.go`
- Modify: `internal/audit/audit.go` (add `EntityStaffIDVerification`)
- Test: `internal/http/handlers/profile_test.go`

**Interfaces:**
- Consumes: `q.VerifyStaffID` (Task 3), `generateMemberID` (existing), `authz.IsAtLeastRole` (existing).
- Produces: `ProfileHandler.VerifyStaffID(c *gin.Context)`, route `POST /members/:id/verify-staff-id` (note: NO `/admin` prefix).

- [ ] **Step 1: Write the failing tests**

```go
func TestVerifyStaffID_RequiresManagerRank(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    supervisor := createTestApprovedProfile(t, q, "supervisor")
    target := createTestPendingProfile(t, q)

    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/verify-staff-id", target.UserID), nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, supervisor))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusForbidden {
        t.Fatalf("expected 403 for supervisor rank, got %d: %s", w.Code, w.Body.String())
    }
}

func TestVerifyStaffID_RejectsSelfVerify(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")

    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/verify-staff-id", manager.UserID), nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 self-verify rejected, got %d: %s", w.Code, w.Body.String())
    }
}

func TestVerifyStaffID_RejectsRejectedMember(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestRejectedProfile(t, q) // new helper, mirrors createTestPendingProfile

    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/verify-staff-id", target.UserID), nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusConflict {
        t.Fatalf("expected 409 for rejected member, got %d: %s", w.Code, w.Body.String())
    }
}

func TestVerifyStaffID_AssignsMemberID(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q)

    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/verify-staff-id", target.UserID), nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
    }

    updated, err := q.GetProfileByUserID(context.Background(), target.UserID)
    if err != nil {
        t.Fatal(err)
    }
    if !updated.MemberID.Valid || updated.MemberID.String == "" {
        t.Fatal("expected member_id assigned after verification")
    }
}

func TestVerifyStaffID_IdempotentSecondCall(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q)

    doVerify := func() *httptest.ResponseRecorder {
        req := httptest.NewRequest(http.MethodPost,
            fmt.Sprintf("/members/%s/verify-staff-id", target.UserID), nil)
        req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
        w := httptest.NewRecorder()
        router.ServeHTTP(w, req)
        return w
    }

    first := doVerify()
    if first.Code != http.StatusOK {
        t.Fatalf("first verify failed: %d %s", first.Code, first.Body.String())
    }
    var firstResp struct{ MemberID string `json:"member_id"` }
    json.Unmarshal(first.Body.Bytes(), &firstResp)

    second := doVerify()
    if second.Code != http.StatusOK {
        t.Fatalf("expected 200 idempotent re-verify, got %d: %s", second.Code, second.Body.String())
    }
    var secondResp struct{ MemberID string `json:"member_id"` }
    json.Unmarshal(second.Body.Bytes(), &secondResp)
    if secondResp.MemberID != firstResp.MemberID {
        t.Fatalf("expected same member_id on idempotent re-verify, got %q vs %q", firstResp.MemberID, secondResp.MemberID)
    }
}

func TestVerifyStaffID_UnknownUserReturns404(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")

    req := httptest.NewRequest(http.MethodPost,
        "/members/00000000-0000-0000-0000-000000000000/verify-staff-id", nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusNotFound {
        t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
    }
}

func TestVerifyStaffID_OverrideOnAlreadyVerifiedReturns409(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q)
    verifyStaffID(t, q, target.UserID, manager.UserID) // already verified

    body := `{"staff_id":"EMP-TYPO-FIX"}`
    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/verify-staff-id", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusConflict {
        t.Fatalf("expected 409 pointing to the correction endpoint, got %d: %s", w.Code, w.Body.String())
    }
}

func TestVerifyStaffID_MalformedIDReturns400(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")

    req := httptest.NewRequest(http.MethodPost, "/members/not-a-uuid/verify-staff-id", nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 malformed id, got %d: %s", w.Code, w.Body.String())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/http/handlers/... -run TestVerifyStaffID -v`

- [ ] **Step 3: Add the audit entity constant**

```go
const EntityStaffIDVerification Entity = "staff_id_verification"
```

- [ ] **Step 4: Implement the handler**

**Read `setMemberStatus` (profile.go) FIRST - do not copy the sketch
below verbatim, it uses placeholder names for illustration only.**
Confirmed real APIs to use instead (checked against the actual code
during Opus verify round 2):
- Caller identity: `middleware.UserID(c)` (NOT `authFromContext(c)`,
  which does not exist).
- Rank check: `authz.IsAtLeastRole(ctx, h.queries, callerUserID,
  "manager")` returns `(bool, error)` - TWO return values, not a bare
  bool. Handle the error (500) same as any other query error.
- Audit: package-level `audit.Record(ctx, qtx, audit.Entry{...})`
  (check the exact `Entry` field names in `setMemberStatus`'s own
  call, ~profile.go:1341-1364) called **inside the transaction, before
  `tx.Commit`** - not a method on `ProfileHandler`, and not after
  commit. `setMemberStatus` does it this way specifically so the audit
  row and the state change commit atomically; do the same here.

```go
func (h *ProfileHandler) VerifyStaffID(c *gin.Context) {
    ctx := c.Request.Context()
    callerID := middleware.UserID(c)
    isManager, err := authz.IsAtLeastRole(ctx, h.queries, callerID, "manager")
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }
    if !isManager {
        c.JSON(http.StatusForbidden, gin.H{"error": "tidak dibenarkan"})
        return
    }

    targetUserID, err := uuid.Parse(c.Param("id"))
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
        return
    }
    if targetUserID == callerID {
        c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh sahkan nombor staff sendiri"})
        return
    }

    target, err := h.queries.GetProfileByUserID(ctx, targetUserID)
    if errors.Is(err, pgx.ErrNoRows) {
        c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
        return
    } else if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }
    if target.Status == "rejected" {
        c.JSON(http.StatusConflict, gin.H{"error": "ahli ni dah ditolak"})
        return
    }

    var body struct {
        StaffID *string `json:"staff_id"`
    }
    _ = c.ShouldBindJSON(&body)

    if target.StaffIDVerifiedAt.Valid {
        if body.StaffID != nil {
            // Already verified AND caller tried to change the value in
            // the same call - do NOT silently no-op this (v1 gap found
            // in Opus review round 2: an idempotent 200 that quietly
            // drops the override would hide a real typo-fix attempt).
            // Point them at the dedicated correction endpoint instead.
            c.JSON(http.StatusConflict, gin.H{"error": "ahli ni dah disahkan - guna PATCH /members/:id/staff-id untuk betulkan nombor staff"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"member_id": target.MemberID.String, "verified_at": target.StaffIDVerifiedAt.Time})
        return
    }

    var override pgtype.Text
    if body.StaffID != nil {
        trimmed := strings.TrimSpace(*body.StaffID)
        if trimmed == "" || len(trimmed) > 64 {
            c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff tidak sah"})
            return
        }
        override = pgtype.Text{String: trimmed, Valid: true}
    }

    tx, err := h.pool.Begin(ctx)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }
    defer tx.Rollback(ctx) // no-op after a successful Commit, same as setMemberStatus
    qtx := h.queries.WithTx(tx)

    var generatedMemberID pgtype.Text
    if !target.MemberID.Valid {
        newID, err := generateMemberID(ctx, qtx)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana nombor ahli"})
            return
        }
        generatedMemberID = pgtype.Text{String: newID, Valid: true}
    }

    updated, err := qtx.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
        UserID:      targetUserID,
        VerifiedBy:  callerID,
        StaffID: override,
        MemberID:    generatedMemberID,
    })
    if errors.Is(err, pgx.ErrNoRows) {
        // Race: someone else's verify committed between our load and our
        // write attempt (they held the row lock, so our UPDATE blocked
        // until they committed, then matched zero rows once unblocked -
        // this is NOT a stale read). Roll back via defer (the tx never
        // committed, so generateMemberID's sequence increment is fully
        // reverted - NextSequence is a table upsert, not a bare nextval,
        // confirmed during Opus verify round 2), then re-read via the
        // non-tx querier to report the winner's actual committed state.
        refreshed, rErr := h.queries.GetProfileByUserID(ctx, targetUserID)
        if rErr != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"member_id": refreshed.MemberID.String, "verified_at": refreshed.StaffIDVerifiedAt.Time})
        return
    } else if isUniqueViolation(err) { // reuse Task 4's helper
        c.JSON(http.StatusConflict, gin.H{"error": "nombor staff ini sudah digunakan"})
        return
    } else if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }

    // Audit INSIDE the transaction, before commit - mirror setMemberStatus
    // exactly (profile.go ~1341-1364), do not move this after tx.Commit.
    if err := audit.Record(ctx, qtx, audit.Entry{
        Entity:   audit.EntityStaffIDVerification,
        ActorID:  callerID,
        TargetID: targetUserID,
        // ...remaining fields per setMemberStatus's own call shape...
    }); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }

    if err := tx.Commit(ctx); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
        return
    }

    c.JSON(http.StatusOK, gin.H{"member_id": updated.MemberID.String, "verified_at": updated.StaffIDVerifiedAt.Time})
}
```

- [ ] **Step 5: Wire the route**

```go
approved.POST("/members/:id/verify-staff-id", profileHandler.VerifyStaffID)
```

- [ ] **Step 6: Run tests, confirm pass**

Run: `go test ./internal/http/handlers/... -run TestVerifyStaffID -v`

- [ ] **Step 7: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`

- [ ] **Step 8: Commit**

```bash
git add internal/http/handlers/profile.go internal/http/router.go internal/audit/audit.go internal/http/handlers/profile_test.go
git commit -m "feat: add POST /members/:id/verify-staff-id"
```

---

### Task 6: `PATCH /members/:id/staff-id` - admin/superadmin correction

**Files:**
- Modify: `internal/http/handlers/profile.go` (add `CorrectStaffID` handler)
- Modify: `internal/http/router.go`
- Modify: `internal/audit/audit.go` (add `EntityStaffIDCorrection`)
- Test: `internal/http/handlers/profile_test.go`

**Interfaces:**
- Consumes: `q.CorrectStaffID` (Task 3).
- Produces: `ProfileHandler.CorrectStaffID(c *gin.Context)`, route `PATCH /members/:id/staff-id`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCorrectStaffID_RequiresAdminRank(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager") // manager can verify but NOT correct
    target := createTestApprovedProfile(t, q, "ahli")

    body := `{"staff_id":"EMP-9999"}`
    req := httptest.NewRequest(http.MethodPatch,
        fmt.Sprintf("/members/%s/staff-id", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusForbidden {
        t.Fatalf("expected 403 for manager rank, got %d: %s", w.Code, w.Body.String())
    }
}

func TestCorrectStaffID_AdminCanCorrectAlreadyVerified(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    admin := createTestApprovedProfile(t, q, "admin")
    target := createTestApprovedProfile(t, q, "ahli") // already staff-verified via approval flow

    body := `{"staff_id":"EMP-REAL-001"}`
    req := httptest.NewRequest(http.MethodPatch,
        fmt.Sprintf("/members/%s/staff-id", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, admin))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
    }

    updated, err := q.GetProfileByUserID(context.Background(), target.UserID)
    if err != nil {
        t.Fatal(err)
    }
    if updated.StaffID != "EMP-REAL-001" {
        t.Fatalf("expected corrected staff_id, got %q", updated.StaffID)
    }
    if !updated.StaffIDVerifiedAt.Valid {
        t.Fatal("correcting staff_id must not un-verify the member")
    }
}

func TestCorrectStaffID_DuplicateReturns409(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    admin := createTestApprovedProfile(t, q, "admin")
    existing := createTestProfileWithStaffID(t, q, "EMP-TAKEN")
    target := createTestApprovedProfile(t, q, "ahli")
    _ = existing

    body := `{"staff_id":"EMP-TAKEN"}`
    req := httptest.NewRequest(http.MethodPatch,
        fmt.Sprintf("/members/%s/staff-id", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, admin))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusConflict {
        t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/http/handlers/... -run TestCorrectStaffID -v`

- [ ] **Step 3: Implement the handler**

Mirror Task 5's structure but simpler (no transaction needed - single
update, no `member_id` interaction): gate via `authz.IsAtLeastRole(ctx,
h.queries, callerID, "admin")` (two return values, 403 on `false`/error),
parse+validate `:id` as `user_id` via `middleware.UserID`-style resolution
(400 if malformed), load target via `GetProfileByUserID` (404 if
missing - **this load is the pre-image**, keep the old `staff_id`
value from it), validate body `staff_id` non-empty/<=64 chars (400),
call `CorrectStaffID`, map unique-violation (via `isUniqueViolation`,
Task 4's helper) to 409.

**Audit must record the old value, not just the new one** (gap found
in Opus verify round 2 - `audit.Record`'s `Entry` struct takes
old/new state per `setMemberStatus`'s own call, ~profile.go:1353-1359):

```go
if err := audit.Record(ctx, h.queries, audit.Entry{
    Entity:   audit.EntityStaffIDCorrection,
    ActorID:  callerID,
    TargetID: targetUserID,
    Old:      map[string]any{"staff_id": target.StaffID}, // from the pre-image load above
    New:      map[string]any{"staff_id": updated.StaffID},
}); err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "ralat pelayan"})
    return
}
```

Match the exact `Entry` field names to whatever `setMemberStatus`
actually uses (`Old`/`New` above are illustrative - read the real
struct definition first). Return 200 with the updated profile's
`staff_id`.

- [ ] **Step 4: Wire the route**

```go
approved.PATCH("/members/:id/staff-id", profileHandler.CorrectStaffID)
```

- [ ] **Step 5: Run tests, confirm pass, then full suite**

Run: `go test ./internal/http/handlers/... -run TestCorrectStaffID -v`
Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/profile.go internal/http/router.go internal/audit/audit.go internal/http/handlers/profile_test.go
git commit -m "feat: add PATCH /members/:id/staff-id for admin correction"
```

---

### Task 7: Expose `staff_id` on management member list/detail endpoints

**Files:**
- Modify: `internal/http/handlers/profile.go` (wherever `GET /members` / pending-members list, and any single-member detail response, build their JSON - grep the response struct names near `ApproveMember`)
- Test: `internal/http/handlers/profile_test.go`

**Interfaces:**
- Produces: `staff_id` and `staff_id_verified_at` fields on the member list/detail JSON response, consumed by Task 11 (Flutter pending-members screen).

- [ ] **Step 1: Locate the response struct**

Run: `grep -n "member_id" internal/http/handlers/profile.go` to find
every JSON response struct that already includes `member_id` for
management views - add the two new fields to the same structs (a
management view showing `member_id` should also show `staff_id`,
since verifying one now determines the other).

- [ ] **Step 2: Write a failing test asserting the new fields appear**

```go
func TestListPendingMembers_IncludesStaffID(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    createTestPendingProfile(t, q) // has a staff_id, unverified

    req := httptest.NewRequest(http.MethodGet, "/members?status=pending", nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if !strings.Contains(w.Body.String(), `"staff_id"`) {
        t.Fatalf("expected staff_id in response, got: %s", w.Body.String())
    }
}
```

- [ ] **Step 3: Run test to verify it fails, implement, verify it passes**

Run: `go test ./internal/http/handlers/... -run TestListPendingMembers_IncludesStaffID -v`

- [ ] **Step 4: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/http/handlers/profile.go internal/http/handlers/profile_test.go
git commit -m "feat: expose staff_id on member list/detail responses"
```

---

### Task 8: `setMemberStatus` - require staff verification, exempt fee (with the v1-bug fix)

**Files:**
- Modify: `internal/http/handlers/profile.go` (`setMemberStatus`, the `status == "approved"` branch)
- Test: `internal/http/handlers/profile_test.go`

**Interfaces:**
- Consumes: `target.StaffIDVerifiedAt` - confirm `setMemberStatus`'s existing profile fetch already selects this column (it will, if it uses `SELECT *`-backed sqlc structs); if it uses a narrower projection query, widen it.

- [ ] **Step 1: Write the failing tests**

```go
func TestApproveMember_RequiresStaffVerified(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q) // staff_id_verified_at NULL

    body := `{}`
    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/approve", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 unverified staff, got %d: %s", w.Code, w.Body.String())
    }
}

func TestApproveMember_StaffVerifiedExemptsFeeEvenWithoutPayment(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q)
    verifyStaffID(t, q, target.UserID, manager.UserID) // helper wrapping Task 3's query

    body := `{}`
    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/approve", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 - staff verified exempts fee, got %d: %s", w.Code, w.Body.String())
    }
}

func TestApproveMember_ManagerCannotForgeBypassAuditOnExemptMember(t *testing.T) {
    // The v1 bug this test guards against: a manager (rank 60, NOT
    // allowed to use bypass_payment - that's rank>=80) sends
    // bypass_payment=true on an already staff-exempt member. The
    // approval must succeed (exemption alone is sufficient), but the
    // audit record must NOT show bypass_payment=true, since the
    // manager never actually exercised bypass authority.
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    target := createTestPendingProfile(t, q)
    verifyStaffID(t, q, target.UserID, manager.UserID)

    body := `{"bypass_payment": true, "bypass_reason": ""}`
    req := httptest.NewRequest(http.MethodPost,
        fmt.Sprintf("/members/%s/approve", target.UserID), strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+testToken(t, manager))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 (exempt regardless of the bogus bypass flag), got %d: %s", w.Code, w.Body.String())
    }

    entries := getAuditEntriesForTarget(t, q, target.UserID) // match whatever audit-lookup helper exists in this test file
    for _, e := range entries {
        if strings.Contains(e.Details, `"bypass_payment":true`) {
            t.Fatalf("audit record must not show bypass_payment=true for a staff-exempt approval, got: %s", e.Details)
        }
    }
}

// NOTE (Opus verify round 2): there is deliberately NO test here named
// something like "non-exempt member still requires admin for bypass".
// Since the hard staff-verification gate (Step 3, first check) returns
// 400 before the payment-gate block runs at all, `staffExempt` is
// ALWAYS true by the time that block executes - the `else` branch
// (existing HasSucceededRegistrationPayment/BypassPayment/bypass_reason
// logic) is unreachable through this handler today, by construction,
// for every caller. A test asserting "200" while unable to construct a
// real non-exempt-but-approvable member (a v1 draft attempted this and
// produced a tautological test that only proved the exempt path works
// again) is not a regression test - it is confirmation of the same
// exempt path already covered by the two tests above. If a future
// "general member" type (non-staff, spec's flagged out-of-scope idea)
// reintroduces a real non-exempt path, add the test then, against that
// concrete code, not against a hypothetical one now.
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/http/handlers/... -run TestApproveMember -v`

- [ ] **Step 3: Implement the gate**

At the top of the `status == "approved"` branch, before the existing
payment-gate block:

```go
if !target.StaffIDVerifiedAt.Valid {
    c.JSON(http.StatusBadRequest, gin.H{"error": "nombor staff ahli ni belum disahkan - sahkan nombor staff dulu sebelum meluluskan"})
    return
}
```

Then, immediately wrapping the EXISTING payment-gate block (all four
sub-parts: the `paid → BypassPayment=false` normalization, the admin-
rank check, the `bypass_reason` check, and `HasPendingRegistrationPayment`):

```go
staffExempt := target.StaffIDVerifiedAt.Valid // always true here (the hard gate above already returned otherwise) - kept as an explicit named variable, not inlined, because a future "staff vs general member" type (noted as out of scope in the spec) would make this NOT always true, and this keeps that extension point visible
if staffExempt {
    req.BypassPayment = false // MUST run before anything below reads req.BypassPayment for audit purposes - this is the exact line that fixes the v1 bug: a manager's forged bypass_payment=true must never reach the audit log as if it were exercised
} else {
    // ...ALL FOUR existing sub-parts of the payment gate, completely unchanged...
}
```

Confirm the audit-record call after the transaction reads
`req.BypassPayment` (post-mutation) rather than a value captured
earlier - if it captured an earlier copy, fix that too, since the
forced-false assignment above only closes the hole if the audit log
actually observes it.

For the `HasPendingRegistrationPayment` sub-part specifically: per the
spec, do NOT hard-block a staff-exempt approval over a pending
(unrelated) bill - but DO add a log line (`log.Warn` or whatever this
codebase's logger convention is) when `staffExempt && hasPendingPayment`,
so staff have a paper trail to manually refund if that pending bill
later resolves as `succeeded`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/http/handlers/... -run TestApproveMember -v`
Also run the full `TestApproveMember*`/`TestRejectMember*` suite -
existing tests may need `verifyStaffID(t, q, ...)` added to their
setup now that approval requires it; fix each fixture, not the gate.

- [ ] **Step 5: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/profile.go internal/http/handlers/profile_test.go
git commit -m "feat: require staff verification before approval, exempt fee without weakening bypass audit"
```

---

### Task 9: `GET /me/payments` - outstanding fee flag

**Files:**
- Modify: `internal/http/handlers/payments.go` (`Mine` handler)
- Test: `internal/http/handlers/payments_test.go`

**Interfaces:**
- Produces: response gains `outstanding_registration_fee: bool`.
- Consumes: an ADDITIONAL `h.queries.GetProfileByUserID` call - the v1 plan incorrectly assumed a `profile` var already existed in `Mine`; it does not, add the call.

- [ ] **Step 1: Write the failing tests**

```go
func TestMinePayments_OutstandingFeeTrueForUnverifiedUnpaid(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    member := createTestPendingProfile(t, q)

    req := httptest.NewRequest(http.MethodGet, "/me/payments", nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, member))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    var resp struct{ OutstandingRegistrationFee bool `json:"outstanding_registration_fee"` }
    json.Unmarshal(w.Body.Bytes(), &resp)
    if !resp.OutstandingRegistrationFee {
        t.Fatal("expected true for unverified unpaid member")
    }
}

func TestMinePayments_OutstandingFeeFalseWhenStaffVerified(t *testing.T) {
    router, q := newTestRouterWithQueries(t)
    manager := createTestApprovedProfile(t, q, "manager")
    member := createTestPendingProfile(t, q)
    verifyStaffID(t, q, member.UserID, manager.UserID)

    req := httptest.NewRequest(http.MethodGet, "/me/payments", nil)
    req.Header.Set("Authorization", "Bearer "+testToken(t, member))
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    var resp struct{ OutstandingRegistrationFee bool `json:"outstanding_registration_fee"` }
    json.Unmarshal(w.Body.Bytes(), &resp)
    if resp.OutstandingRegistrationFee {
        t.Fatal("expected false once staff verified")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/http/handlers/... -run TestMinePayments_Outstanding -v`

- [ ] **Step 3: Implement**

In `Mine`, add `profile, err := h.queries.GetProfileByUserID(ctx,
callerID)` (handle error the same way the rest of the handler already
handles caller-profile lookups elsewhere in this file). Compute:

```go
outstanding := !profile.StaffIDVerifiedAt.Valid && !hasSucceededPayment(registrationFee)
```

where `hasSucceededPayment` checks the already-built
`registration_fee` slice for any entry with `status == "succeeded"`.
Add `"outstanding_registration_fee": outstanding` to the response.

- [ ] **Step 4: Run tests, confirm pass, then full suite**

Run: `go test ./internal/http/handlers/... -run TestMinePayments -v`
Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/http/handlers/payments.go internal/http/handlers/payments_test.go
git commit -m "feat: expose outstanding_registration_fee on GET /me/payments"
```

---

### Task 10: Flutter - nullable `memberId` across all models

**Files:**
- Modify: `lib/features/profile/profile_providers.dart` (two sites, `~line 128, 352`)
- Modify: wherever `member_detail_model.dart` lives (`~line 73`)
- Modify: `lib/features/posts/post_models.dart` (`~line 23`)
- Test: matching test files for each

**Interfaces:**
- Produces: `Profile.memberId String?`, `MemberDetail.memberId String?` (or matching class names - confirm exact names via `grep -rn "memberId" lib/`), `Author.memberId String?` (if `post_models.dart`'s field is on `Author`).

- [ ] **Step 1: Grep every hard cast**

Run: `grep -rn "member_id'\] as String\b" lib/` - this must return
ZERO results after this task; every one becomes `as String?`.

- [ ] **Step 2: Write failing tests for each model**

For each model file, mirror whatever existing "field is missing"
back-compat test exists (e.g. the `donations` pattern documented in
`marc_flutter/TODO.md`'s L33 section) - add an equivalent asserting
`memberId` parses to `null` when the JSON key is absent, and does not
throw.

```dart
test('memberId is null when member_id key is absent', () {
  final profile = Profile.fromJson({/* ...other required fields..., no member_id key */});
  expect(profile.memberId, isNull);
});
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `flutter test test/features/profile/` (and the other two
model test dirs) - expect a `TypeError` or a failing assertion.

- [ ] **Step 4: Implement**

Change each `String memberId` field to `String? memberId`, parsed as
`json['member_id'] as String?`. Update every UI site that displays
`memberId` to show a fallback ("Belum disahkan" / "-") when null -
find each display site via
`grep -rn "\.memberId\b" lib/features/profile lib/features/admin
lib/features/posts` and fix each one, not just the model.

- [ ] **Step 5: Run tests to verify they pass**

Run: `flutter test test/features/profile/ test/features/admin/ test/features/posts/`

- [ ] **Step 6: Run full Flutter suite**

Run: `flutter analyze && flutter test`

- [ ] **Step 7: Commit**

```bash
git add lib/features/profile lib/features/admin lib/features/posts test/features/profile test/features/admin test/features/posts
git commit -m "fix: handle nullable member_id across Profile, MemberDetail, and Author models"
```

---

### Task 11: Flutter - `staff_id` field on the register screen

**Files:**
- Modify: register form file (`grep -rl "display_name" lib/features/auth/`)
- Modify: wherever `AuthService.register(...)` builds the request body
- Test: matching widget test file

**Interfaces:**
- Produces: `AuthService.register(..., staffNumber: String)` - request body gains `staff_id`.

- [ ] **Step 1: Locate the existing field pattern**

Run: `grep -rn "display_name\|displayName" lib/features/auth/*.dart`

- [ ] **Step 2: Write the failing widget test**

```dart
testWidgets('shows validation error when staff ID is empty', (tester) async {
  await pumpRegisterPage(tester);
  await tester.tap(find.text('Daftar'));
  await tester.pump();
  expect(find.text('Nombor staff wajib diisi'), findsOneWidget);
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `flutter test test/features/auth/register_page_test.dart`

- [ ] **Step 4: Implement**

Add a `TextFormField` for "Nombor Staff" matching the `display_name`
field's exact widget pattern, validator returning `'Nombor staff wajib
diisi'` when trimmed-empty or >64 chars. Wire it into the submit
handler's `AuthService.register(...)` call with a new `staffNumber`
named parameter serialized to `staff_id` in the JSON body.

- [ ] **Step 5: Run test to verify it passes, then full suite**

Run: `flutter test test/features/auth/register_page_test.dart`
Run: `flutter analyze && flutter test`
(fix any existing register-page test submitting a full valid form
without staff ID)

- [ ] **Step 6: Commit**

```bash
git add lib/features/auth/ test/features/auth/
git commit -m "feat: add mandatory staff ID field to registration"
```

---

### Task 12: Flutter - verify + correct staff ID actions on pending members screen

**Files:**
- Modify: `lib/features/admin/pending_members_page.dart` (or wherever `ApproveMember`/`RejectMember` buttons live - `grep -rl "approveMember\|ApproveMember" lib/`)
- Modify: the matching repository/providers file
- Test: matching widget/provider test file

**Interfaces:**
- Consumes: `POST /members/:id/verify-staff-id` (Task 5), `PATCH /members/:id/staff-id` (Task 6), `staff_id`/`staff_id_verified_at` on the list response (Task 7).
- Produces: `MembersRepository.verifyStaffID(String userId)`, `MembersRepository.correctStaffID(String userId, String staffNumber)`.

- [ ] **Step 1: Locate the existing approve/reject action pattern**

Run: `grep -rn "approveMember\|ApproveMember" lib/features/admin/*.dart`

- [ ] **Step 2: Write failing provider tests**

Mirror whatever test exists for `approveMember`, adding equivalents
for both `verifyStaffID` and `correctStaffID` against mocked
endpoints, asserting the parsed response and the error mapping for a
403/409/404.

- [ ] **Step 3: Run tests to verify they fail**

Run: `flutter test test/features/admin/members_providers_test.dart`

- [ ] **Step 4: Implement the repository methods**

Add both methods calling their respective endpoints with the same
Dio instance/error-mapping pattern as `approveMember`.

- [ ] **Step 5: Add the UI**

On each pending-member list item: show `staff_id` (from Task 7's
response) next to a "Sahkan Nombor Staff" button, visible only when
the viewer's rank is >= manager (reuse/add an
`isAtLeastManagerProvider` if `manage_providers.dart` doesn't already
expose one - check before adding a duplicate). Disable/hide "Luluskan"
until `staffNumberVerifiedAt != null` for that row, with a tooltip
"Sahkan nombor staff dulu" (mirrors the backend's hard gate so
managers don't hit an avoidable 400). Separately, on the member detail
view (any screen, not just pending), add a "Betulkan Nombor Staff"
action visible only when the viewer's rank is >= admin (reuse
`isSuperAdminProvider`'s sibling check if one exists for admin-rank,
or add `isAtLeastAdminProvider` following the same pattern) - this one
is NOT limited to pending members, since correction applies to already
-verified members too (the backfilled-user use case from the product
requirement).

- [ ] **Step 6: Run tests, confirm pass, then full suite**

Run: `flutter test test/features/admin/members_providers_test.dart`
Run: `flutter analyze && flutter test`

- [ ] **Step 7: Commit**

```bash
git add lib/features/admin/ test/features/admin/
git commit -m "feat: add staff ID verify and correct actions to admin screens"
```

---

### Task 13: Flutter - outstanding fee banner on payment history page

**Files:**
- Modify: `lib/features/payments/payment_history_page.dart`
- Modify: `lib/features/payments/payment_providers.dart` (add `outstandingRegistrationFee` to `MyPaymentHistory`)
- Test: `test/features/payments/payment_models_test.dart`, plus a widget test

**Interfaces:**
- Consumes: `outstanding_registration_fee` (Task 9).
- Produces: `MyPaymentHistory.outstandingRegistrationFee bool` (default `false` if key missing - same back-compat style as the existing `donations` field).

- [ ] **Step 1: Write the failing model tests**

```dart
test('outstandingRegistrationFee defaults false when key missing', () {
  final history = MyPaymentHistory.fromJson({'registration_fee': [], 'activity_fees': [], 'donations': []});
  expect(history.outstandingRegistrationFee, isFalse);
});

test('outstandingRegistrationFee parses true', () {
  final history = MyPaymentHistory.fromJson({'registration_fee': [], 'activity_fees': [], 'donations': [], 'outstanding_registration_fee': true});
  expect(history.outstandingRegistrationFee, isTrue);
});
```

- [ ] **Step 2: Run tests to verify they fail, then implement**

Run: `flutter test test/features/payments/payment_models_test.dart`
Add `final bool outstandingRegistrationFee;` parsed as
`json['outstanding_registration_fee'] as bool? ?? false`.

- [ ] **Step 3: Add the banner widget**

Above the `registration_fee[]` list in `payment_history_page.dart`,
show a banner when `history.outstandingRegistrationFee`, with a button
routing to the existing `CheckoutPage` (`context.push('/checkout',
extra: CheckoutRequest(...))`) - reuse it, do not build a new checkout
flow.

- [ ] **Step 4: Write a widget test for the banner**

```dart
testWidgets('shows outstanding fee banner when flag is true', (tester) async {
  await pumpPaymentHistoryPage(tester, outstandingRegistrationFee: true);
  expect(find.text('Yuran pendaftaran belum dibayar'), findsOneWidget);
});

testWidgets('hides outstanding fee banner when flag is false', (tester) async {
  await pumpPaymentHistoryPage(tester, outstandingRegistrationFee: false);
  expect(find.text('Yuran pendaftaran belum dibayar'), findsNothing);
});
```

- [ ] **Step 5: Run tests, confirm pass, then full suite**

Run: `flutter test test/features/payments/`
Run: `flutter analyze && flutter test`

- [ ] **Step 6: Commit**

```bash
git add lib/features/payments/ test/features/payments/
git commit -m "feat: show outstanding registration fee banner on payment history page"
```

---

### Task 14: End-to-end sanity pass + TODO.md updates

**Files:**
- Modify: `marc_go/TODO.md`
- Modify: `marc_flutter/TODO.md`

- [ ] **Step 1: Run both full suites one final time**

`marc_go`: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
`marc_flutter`: `flutter analyze && flutter test`

- [ ] **Step 2: Manual smoke test (documented, not automated)**

Register with a staff ID → confirm `member_id` absent from `/me`
→ as manager, verify → confirm `/me` now shows `member_id` → approve
(should succeed without payment) → confirm `GET /me/payments` shows
`outstanding_registration_fee: false` → as admin, correct that
member's `staff_id` → confirm it changed but `member_id` and
verified status did not. Also confirm a manager attempting the
correct-endpoint gets 403.

- [ ] **Step 3: Update `marc_go/TODO.md` and `marc_flutter/TODO.md`**

Follow this codebase's existing dated-section style (see the ToyyibPay/
RBAC sections for the format).

- [ ] **Step 4: Commit** (in both repos separately)

```bash
git add TODO.md
git commit -m "docs: record staff ID verification feature in TODO.md"
```

---

## Self-review notes (v2)

- **All Critical/High findings from the Opus v1 review are addressed**: C1 (wrong ID/query, Task 3/5 now use `user_id`/`GetProfileByUserID`), C2 (bypass-audit forgery, Task 8's `req.BypassPayment = false` fix + dedicated test), C3 (Flutter null crashes, Task 10 added), C4 (member_id blast radius, Task 2 added), H1 (backfill over-exemption, Task 1 fixed), H2 (duplicate staff_id 500, Task 4 fixed), H3 (route path, corrected throughout).
- **Medium findings addressed**: M1 (transaction wrapping, Task 5 Step 4), M3 (correction endpoint, Task 6 - this doubles as the user's own follow-up requirement), M4 (member_id numbering semantics, documented as intended in spec Q1), M5 (down migration, Task 1 Step 2), M6 (Mine handler profile fetch, Task 9 Step 3).
- **Low findings addressed**: L1 (self-lockout, Task 5), L3 (malformed-id 400, rejected-member 409, staff_id exposure - Task 7).
- **New from user follow-up**: activity-fee-gated-join confirmed as already-built/unaffected (no task needed, documented in spec); admin/superadmin staff-id correction is Task 6 + the correction half of Task 12.

## Opus verify round 2 (2026-09-02) - confirmed fixed, plus small corrections applied

Independent re-verification confirmed every Critical/High/Medium/Low
finding from round 1 is structurally correct in v2 (not just
addressed in wording) - including the two trickiest ones: the
`req.BypassPayment = false` fix genuinely closes the audit-forgery
hole (`setMemberStatus`'s `req` is passed by value and the audit read
happens after the payment-gate block, so the assignment is observed),
and the transaction/rollback logic in Task 5 is race-safe (`WithTx`
is real, `NextSequence` is a table upsert so rollback fully reverts
it, and an `ErrNoRows` after our `UPDATE ... WHERE
staff_id_verified_at IS NULL` can only mean the other writer's
transaction already committed - not a stale read, since our UPDATE
would have blocked on the row lock otherwise).

Round 2 found five smaller issues, all fixed directly in this
document: Task 2's file list was missing `profile.go:500` and had the
wrong path for `audit_helpers.go` (now corrected); Task 5's handler
sketch used three APIs that don't exist in this codebase
(`authFromContext`, a single-arg `IsAtLeastRole`, `h.audit.Record` as
a method) - replaced with the real `middleware.UserID`,
`authz.IsAtLeastRole(ctx, q, userID, role) (bool, error)`, and
package-level `audit.Record(ctx, qtx, audit.Entry{...})` called inside
the transaction before commit; Task 5 also silently no-op'd a
staff_id override on an already-verified profile - now returns
409 pointing at the correction endpoint, with a test; Task 6's audit
call was missing the pre-image (old `staff_id` value) - now
captured from the pre-load; and Task 8's tautological "non-exempt"
test (which could not actually construct a non-exempt approvable
member) was replaced with an explanatory note instead of a fake test.
The spec's "Kesan logik"/toyyibpay-retained paragraph was also
corrected - the payment-gate `else` branch is confirmed **dead code
today** (unreachable, since verification is now mandatory for any
approval), kept only as a future extension point, not as a live
"pay-to-bypass-unverified" path as v1's wording implied.
