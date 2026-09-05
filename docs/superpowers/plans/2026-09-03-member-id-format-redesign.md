# Format Nombor Ahli Baharu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change how `generateMemberID` builds new member numbers to `MARC-{staff_id}/{year}-{code}`, where `{code}` is a 4-digit global sequence for ordinary members, `T{n}`/`P{n}` global per-role sequences for tester/penaung, and a fixed `SA` literal for superadmin. Add an admin-only endpoint to directly correct an already-assigned `member_id`. No database migration — `profiles.member_id` is already nullable with a unique index from the staff-id-verification feature (staged, uncommitted).

**Architecture:** Backend-only (`marc_go`). Four tasks, mostly sequential: (1) redesign `generateMemberID` and its one call site, including a guard against embedding a backfilled placeholder `staff_id`; (2) add a minimal `staff_id` character guard so the new embedded format can't get visually corrupted; (3) harden the ALREADY-STAGED `CorrectStaffID` endpoint with a self-lockout + target-rank check that a design review found missing; (4) add the `PATCH /members/:id/member-id` correction endpoint, built on Task 3's now-hardened pattern. No Flutter changes — member_id is already displayed as an opaque string everywhere in the app; only the string's shape changes for newly-generated values.

**Spec:** `docs/superpowers/specs/2026-09-03-member-id-format-redesign-design.md` (already incorporates a full round of Opus review — read it in full, not just the Q&A section, especially the "Aksara `staff_id`" section which corrects an earlier factual error about receipt filenames, and the self-lockout/rank-guard finding that Task 3 exists to fix).

**Depends on:** `docs/superpowers/plans/2026-09-02-staff-id-verification.md` Tasks 1-9 (staged, uncommitted, already implemented in this working tree) — this plan builds directly on that code (`generateMemberID`, `VerifyStaffID`, `CorrectStaffID`, `staff_id` columns). Do not implement this plan against a tree that doesn't already have that work.

## Global Constraints

- No new migration. `profiles.member_id` is already `pgtype.Text` (nullable) with a unique index (`profiles_member_id_key`, confirmed existing from before the staff-id feature).
- `generateMemberID`'s new parameters (`staffID`, `roleKey`) come from data the ONE existing caller (`VerifyStaffID`, `profile.go`) already has in hand — no new query.
- The role→code mapping must have an explicit default branch (ordinary numeric code) for any `roleKey` not specifically listed — never panic or error on an unrecognized role.
- `PATCH /members/:id/member-id` requires rank >= 80 (`admin`), matching `PATCH /members/:id/staff-id`'s gate exactly — copy that handler's structure.
- The correction endpoint must reject (409) correcting a profile whose `member_id` is currently NULL — it corrects existing values, it must never be usable to assign a first member_id and bypass the verification gate.
- Existing (already-generated, old-format) `member_id` values are never touched by this plan — no backfill, no reformatting.
- All backend code must pass `go build ./...`, `go vet ./...`, `gofmt -l .` (empty output), and `go test ./...` (including against a real Postgres DB via `HANDLER_TEST_DB`, this repo's established convention — see `internal/http/handlers/verify_staff_id_live_test.go` for the pattern) before each task is considered done.
- Stage with `git add` only — do not commit (per current session convention).

---

### Task 1: Redesign `generateMemberID` — role-based code, global sequences

**Files:**
- Modify: `internal/http/handlers/auth.go` (the `generateMemberID` function, currently at line 345)
- Modify: `internal/http/handlers/profile.go` (the one call site inside `VerifyStaffID`, currently at line 1596: `generated, err := generateMemberID(ctx, qtx)`)
- Test: new `internal/http/handlers/generate_member_id_test.go` (plain unit test, no DB needed for the pure string-formatting logic — but the `NextSequence` call needs a real `*sqlc.Queries`, so this may need to be a `_live_test.go` instead; check whether `NextSequence` can be tested without a DB connection, and if not, follow the `_live_test.go`/`HANDLER_TEST_DB` convention like every other DB-touching test in this package)

**Interfaces:**
- Produces: `generateMemberID(ctx context.Context, q *sqlc.Queries, staffID string, roleKey string) (string, error)` — replaces the current two-parameter signature. The one caller (`VerifyStaffID`) is updated in this same task.

- [ ] **Step 1: Write the failing tests**

Using this package's `_live_test.go` convention (`statusTestPool(t)` for a real Postgres connection, since `NextSequence` needs the `sequences` table):

**NOTE on test design**: each test below calls `generateMemberID` TWICE
with the SAME `staffID` to prove the sequence actually advances between
calls — a v1 draft of this plan varied `staffID` between the two calls
instead, which only proves the two IDs differ (they'd differ anyway,
since `staffID` is embedded verbatim, regardless of whether the
sequence moved at all). Also use the SAME `Asia/Kuala_Lumpur` location
the implementation uses for computing the expected year — a bare
`time.Now().Format("2006")` on a server running in a different
timezone can disagree with the implementation for a few hours around
New Year.

```go
var testMYTLocation = func() *time.Location {
    loc, err := time.LoadLocation("Asia/Kuala_Lumpur")
    if err != nil {
        return time.FixedZone("MYT", 8*60*60)
    }
    return loc
}()

func TestGenerateMemberID_OrdinaryMemberSequenceAdvances(t *testing.T) {
    pool, ctx := statusTestPool(t)
    q := sqlc.New(pool)

    id1, err := generateMemberID(ctx, q, "0110", "ahli")
    if err != nil {
        t.Fatal(err)
    }
    year := time.Now().In(testMYTLocation).Format("2006")
    if !strings.HasPrefix(id1, fmt.Sprintf("MARC-0110/%s-", year)) {
        t.Fatalf("unexpected prefix: %s", id1)
    }
    if !regexp.MustCompile(`-\d{4}$`).MatchString(id1) {
        t.Fatalf("expected 4-digit zero-padded suffix, got: %s", id1)
    }

    // SAME staffID — proves the sequence, not just the embedded
    // staff_id, is what makes the second call distinct.
    id2, err := generateMemberID(ctx, q, "0110", "ahli")
    if err != nil {
        t.Fatal(err)
    }
    if id1 == id2 {
        t.Fatal("expected the ahli sequence to advance between calls with the same staff_id")
    }
}

func TestGenerateMemberID_TesterGetsNumberedTCode(t *testing.T) {
    pool, ctx := statusTestPool(t)
    q := sqlc.New(pool)

    id1, err := generateMemberID(ctx, q, "0200", "tester")
    if err != nil {
        t.Fatal(err)
    }
    if !regexp.MustCompile(`-T\d+$`).MatchString(id1) {
        t.Fatalf("expected T<n> suffix, got: %s", id1)
    }

    // SAME staffID — proves the tester sequence advances independently.
    id2, err := generateMemberID(ctx, q, "0200", "tester")
    if err != nil {
        t.Fatal(err)
    }
    if id1 == id2 {
        t.Fatal("expected the tester sequence to advance between calls with the same staff_id")
    }
}

func TestGenerateMemberID_AhliAndTesterSequencesAreIndependent(t *testing.T) {
    // Proves member_seq:ahli and member_seq:tester are genuinely
    // separate counters, not the same key reused — interleave calls
    // and confirm neither role's numbering is disturbed by the other.
    pool, ctx := statusTestPool(t)
    q := sqlc.New(pool)

    ahli1, err := generateMemberID(ctx, q, "0500", "ahli")
    if err != nil {
        t.Fatal(err)
    }
    tester1, err := generateMemberID(ctx, q, "0500", "tester")
    if err != nil {
        t.Fatal(err)
    }
    ahli2, err := generateMemberID(ctx, q, "0500", "ahli")
    if err != nil {
        t.Fatal(err)
    }
    if ahli1 == ahli2 {
        t.Fatal("ahli sequence did not advance despite an interleaved tester call")
    }
    if !regexp.MustCompile(`-T\d+$`).MatchString(tester1) {
        t.Fatalf("expected T<n> suffix for the interleaved tester call, got: %s", tester1)
    }
}

func TestGenerateMemberID_SuperadminGetsLiteralSACode(t *testing.T) {
    pool, ctx := statusTestPool(t)
    q := sqlc.New(pool)

    year := time.Now().In(testMYTLocation).Format("2006")

    id, err := generateMemberID(ctx, q, "0300", "superadmin")
    if err != nil {
        t.Fatal(err)
    }
    want := fmt.Sprintf("MARC-0300/%s-SA", year)
    if id != want {
        t.Fatalf("expected %q, got %q", want, id)
    }

    // Calling it again for the SAME staff_id must still yield "SA"
    // with no sequence drawn (no NextSequence key for "superadmin" at
    // all) — per the spec's Q2 decision (superadmin uniqueness is a
    // policy, not enforced in code, so repeated calls are idempotent
    // in shape even though real code only calls this once per profile).
    id2, err := generateMemberID(ctx, q, "0300", "superadmin")
    if err != nil {
        t.Fatal(err)
    }
    if id2 != want {
        t.Fatalf("expected superadmin code to stay %q with no sequence draw, got %q", want, id2)
    }
}

func TestGenerateMemberID_UnknownRoleFallsBackToOrdinary(t *testing.T) {
    pool, ctx := statusTestPool(t)
    q := sqlc.New(pool)

    id, err := generateMemberID(ctx, q, "0400", "manager")
    if err != nil {
        t.Fatal(err)
    }
    if !regexp.MustCompile(`-\d{4}$`).MatchString(id) {
        t.Fatalf("expected ordinary 4-digit suffix for role not in the special map, got: %s", id)
    }
}
```

**Also add, in this same step, a test on `VerifyStaffID` itself (not
`generateMemberID` directly)** proving the placeholder-`staff_id` guard
from Step 3 below works — `TestVerifyStaffID_RejectsBackfilledPlaceholderStaffIDWithoutOverride`:
seed a profile whose `staff_id` was set to its own `user_id` (mirroring
the migration's backfill — use `createTestPendingProfile`'s existing
seeding if it already does this, or seed it explicitly), call
`VerifyStaffID` with NO `staff_id` override in the body, and assert
400. Then confirm the SAME call WITH an override succeeds (200) and
the resulting `member_id` embeds the override value, not the
placeholder UUID.

- [ ] **Step 2: Run tests to verify they fail**

Run: `HANDLER_TEST_DB=<scratch-db-dsn> go test ./internal/http/handlers/... -run TestGenerateMemberID -v`
Expected: FAIL (compile error — signature doesn't match yet).

- [ ] **Step 3: Implement**

Replace `generateMemberID` in `auth.go`:

```go
// memberIDCode returns the {code} segment for a newly-generated
// member_id, and the sequence key to draw from if the role needs a
// number (empty key means no sequence is drawn — currently only
// "superadmin"). Adding a future role (e.g. "penaung") is one entry
// here, per docs/superpowers/specs/2026-09-03-member-id-format-redesign-design.md.
func memberIDCode(ctx context.Context, q *sqlc.Queries, roleKey string) (string, error) {
    switch roleKey {
    case "superadmin":
        return "SA", nil
    case "tester":
        seq, err := q.NextSequence(ctx, "member_seq:tester")
        if err != nil {
            return "", err
        }
        return fmt.Sprintf("T%d", seq), nil
    // case "penaung": // future role, not yet created — same pattern as "tester":
    //     seq, err := q.NextSequence(ctx, "member_seq:penaung")
    //     if err != nil { return "", err }
    //     return fmt.Sprintf("P%d", seq), nil
    default:
        seq, err := q.NextSequence(ctx, "member_seq:ahli")
        if err != nil {
            return "", err
        }
        return fmt.Sprintf("%04d", seq), nil
    }
}

func generateMemberID(ctx context.Context, q *sqlc.Queries, staffID string, roleKey string) (string, error) {
    loc, err := time.LoadLocation("Asia/Kuala_Lumpur")
    if err != nil {
        loc = time.FixedZone("MYT", 8*60*60)
    }
    year := time.Now().In(loc).Format("2006")

    code, err := memberIDCode(ctx, q, roleKey)
    if err != nil {
        return "", err
    }

    return fmt.Sprintf("MARC-%s/%s-%s", staffID, year, code), nil
}
```

Note: the OLD format used `MARC%s/%s/%04d` (year/month/seq, no
dashes). The NEW format is `MARC-%s/%s-%s` (dash after MARC, staff_id,
slash, year, dash, code) — copy the spec's exact format string, do not
approximate it.

- [ ] **Step 4: Update the call site — effective staff_id, placeholder guard, unique-violation disambiguation**

In `profile.go`'s `VerifyStaffID`, change:

```go
generated, err := generateMemberID(ctx, qtx)
```

to (read the surrounding code first to confirm the exact local
variable names — `target`, `override`, `req.StaffID` — match what's
actually there; these are the names confirmed present when this plan
was written, but re-verify since line numbers may have shifted):

```go
effectiveStaffID := target.StaffID
if override.Valid {
    effectiveStaffID = override.String
}

// Defense-in-depth guard, NOT a fix for a currently-reachable
// production bug (confirmed during this plan's own review): every
// row that existed before the staff-id-verification migration
// already had a real (old-format) member_id, because the OLD
// registration flow generated member_id at register time for every
// signup, pending or not. member_id only starts NULL for signups
// AFTER that migration, and those always require a real staff_id at
// register time (binding:"required", non-empty). So the specific
// combination this guard checks for — staff_id still equal to the
// row's own user_id AND member_id still NULL — cannot occur from any
// real migration/registration path today. Kept anyway because it's
// a one-line, zero-cost check against a mistake in a future data
// migration or manual DB edit reintroducing that combination; do NOT
// present this in code comments or docs as blocking a known live
// bug, since it does not.
if !override.Valid && target.StaffID == target.UserID.String() {
    c.JSON(http.StatusBadRequest, gin.H{
        "error": "staff_id ahli ni masih placeholder - sila isi nombor staff sebenar semasa sahkan",
    })
    return
}

generated, err := generateMemberID(ctx, qtx, effectiveStaffID, target.RoleKey)
```

(Note this is written directly as `!override.Valid && target.StaffID
== target.UserID.String()` rather than routing through
`effectiveStaffID` — the two are equivalent when `override.Valid` is
false, since `effectiveStaffID` then equals `target.StaffID`, but
writing it this way makes clear the check is skipped entirely the
moment ANY override is supplied, including one where an admin
explicitly retypes the UUID — that's a deliberate admin action the
guard is not meant to catch, not a bypass.)

Place this guard BEFORE the `tx.Begin` call (or right after it but
before any write) — it's a validation failure, not a race condition,
so it should return before any transaction work starts if possible;
if the existing code structure already has the transaction open by
this point, returning after `defer tx.Rollback` is still correct
(rollback is a no-op on an unused transaction).

Update the Step 1 test for this
(`TestVerifyStaffID_RejectsBackfilledPlaceholderStaffIDWithoutOverride`)
with a comment noting it seeds an ARTIFICIAL fixture combination
(`staff_id == user_id` AND `member_id` NULL) that the real migration
path cannot itself produce — this is a defense-in-depth unit test, not
a regression test for an observed bug.

**Unique-violation disambiguation**: find `VerifyStaffID`'s existing
unique-violation handling (confirmed present, currently returns the
single message "nombor staff ini sudah digunakan" for any unique
violation on the write, via the shared `isUniqueViolation(err)` helper
defined in `auth.go`). Now that `member_id` values can be
independently set by `PATCH /members/:id/member-id` (Task 4), the
`VerifyStaffID` write in this handler could — rarely — collide on the
GENERATED `member_id` value instead of the `staff_id` override.

`profile.go` currently imports `pgx`/`pgtype`/`pgxpool` but NOT
`pgconn` — inspecting `pgErr.ConstraintName` needs the
`github.com/jackc/pgx/v5/pgconn` package, so add that import. Rather
than duplicate the `errors.As(err, &pgErr)` unwrap inline at this call
site, extend the EXISTING `isUniqueViolation` helper (`auth.go:689`)
with a sibling that also returns the constraint name, e.g.
`uniqueViolationConstraint(err error) (string, bool)`, and use it at
both this call site and (once Task 4 exists) `CorrectMemberID`'s —
keeping the `pgconn` unwrap logic in one place:

```go
// in auth.go, next to isUniqueViolation:
func uniqueViolationConstraint(err error) (string, bool) {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) && pgErr.Code == "23505" {
        return pgErr.ConstraintName, true
    }
    return "", false
}
```

```go
// in VerifyStaffID, replacing the current single-message unique-violation branch:
if constraint, ok := uniqueViolationConstraint(err); ok {
    switch constraint {
    case "profiles_staff_id_key":
        c.JSON(http.StatusConflict, gin.H{"error": "nombor staff ini sudah digunakan"})
    case "profiles_member_id_key":
        c.JSON(http.StatusConflict, gin.H{"error": "nombor ahli yang dijana berlanggar dengan rekod sedia ada - cuba sahkan semula"})
    default:
        c.JSON(http.StatusConflict, gin.H{"error": "konflik data - cuba semula"})
    }
    return
}
```

**Constraint name confirmed** (checked against the real migration
during this plan's review): `member_id`'s uniqueness is declared
inline as `member_id text not null unique` in
`internal/db/migrations/20260805223400_create_profiles.sql:5` (not a
separate `create unique index` statement like `profiles_staff_id_key`
is). Postgres auto-names an inline `unique` column constraint after
the table and column, so `pgErr.ConstraintName` for this one returns
exactly `profiles_member_id_key` — the literal string above is
correct, no further verification needed before using it.

- [ ] **Step 5: Run tests to verify they pass**

Run: `HANDLER_TEST_DB=<scratch-db-dsn> go test ./internal/http/handlers/... -run TestGenerateMemberID -v`
Also re-run the full `VerifyStaffID` test suite
(`TestVerifyStaffID_*` in `verify_staff_id_live_test.go`) since its
call site changed — check whether any of those tests assert on the
OLD member_id format (e.g. a regex or exact string match against
`MARC2026/09/...`) and update them to the new format if so.

- [ ] **Step 6: Run full backend suite**

Run: `go build ./... && go vet ./... && gofmt -l .` (clean, no
`HANDLER_TEST_DB` needed for this part), then create a scratch
Postgres DB and run `HANDLER_TEST_DB=<scratch-dsn> go test ./...`,
confirming zero regressions, then drop the scratch DB.

- [ ] **Step 7: Stage**

```bash
git add internal/http/handlers/auth.go internal/http/handlers/profile.go internal/http/handlers/generate_member_id_test.go internal/http/handlers/verify_staff_id_live_test.go
```

Do NOT commit.

---

### Task 2: `staff_id` character guard (`/` rejected)

**Files:**
- Modify: `internal/http/handlers/auth.go` (the `staff_id` validation added in the staff-id-verification feature's `Register` handler — find the exact trim/length check already there)
- Modify: `internal/http/handlers/profile.go` (the `staff_id` validation in `VerifyStaffID`'s override path, and in `CorrectStaffID`)
- Test: extend whatever test files already cover `staff_id` validation in each of the three call sites (`auth_register_live_test.go`, `verify_staff_id_live_test.go`, `correct_staff_id_live_test.go`)

**Interfaces:** none new — this task only tightens existing validation by one character class.

- [ ] **Step 1: Write the failing tests**

Add one test per site asserting a `staff_id` containing `/` is rejected with 400 and a clear message ("nombor staff tidak boleh mengandungi '/'"):
- Register: `TestRegister_StaffIDWithSlashRejected`
- VerifyStaffID override: `TestVerifyStaffID_OverrideStaffIDWithSlashRejected`
- CorrectStaffID: `TestCorrectStaffID_StaffIDWithSlashRejected`

Follow each file's existing test pattern exactly (same helpers, same assertion style as the neighboring `staff_id` validation tests already in each file).

- [ ] **Step 2: Run tests to verify they fail**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run 'StaffIDWithSlash' -v`

- [ ] **Step 3: Implement**

At each of the three sites, after the existing trim/length check,
add: `if strings.Contains(trimmed, "/") { return 400 "nombor staff tidak boleh mengandungi '/'" }`.
Match each site's exact existing error-response shape (some may use
`c.JSON(http.StatusBadRequest, gin.H{"error": "..."})` directly,
others may go through a shared validation helper — read each site
first, do not assume they're identical).

- [ ] **Step 4: Run tests, confirm pass, then full suite**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run 'StaffIDWithSlash' -v`
Run: `go build ./... && go vet ./... && gofmt -l . && HANDLER_TEST_DB=<scratch-dsn> go test ./...`

- [ ] **Step 5: Stage**

```bash
git add internal/http/handlers/auth.go internal/http/handlers/profile.go internal/http/handlers/auth_register_live_test.go internal/http/handlers/verify_staff_id_live_test.go internal/http/handlers/correct_staff_id_live_test.go
```

Do NOT commit.

---

### Task 3: Harden `CorrectStaffID` with self-lockout + target-rank guard

**Why this task exists**: Opus review found that `CorrectStaffID`
(`PATCH /members/:id/staff-id`, already implemented and staged from
the staff-id-verification feature) has NO self-lockout guard and NO
target-rank check — an admin (rank 80) can currently correct their
OWN `staff_id`, or a SUPERADMIN's (rank 100) `staff_id`, neither of
which should be allowed. `VerifyStaffID` already has a self-lockout
guard (`targetID == callerID`, confirmed in `profile.go`) for the same
class of risk; `CorrectStaffID` was written without it. This task
fixes the existing endpoint BEFORE Task 4 copies its (currently
incomplete) pattern for `CorrectMemberID` — do this task first.

**Files:**
- Modify: `internal/http/handlers/profile.go` (`CorrectStaffID`)
- Test: `internal/http/handlers/correct_staff_id_live_test.go`

**Interfaces:** none new — hardens existing behavior only.

- [ ] **Step 1: Write the failing tests**

```go
func TestCorrectStaffID_RejectsSelfCorrection(t *testing.T) {
    // admin tries to correct their OWN staff_id → 400, mirrors
    // VerifyStaffID's self-lockout test.
}

func TestCorrectStaffID_RejectsCorrectingHigherRank(t *testing.T) {
    // admin (rank 80) tries to correct a superadmin's (rank 100)
    // staff_id → 403.
}

func TestCorrectStaffID_AllowsCorrectingLowerRank(t *testing.T) {
    // admin correcting an ordinary ahli's staff_id still works (200)
    // — confirms the new rank check isn't overly broad.
}

func TestCorrectStaffID_RejectsCorrectingEqualRank(t *testing.T) {
    // admin (rank 80) tries to correct ANOTHER admin (rank 80) →
    // 403. House style rejects EQUAL rank too, not just strictly
    // higher — see Step 3's exact comparison operator.
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run 'TestCorrectStaffID_Rejects|TestCorrectStaffID_Allows' -v`

- [ ] **Step 3: Implement**

**Read `UpdateMemberRole` and `UpdateMemberActive` first** (`profile.go`,
confirmed present at the time this plan was written, ~lines 830-870
and ~955-970) — these are the REAL "equal-or-higher rank" pattern in
this codebase, not `RejectMember` (a v1 draft of this plan pointed at
`RejectMember`, which actually does a `RoleCategory` check, not a rank
comparison — wrong exemplar, do not use it). Both real examples:
1. Load the caller's own full profile row (`CorrectStaffID` currently
   only has `callerID uuid.UUID` from `middleware.UserID(c)` — it does
   NOT already have a `caller` variable, unlike `UpdateMemberRole`/
   `UpdateMemberActive` which call `h.queries.GetProfileByUserID(ctx,
   callerID)` for exactly this reason. You must add this call — do not
   assume a `caller` variable already exists).
2. Self-lockout: `if targetID == callerID { 400 }`.
3. Rank check, using `caller.RoleRank <= target.RoleRank` (note the
   operator: **`<=`, not `<` or `>`** — house style rejects EQUAL rank
   too, e.g. an admin correcting another admin, not just strictly
   higher rank. A v1 draft of this task used `target.RoleRank >
   caller.RoleRank`, which would incorrectly ALLOW admin-on-admin
   correction — do not use that form).

Insert into `CorrectStaffID` right after the existing `isAdminUp`
check and before loading `target` (matching where `UpdateMemberRole`
places its own caller-load + self-lockout + rank sequence):

```go
if targetID == callerID {
    c.JSON(http.StatusBadRequest, gin.H{"error": "tidak boleh betulkan nombor staff akaun sendiri"})
    return
}

caller, err := h.queries.GetProfileByUserID(ctx, callerID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal betulkan nombor staff"})
    return
}

target, err := h.queries.GetProfileByUserID(ctx, targetID)
if err != nil {
    c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
    return
}
if caller.RoleRank <= target.RoleRank {
    c.JSON(http.StatusForbidden, gin.H{"error": "tidak boleh edit ahli setaraf/lebih tinggi drpd anda"})
    return
}
```

Note `target` is loaded HERE, replacing the existing pre-image load
that was already in `CorrectStaffID` right before `bindJSON` — do not
load it twice; keep the single load (this one) and remove the
original standalone one, adjusting whatever comment referred to it as
"pre-image" so it still makes sense in its new position (it's still
the pre-image, just loaded earlier in the function now).

Use the EXACT existing error message
`"tidak boleh edit ahli setaraf/lebih tinggi drpd anda"` (copied
verbatim from `UpdateMemberRole`/`UpdateMemberActive`) rather than
inventing new wording — this keeps the error message consistent
across every "can't act on this rank" case in the API.

- [ ] **Step 4: Run tests, confirm pass, then full suite**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run TestCorrectStaffID -v`
Run: `go build ./... && go vet ./... && gofmt -l . && HANDLER_TEST_DB=<scratch-dsn> go test ./...`

- [ ] **Step 5: Stage**

```bash
git add internal/http/handlers/profile.go internal/http/handlers/correct_staff_id_live_test.go
```

Do NOT commit.

---

### Task 4: `PATCH /members/:id/member-id` — admin correction of an existing member_id

**Files:**
- Modify: `queries/profiles.sql` (add `CorrectMemberID` query, near `CorrectStaffID`)
- Modify: `internal/http/handlers/profile.go` (add `ProfileHandler.CorrectMemberID`, near `CorrectStaffID`)
- Modify: `internal/http/router.go` (wire the route, near `PATCH /members/:id/staff-id`)
- Modify: `internal/audit/audit.go` (add `EntityMemberIDCorrection`)
- Test: new `internal/http/handlers/correct_member_id_live_test.go`, following `correct_staff_id_live_test.go`'s exact structure

**Interfaces:**
- Produces: `CorrectMemberID(ctx, CorrectMemberIDParams{UserID uuid.UUID, MemberID string}) (Profile, error)` — the query itself is guarded (`WHERE user_id = $1 AND member_id IS NOT NULL`), so a target with `member_id IS NULL` returns `pgx.ErrNoRows`, which the handler must map to 409 (not 404 — the profile exists, it just has no member_id yet).

- [ ] **Step 1: Read `CorrectStaffID` first**

Read the existing `CorrectStaffID` handler and query in full (`profile.go`, `queries/profiles.sql`) — this task is structurally almost identical (admin-rank gate, pre-image load for audit old-value, simple update, unique-violation → 409, audit record). Copy its exact conventions (how it gets `callerID`, calls `authz.IsAtLeastRole`, parses `:id`, loads the target, builds the audit `Entry`).

- [ ] **Step 2: Write the failing tests**

```go
func TestCorrectMemberID_RequiresAdminRank(t *testing.T) {
    // manager (rank 60) → 403, mirrors TestCorrectStaffID_RequiresAdminRank
}

func TestCorrectMemberID_AdminCanCorrectExistingValue(t *testing.T) {
    // admin corrects an already-verified member's member_id to a new
    // string; assert the update, and assert an audit row with
    // Old/New staff_id... err, member_id values under EntityMemberIDCorrection
}

func TestCorrectMemberID_NullMemberIDReturns409(t *testing.T) {
    // target created via createTestPendingProfile (member_id NULL,
    // never verified) → 409 "ahli ni belum ada nombor ahli - sahkan
    // nombor staff dulu"
}

func TestCorrectMemberID_DuplicateReturns409(t *testing.T) {
    // two verified members; admin tries to set the first one's
    // member_id to the second one's existing value → 409
}

func TestCorrectMemberID_UnknownUserReturns404(t *testing.T) {
}

func TestCorrectMemberID_MalformedIDReturns400(t *testing.T) {
}

func TestCorrectMemberID_RejectsSelfCorrection(t *testing.T) {
    // mirrors TestCorrectStaffID_RejectsSelfCorrection from Task 3
}

func TestCorrectMemberID_RejectsCorrectingHigherRank(t *testing.T) {
    // mirrors TestCorrectStaffID_RejectsCorrectingHigherRank from Task 3
}

func TestCorrectMemberID_DoesNotTouchStaffID(t *testing.T) {
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run TestCorrectMemberID -v`

- [ ] **Step 4: Write the query**

```sql
-- name: CorrectMemberID :one
update profiles
set member_id = @member_id
where user_id = @user_id
  and member_id is not null
returning *;
```

Run `sqlc generate` afterward.

- [ ] **Step 5: Add the audit entity constant**

`audit.Entity` is NOT a distinct Go type — `internal/audit/audit.go`
declares its entity constants as plain untyped strings (confirm by
reading the file; do not write `Entity` as a type name, that will not
compile). Match the exact existing declaration style:

```go
const EntityMemberIDCorrection = "member_id_correction"
```

(placed next to `EntityStaffIDCorrection`, same const block/style).

- [ ] **Step 6: Implement the handler**

Mirror the NOW-HARDENED `CorrectStaffID` from Task 3 (including its
self-lockout and target-rank guards — do not skip them here just
because this is a "new" endpoint; the same risk applies identically),
with these differences:
- Gate: same rank >= 80 (`admin`) — unchanged.
- Self-lockout: 400 if `targetID == callerID` (copy Task 3's fix).
- Target-rank: 403 if `caller.RoleRank <= target.RoleRank` (copy Task 3's EXACT operator — `<=`, rejecting equal rank too, not `>`/`<`. This endpoint also needs its own `caller` load via `GetProfileByUserID`, same as Task 3 — `CorrectMemberID` is new code with no pre-existing `caller` variable to begin with).
- Validation: `member_id` non-empty after trim, max **128** chars (not 64 — the full format is longer than a bare `staff_id`).
- Error ordering on the write — **check `errors.Is(err, pgx.ErrNoRows)` FIRST**: this means the target's `member_id` was NULL (the query's `WHERE ... AND member_id IS NOT NULL` matched zero rows) → **409** "ahli ni belum ada nombor ahli - sahkan nombor staff dulu (`POST /members/:id/verify-staff-id`)". Only if that check is false, fall through to `isUniqueViolation(err)` → a DIFFERENT **409** ("nombor ahli ini sudah digunakan ahli lain") for an actual duplicate `member_id`. These are two distinct causes with two distinct messages — do not merge them into one generic "409 conflict", and do not check `isUniqueViolation` first (a `pgx.ErrNoRows` is never a `*pgconn.PgError`, so checking unique-violation first would silently fall through to a generic 500 for the NULL-member_id case instead of the correct 409 — verify this ordering compiles and behaves as described with a test for each branch, not just one).
- Audit: `EntityMemberIDCorrection`, `Old: {"member_id": target.MemberID.String}`, `New: {"member_id": updated.MemberID.String}` merged with actor fields, same pattern as `CorrectStaffID`.
- Also add a test confirming `CorrectMemberID` does NOT touch `staff_id`/`staff_id_verified_at` on the target (mirrors the invariant `CorrectStaffID` guarantees for its own untouched fields).

- [ ] **Step 7: Wire the route**

```go
approved.PATCH("/members/:id/member-id", profileHandler.CorrectMemberID)
```

- [ ] **Step 8: Run tests, confirm pass, then full suite**

Run: `HANDLER_TEST_DB=<scratch-dsn> go test ./internal/http/handlers/... -run TestCorrectMemberID -v`
Run: `go build ./... && go vet ./... && gofmt -l . && HANDLER_TEST_DB=<scratch-dsn> go test ./...`

- [ ] **Step 9: Stage**

```bash
git add queries/profiles.sql internal/db/sqlc internal/http/handlers/profile.go internal/http/router.go internal/audit/audit.go internal/http/handlers/correct_member_id_live_test.go
```

Do NOT commit.

---

## Self-review notes

- **Spec coverage**: role→code map with explicit default (Task 1), global non-resetting sequences per the spec's Q3 (Task 1's `memberIDCode` uses fixed keys with no year component), superadmin literal / tester numbered per Q2 (Task 1), existing member_id left untouched (no task modifies existing rows), admin-only direct-edit capability (Task 4), the `/`-in-staff_id guard (Task 2, with the corrected rationale — it's about visual/structural delimiter confusion, not filenames as a v1 draft incorrectly claimed), the backfill-placeholder guard (Task 1 Step 4), the self-lockout/rank hole in both correction endpoints (Task 3 for the existing `CorrectStaffID`, carried into Task 4 for the new `CorrectMemberID`), and the unique-violation disambiguation in both `VerifyStaffID` (Task 1 Step 4) and `CorrectMemberID` (Task 4 Step 6).
- **Not built, intentionally**: `penaung` role itself (commented-out placeholder in `memberIDCode` only, per spec's explicit scope note), any enforcement of "at most one superadmin" (spec: policy, not code), any restriction on `-` in `staff_id` (spec: accepted residual cosmetic risk, confirmed nothing parses member_id by splitting on `-`).
- **Order matters**: Task 1 must land first (Tasks 2-4 all touch code Task 1 introduces or assume its new `generateMemberID` signature). Task 3 (hardening `CorrectStaffID`) must land before Task 4 (which copies that now-hardened pattern for `CorrectMemberID`) — doing Task 4 first would propagate the self-lockout/rank gap into the new endpoint too. Task 2 is independent and can run anywhere after Task 1.
- **Fixed during this plan's own review, round 1** (Opus, 2026-09-03): Task 1's original tests varied `staff_id` between calls instead of holding it constant, which only proved two IDs differ — not that the sequence advances; rewritten to hold `staff_id` fixed per test. Task 1's original call-site update didn't guard against embedding a backfilled placeholder `staff_id` (a raw UUID) into a newly-generated `member_id`; guard added. Task 4's original audit-constant sketch used a nonexistent `Entity` type; corrected to match this codebase's untyped-string-constant convention. Task 4's original error-handling didn't specify checking `pgx.ErrNoRows` before `isUniqueViolation`, which would have misrouted the "no member_id yet" case into a generic 500; ordering now explicit. The self-lockout/target-rank gap in `CorrectStaffID` (pre-existing, staged code from a prior feature, not introduced by this plan) was found during this review and given its own fix task (Task 3) rather than left for a separate pass.
- **Fixed during round 2** (Opus re-verify, 2026-09-03): the placeholder-`staff_id` guard's framing was corrected — round 1 presented it as fixing a reachable production bug; round 2 confirmed the exact combination it guards against (`staff_id == user_id` AND `member_id` still NULL) cannot occur via any real migration or registration path today (every pre-migration row already had a real member_id from the OLD registration flow, and every post-migration registration requires a real `staff_id`) — the guard is kept as zero-cost defense-in-depth, not a live-bug fix, and both documents and the Step-1 test comment now say so explicitly. Task 3's guard-code sketch would not have compiled (`CorrectStaffID` has no `caller` variable — only `callerID`) and used the wrong comparison operator (`>` instead of the house style's `<=`, which also rejects equal rank) and the wrong exemplar (`RejectMember`'s category check instead of `UpdateMemberRole`/`UpdateMemberActive`'s real rank check) — all three fixed with the exact real code pattern, confirmed present in `profile.go`. Task 1's unique-violation-disambiguation sketch used `pgconn.PgError` without noting `profile.go` doesn't import `pgconn` yet, and left the `member_id` constraint name as an open question — both resolved (import needed, extend the existing `isUniqueViolation` helper with a constraint-name variant, and the constraint name is confirmed `profiles_member_id_key` from the actual migration text, an inline `unique` column constraint that Postgres auto-names that way).
