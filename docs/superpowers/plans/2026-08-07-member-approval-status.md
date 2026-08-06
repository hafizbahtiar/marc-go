# Stage 11 Backend — Member Approval Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a management-approval gate to registration — new accounts start `pending` and are blocked from every endpoint except `/me` until a management user approves them, matching MAIWP-only access requirements.

**Architecture:** A new `status` column on `profiles` (`pending`/`approved`/`rejected`), a `RequireApprovedStatus` middleware mirroring the existing `RequireVerifiedEmail`, two new management-only endpoints (`approve`/`reject`), and notification fan-out reusing the existing Stage 10 `notifications` table (widened to allow member-status events, which have no associated post).

**Tech Stack:** Go 1.26, Gin, sqlc (pgx/v5), goose migrations, Postgres, Resend (email), existing `internal/authz`, `internal/http/middleware`, `internal/http/handlers` packages.

## Global Constraints

- All new user-facing error/success strings are in Malay, matching every existing handler/middleware message in this codebase.
- Every migration follows the existing single-file goose convention: `-- +goose Up` / `-- +goose Down`, filename `internal/db/migrations/<YYYYMMDDHHmmss>_<name>.sql`.
- Ownership pattern (Stage 3): handlers never trust a client-supplied "who am I" — always resolve the acting user from `middleware.UserID(c)` (JWT-derived). The *target* of approve/reject legitimately comes from the URL param (`:id`), but the *caller's* management check always uses `middleware.UserID(c)`.
- This codebase has **no `_test.go` files for HTTP handlers** — handlers are verified live against a real local Postgres via `curl`, not Go unit tests (see `TODO.md` Stage 10: "Verified end-to-end lawan Postgres sebenar"). This plan follows that established convention: each backend task is checked with `go build`/`go vet`, and full behavioral verification happens once, end-to-end, in the final task — not per-task curl calls against a half-built flow. Packages that already have `_test.go` files (`internal/email`, `internal/push`) are not touched by this plan, so no new Go test files are added.
- sqlc regeneration command: `sqlc generate` (run from repo root, requires the `sqlc` CLI on `PATH`).
- Local dev Postgres: `DATABASE_URL` in `.env` (already configured per README Quickstart). Migrations auto-apply on server start (`go run ./cmd/api`) — there is no separate manual `goose up` step in this codebase.

---

### Task 1: Migration — `profiles.status` / `approved_by` / `approved_at`

**Files:**
- Create: `internal/db/migrations/20260807120000_add_profile_status.sql`

**Interfaces:**
- Produces: `profiles.status text not null default 'pending'` (check constraint `'pending'|'approved'|'rejected'`), `profiles.approved_by uuid` (nullable, FK `users(id)`), `profiles.approved_at timestamptz` (nullable). All existing rows backfilled to `status = 'approved'` as part of this same migration.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
alter table profiles
  add column status text not null default 'pending'
    check (status in ('pending', 'approved', 'rejected')),
  add column approved_by uuid references users(id),
  add column approved_at timestamptz;

-- Backfill: everyone who registered BEFORE this migration is already a
-- known/active member — only rows created AFTER this point get the new
-- 'pending' default. approved_by/approved_at stay null for these (no
-- real approver to attribute retroactively).
update profiles set status = 'approved';

create index profiles_status_idx on profiles(status);

-- +goose Down
drop index if exists profiles_status_idx;
alter table profiles
  drop column if exists approved_at,
  drop column if exists approved_by,
  drop column if exists status;
```

- [ ] **Step 2: Apply against local dev Postgres and verify**

Run: `go run ./cmd/api` (goose auto-applies on startup, then Ctrl-C once it logs "listening on :8080")

Then verify column + backfill directly:

```bash
psql "$DATABASE_URL" -c "\d profiles" | grep -E "status|approved_by|approved_at"
psql "$DATABASE_URL" -c "select status, count(*) from profiles group by status;"
```

Expected: three new columns listed; `select status, count(*)` shows all existing rows as `approved` (no `pending`/`rejected` rows yet, since no new registrations have happened since the migration).

- [ ] **Step 3: Commit**

```bash
git add internal/db/migrations/20260807120000_add_profile_status.sql
git commit -m "Add profiles.status/approved_by/approved_at (Stage 11 migration)"
```

---

### Task 2: Migration — widen `notifications` for member-status events

**Problem this solves:** `notifications.post_id` is currently `not null` and `type` is constrained to `('post_like', 'post_comment')`. Member-approval notifications aren't about a post, so `post_id` must become nullable and the type list must grow.

**Files:**
- Create: `internal/db/migrations/20260807120100_widen_notifications_member_status.sql`
- Modify: `internal/http/handlers/posts_common.go:80-105` (`notifyOwner` — `postID` param type change)
- Modify: `internal/http/handlers/posts.go:310` (call site)
- Modify: `internal/http/handlers/comments.go:88-89` (call site)

**Interfaces:**
- Consumes: nothing new.
- Produces: `notifyOwner(ctx, q, pushSvc, recipientID, actorID uuid.UUID, notifType string, postID pgtype.UUID, commentID pgtype.UUID, title, message string)` — **`postID` changes from `uuid.UUID` to `pgtype.UUID`** (both callers below updated in this same task). `notifications.type` now accepts `'post_like' | 'post_comment' | 'member_pending' | 'member_approved' | 'member_rejected'`. `notifications.post_id` is nullable — `sqlc.CreateNotificationParams.PostID` becomes `pgtype.UUID` (was `uuid.UUID`) after regeneration in Task 3.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
alter table notifications alter column post_id drop not null;
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment', 'member_pending', 'member_approved', 'member_rejected'));

-- +goose Down
-- NOTE: this down migration will fail if any member_* notifications
-- exist by the time it runs (post_id null violates the restored NOT
-- NULL, and the old type check rejects member_* rows) — expected for a
-- dev rollback, not a concern for forward deploys.
alter table notifications drop constraint notifications_type_check;
alter table notifications add constraint notifications_type_check
  check (type in ('post_like', 'post_comment'));
alter table notifications alter column post_id set not null;
```

- [ ] **Step 2: Apply and verify the constraint name assumption**

Run: `go run ./cmd/api` (Ctrl-C once "listening" logs), then:

```bash
psql "$DATABASE_URL" -c "\d notifications"
```

Expected: `post_id` listed without `not null`; the `Check constraints:` section shows `notifications_type_check` with the 5 allowed values. If Postgres rejected the `drop constraint notifications_type_check` because the auto-generated name differs, the server log will show the migration error on startup — fix the constraint name in the migration to match what `\d notifications` showed *before* this migration ran (re-check via `psql` against a fresh `dropdb marc_test && createdb marc_test` + old migrations only, if needed), then re-run.

- [ ] **Step 3: Update `notifyOwner`'s `postID` parameter type**

In `internal/http/handlers/posts_common.go`, change the signature (around line 80):

```go
func notifyOwner(
	ctx context.Context,
	q *sqlc.Queries,
	pushSvc *push.Service,
	recipientID, actorID uuid.UUID,
	notifType string,
	postID pgtype.UUID,
	commentID pgtype.UUID,
	title, message string,
) {
```

(only the `postID` line changes: `postID uuid.UUID,` → `postID pgtype.UUID,`)

And the call inside it:

```go
	if _, err := q.CreateNotification(ctx, sqlc.CreateNotificationParams{
		RecipientID: recipientID,
		ActorID:     actorID,
		Type:        notifType,
		PostID:      postID,
		CommentID:   commentID,
	}); err != nil {
```

(unchanged — `postID` is now already the right type)

- [ ] **Step 4: Update the two call sites**

`internal/http/handlers/posts.go:310`, inside `Like`:

```go
		notifyOwner(ctx, h.queries, h.push, authorID, userID, "post_like", pgtype.UUID{Bytes: id, Valid: true}, pgtype.UUID{}, "Post anda disukai", "Seseorang menyukai post anda")
```

(`id` wrapped in `pgtype.UUID{Bytes: id, Valid: true}`)

`internal/http/handlers/comments.go:88-89`, inside `Create`:

```go
		notifyOwner(ctx, h.queries, h.push, postAuthorID, userID, "post_comment", pgtype.UUID{Bytes: postID, Valid: true},
			pgtype.UUID{Bytes: comment.ID, Valid: true}, "Comment baru", "Seseorang comment pada post anda")
```

(`postID` wrapped the same way)

- [ ] **Step 5: Build (this will fail until Task 3 regenerates sqlc — expected)**

Run: `go build ./...`
Expected: fails with a `PostID` type mismatch in `sqlc.CreateNotificationParams` (still `uuid.UUID` in generated code until Task 3 runs `sqlc generate`). This is expected — do not try to fix it here, Task 3 resolves it.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/20260807120100_widen_notifications_member_status.sql \
  internal/http/handlers/posts_common.go internal/http/handlers/posts.go internal/http/handlers/comments.go
git commit -m "Widen notifications for member-status events (nullable post_id, new types)"
```

---

### Task 3: sqlc queries for approval flow

**Files:**
- Modify: `queries/profiles.sql` (append new queries)
- Modify: `internal/db/sqlc/*.go` (regenerated, not hand-edited)

**Interfaces:**
- Produces:
  - `q.GetStatusByUserID(ctx, userID uuid.UUID) (string, error)`
  - `q.ListProfilesByStatus(ctx, status string) ([]ListProfilesByStatusRow, error)` — same shape as `ListProfilesRow` plus `Status`/`ApprovedBy`/`ApprovedAt` (all rows now have these via `p.*`)
  - `q.ApproveProfile(ctx, sqlc.ApproveProfileParams{UserID uuid.UUID, ApprovedBy pgtype.UUID}) (Profile, error)`
  - `q.RejectProfile(ctx, sqlc.RejectProfileParams{UserID uuid.UUID, ApprovedBy pgtype.UUID}) (Profile, error)`
  - `q.ListManagementUserIDs(ctx, category string) ([]uuid.UUID, error)`
  - `sqlc.Profile` struct gains `Status string`, `ApprovedBy pgtype.UUID`, `ApprovedAt pgtype.Timestamptz` fields (via the Task 1 migration).
  - `sqlc.GetProfileByUserIDRow` and `sqlc.ListProfilesRow` also gain those three fields (both already `select p.*, ...`).

- [ ] **Step 1: Append queries to `queries/profiles.sql`**

```sql

-- name: GetStatusByUserID :one
select status from profiles where user_id = $1;

-- name: ListProfilesByStatus :many
select
  p.*,
  r.key as role_key,
  r.name as role_name,
  r.category as role_category
from profiles p
join roles r on r.id = p.role_id
where p.status = $1
order by p.member_id;

-- name: ApproveProfile :one
update profiles
set status = 'approved', approved_by = $2, approved_at = now()
where user_id = $1
returning *;

-- name: RejectProfile :one
update profiles
set status = 'rejected', approved_by = $2, approved_at = now()
where user_id = $1
returning *;

-- name: ListManagementUserIDs :many
select p.user_id
from profiles p
join roles r on r.id = p.role_id
where r.category = $1;
```

- [ ] **Step 2: Regenerate**

Run: `sqlc generate`
Expected: no errors. `internal/db/sqlc/profiles.sql.go` gains the 5 new functions; `internal/db/sqlc/models.go`'s `Profile` struct gains `Status`, `ApprovedBy`, `ApprovedAt`.

- [ ] **Step 3: Build to confirm Task 2's call sites now compile**

Run: `go build ./... && go vet ./...`
Expected: clean (the `PostID` mismatch from Task 2 Step 5 is now resolved, since `CreateNotificationParams.PostID` is `pgtype.UUID`).

- [ ] **Step 4: Commit**

```bash
git add queries/profiles.sql internal/db/sqlc/
git commit -m "Add sqlc queries for member approval status"
```

---

### Task 4: `RequireApprovedStatus` middleware

**Files:**
- Modify: `internal/http/middleware/verified.go` (append; same file `RequireVerifiedEmail` lives in — same pattern, same file)

**Interfaces:**
- Consumes: `sqlc.Queries.GetStatusByUserID` (Task 3), `UserID(c)` (existing helper, same package).
- Produces: `middleware.RequireApprovedStatus(q *sqlc.Queries) gin.HandlerFunc` — must be installed after `RequireAuth`, same as `RequireVerifiedEmail`.

- [ ] **Step 1: Add the middleware**

Append to `internal/http/middleware/verified.go`:

```go

// RequireApprovedStatus mesti dipasang selepas RequireAuth. Gate akses
// sehingga profiles.status = 'approved' (Stage 11) — app khusus
// kakitangan MAIWP, pendaftaran baru kena diluluskan management dulu.
//
// GET/PATCH /me SENGAJA tak diletak di bawah middleware ni — user
// pending/rejected tetap perlu boleh papar status semasa dia sendiri
// (padanan design: "boleh login, tapi semua endpoint lain block").
func RequireApprovedStatus(q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		status, err := q.GetStatusByUserID(c.Request.Context(), UserID(c))
		if err != nil || status != "approved" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "akaun anda belum diluluskan pihak pengurusan"})
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 2: Build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/http/middleware/verified.go
git commit -m "Add RequireApprovedStatus middleware"
```

---

### Task 5: Router wiring

**Files:**
- Modify: `internal/http/router.go:73-90`

**Interfaces:**
- Consumes: `middleware.RequireApprovedStatus` (Task 4), `profileHandler.ApproveMember`/`RejectMember` (Task 6 — this task wires the routes; Task 6 implements the handlers referenced here, so `go build` for this task alone will fail until Task 6 lands — expected, same pattern as Task 2/3).

- [ ] **Step 1: Split `protectedAuthGroup` and gate `/verify-email/request`**

In `internal/http/router.go`, replace:

```go
	protectedAuthGroup := r.Group("/auth", middleware.RequireAuth(jwtSvc))
	protectedAuthGroup.POST("/verify-email/request", authHandler.RequestEmailVerification)
```

with:

```go
	protectedAuthGroup := r.Group("/auth", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)))
	protectedAuthGroup.POST("/verify-email/request", authHandler.RequestEmailVerification)
```

- [ ] **Step 2: Split `protected` into `protected` (auth-only, `/me`) and a new `approved` group**

Replace:

```go
	protected := r.Group("/", middleware.RequireAuth(jwtSvc))
	protected.GET("/me", profileHandler.Me)
	protected.PATCH("/me", profileHandler.UpdateMe)
	protected.GET("/members", profileHandler.Members)
	protected.POST("/device-tokens", deviceTokenHandler.Upsert)
	protected.DELETE("/device-tokens/:id", deviceTokenHandler.Delete)
	protected.DELETE("/device-tokens/by-onesignal/:onesignalId", deviceTokenHandler.DeleteByOnesignalID)
```

with:

```go
	protected := r.Group("/", middleware.RequireAuth(jwtSvc))
	protected.GET("/me", profileHandler.Me)
	protected.PATCH("/me", profileHandler.UpdateMe)

	// approved (Stage 11) — /members, /device-tokens, dan approve/reject
	// sendiri perlu status=approved. /me sengaja TAK di sini (lihat
	// RequireApprovedStatus).
	approved := r.Group("/", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)))
	approved.GET("/members", profileHandler.Members)
	approved.POST("/members/:id/approve", profileHandler.ApproveMember)
	approved.POST("/members/:id/reject", profileHandler.RejectMember)
	approved.POST("/device-tokens", deviceTokenHandler.Upsert)
	approved.DELETE("/device-tokens/:id", deviceTokenHandler.Delete)
	approved.DELETE("/device-tokens/by-onesignal/:onesignalId", deviceTokenHandler.DeleteByOnesignalID)
```

- [ ] **Step 3: Add `RequireApprovedStatus` to the Posts `verified` group (defense in depth)**

Replace:

```go
	verified := r.Group("/", middleware.RequireAuth(jwtSvc), middleware.RequireVerifiedEmail(sqlc.New(pool)))
```

with:

```go
	verified := r.Group("/", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)), middleware.RequireVerifiedEmail(sqlc.New(pool)))
```

- [ ] **Step 4: Update `NewProfileHandler` call site**

Replace:

```go
	profileHandler := handlers.NewProfileHandler(pool)
```

with:

```go
	profileHandler := handlers.NewProfileHandler(pool, emailClient)
```

(`emailClient` is already an `internal/email.Client` parameter of `NewRouter` — no signature change needed there.)

- [ ] **Step 5: Build (expected to fail until Task 6)**

Run: `go build ./...`
Expected: fails — `handlers.NewProfileHandler` still takes one arg, and `ApproveMember`/`RejectMember` don't exist yet. This is expected; Task 6 resolves it.

- [ ] **Step 6: Commit**

```bash
git add internal/http/router.go
git commit -m "Wire RequireApprovedStatus into router (Stage 11)"
```

---

### Task 6: `ProfileHandler` — status fields, `/members` filter, approve/reject

**Files:**
- Modify: `internal/http/handlers/profile.go` (entire file restructured below)

**Interfaces:**
- Consumes: `sqlc.ApproveProfile`, `sqlc.RejectProfile`, `sqlc.ListProfilesByStatus`, `sqlc.GetProfileByUserID` (Task 3 + existing), `authz.IsManagement` (existing), `email.Client.Send` (existing, `internal/email`), `nullableUUIDString`/`formatTimeNullable` (existing helpers in `posts_common.go`, same package `handlers`).
- Produces: `handlers.NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client) *ProfileHandler` (signature change — consumed by Task 5's router wiring), `ProfileHandler.ApproveMember(c *gin.Context)`, `ProfileHandler.RejectMember(c *gin.Context)` (consumed by Task 5's router wiring), `profileResponse.Status string` (new field, for Flutter's pending-screen check), `memberResponse.UserID string` and `memberResponse.Status string` (new fields, needed so management can target `approve`/`reject` by id and see who's pending).

- [ ] **Step 1: Rewrite `internal/http/handlers/profile.go`**

```go
package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
)

type ProfileHandler struct {
	queries     *sqlc.Queries
	emailClient *email.Client
}

func NewProfileHandler(pool *pgxpool.Pool, emailClient *email.Client) *ProfileHandler {
	return &ProfileHandler{queries: sqlc.New(pool), emailClient: emailClient}
}

type profileResponse struct {
	MemberID      string  `json:"member_id"`
	Email         string  `json:"email"`
	EmailVerified bool    `json:"email_verified"`
	Status        string  `json:"status"`
	DisplayName   *string `json:"display_name"`
	Phone         *string `json:"phone"`
	RoleKey       string  `json:"role_key"`
	RoleName      string  `json:"role_name"`
	Category      string  `json:"category"`
}

// Me setara `myProfileProvider` di Flutter — profil user semasa. Sengaja
// TIDAK di bawah RequireApprovedStatus (Stage 11): user pending/rejected
// kena boleh baca status dia sendiri supaya app boleh papar skrin yang
// betul.
func (h *ProfileHandler) Me(c *gin.Context) {
	row, err := h.queries.GetProfileByUserID(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
		return
	}

	c.JSON(http.StatusOK, profileResponse{
		MemberID:      row.MemberID,
		Email:         row.Email,
		EmailVerified: row.EmailVerified,
		Status:        row.Status,
		DisplayName:   textToPtr(row.DisplayName),
		Phone:         textToPtr(row.Phone),
		RoleKey:       row.RoleKey,
		RoleName:      row.RoleName,
		Category:      row.RoleCategory,
	})
}

type updateMeRequest struct {
	DisplayName string `json:"display_name"`
	Phone       string `json:"phone"`
}

// UpdateMe setara `ProfileRepository.update` di Flutter — string kosong
// (lepas trim) disimpan sebagai NULL. Sengaja TIDAK di bawah
// RequireApprovedStatus (Stage 11) — sama sebab macam Me.
func (h *ProfileHandler) UpdateMe(c *gin.Context) {
	var req updateMeRequest
	if !bindJSON(c, &req) {
		return
	}

	updated, err := h.queries.UpdateProfile(c.Request.Context(), sqlc.UpdateProfileParams{
		UserID:      middleware.UserID(c),
		DisplayName: ptrToText(req.DisplayName),
		Phone:       ptrToText(req.Phone),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"member_id":    updated.MemberID,
		"display_name": textToPtr(updated.DisplayName),
		"phone":        textToPtr(updated.Phone),
	})
}

type memberResponse struct {
	UserID      string  `json:"user_id"`
	MemberID    string  `json:"member_id"`
	DisplayName *string `json:"display_name"`
	RoleName    string  `json:"role_name"`
	Category    string  `json:"category"`
	Status      string  `json:"status"`
}

// Members setara `membersProvider` di Flutter — gantian RLS
// `select_all_profiles_management`: ahli biasa nampak diri sendiri
// sahaja, management nampak semua. Management boleh tapis
// `?status=pending` untuk senarai ahli menunggu kelulusan (Stage 11).
func (h *ProfileHandler) Members(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	isManagement, err := authz.IsManagement(ctx, h.queries, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	if !isManagement {
		row, err := h.queries.GetProfileByUserID(ctx, userID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profil tidak dijumpai"})
			return
		}
		c.JSON(http.StatusOK, []memberResponse{{
			UserID:      row.UserID.String(),
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
			Status:      row.Status,
		}})
		return
	}

	if statusFilter := c.Query("status"); statusFilter != "" {
		rows, err := h.queries.ListProfilesByStatus(ctx, statusFilter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
			return
		}
		members := make([]memberResponse, len(rows))
		for i, row := range rows {
			members[i] = memberResponse{
				UserID:      row.UserID.String(),
				MemberID:    row.MemberID,
				DisplayName: textToPtr(row.DisplayName),
				RoleName:    row.RoleName,
				Category:    row.RoleCategory,
				Status:      row.Status,
			}
		}
		c.JSON(http.StatusOK, members)
		return
	}

	rows, err := h.queries.ListProfiles(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai ahli"})
		return
	}

	members := make([]memberResponse, len(rows))
	for i, row := range rows {
		members[i] = memberResponse{
			UserID:      row.UserID.String(),
			MemberID:    row.MemberID,
			DisplayName: textToPtr(row.DisplayName),
			RoleName:    row.RoleName,
			Category:    row.RoleCategory,
			Status:      row.Status,
		}
	}
	c.JSON(http.StatusOK, members)
}

type memberActionResponse struct {
	UserID     string  `json:"user_id"`
	Status     string  `json:"status"`
	ApprovedBy *string `json:"approved_by"`
	ApprovedAt *string `json:"approved_at"`
}

// ApproveMember (Stage 11) — management sahaja. Set status='approved',
// hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) ApproveMember(c *gin.Context) {
	h.setMemberStatus(c, "approved")
}

// RejectMember (Stage 11) — management sahaja. Set status='rejected'
// (row KEKAL, bukan padam — audit trail + boleh undo via ApproveMember
// lain kali). Hantar email + in-app notification kepada ahli berkenaan.
func (h *ProfileHandler) RejectMember(c *gin.Context) {
	h.setMemberStatus(c, "rejected")
}

func (h *ProfileHandler) setMemberStatus(c *gin.Context, status string) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	callerID := middleware.UserID(c)

	isManagement, err := authz.IsManagement(ctx, h.queries, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status ahli"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh luluskan/tolak ahli"})
		return
	}

	approvedBy := pgtype.UUID{Bytes: callerID, Valid: true}

	var updated sqlc.Profile
	if status == "approved" {
		updated, err = h.queries.ApproveProfile(ctx, sqlc.ApproveProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	} else {
		updated, err = h.queries.RejectProfile(ctx, sqlc.RejectProfileParams{UserID: targetID, ApprovedBy: approvedBy})
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ahli tidak dijumpai"})
		return
	}

	notifType := "member_approved"
	subject, html := "Pendaftaran MARC Diluluskan",
		"<p>Pendaftaran anda telah diluluskan oleh pihak pengurusan. Log masuk semula dan sahkan email anda untuk mula guna app MARC.</p>"
	if status == "rejected" {
		notifType = "member_rejected"
		subject, html = "Pendaftaran MARC Ditolak",
			"<p>Pendaftaran anda ke app MARC tidak diluluskan pada masa ini. Jika ini satu kesilapan, sila hubungi pihak pengurusan MAIWP.</p>"
	}

	if target, err := h.queries.GetProfileByUserID(ctx, targetID); err == nil {
		if err := h.emailClient.Send(ctx, target.Email, subject, html); err != nil {
			log.Printf("gagal hantar email status ahli: %v", err)
		}
	}

	if _, err := h.queries.CreateNotification(ctx, sqlc.CreateNotificationParams{
		RecipientID: targetID,
		ActorID:     callerID,
		Type:        notifType,
		PostID:      pgtype.UUID{},
		CommentID:   pgtype.UUID{},
	}); err != nil {
		log.Printf("gagal cipta notification status ahli: %v", err)
	}

	c.JSON(http.StatusOK, memberActionResponse{
		UserID:     updated.UserID.String(),
		Status:     updated.Status,
		ApprovedBy: nullableUUIDString(updated.ApprovedBy),
		ApprovedAt: formatTimeNullable(updated.ApprovedAt),
	})
}

func textToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func ptrToText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
```

- [ ] **Step 2: Build**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: clean, no output from `gofmt -l .`.

- [ ] **Step 3: Commit**

```bash
git add internal/http/handlers/profile.go
git commit -m "Add ApproveMember/RejectMember, status fields (Stage 11)"
```

---

### Task 7: Notify management when a new member registers

**Files:**
- Modify: `internal/http/handlers/auth.go:1-21` (imports), `:153-160` (`Register`, append call)

**Interfaces:**
- Consumes: `sqlc.ListManagementUserIDs` (Task 3), `authz.CategoryManagement` (existing constant), `sqlc.CreateNotification` (existing).
- Produces: `notifyManagementOfPendingMember(ctx context.Context, q *sqlc.Queries, newUserID uuid.UUID)` — private helper in `auth.go`, not consumed elsewhere.

- [ ] **Step 1: Add the import**

In `internal/http/handlers/auth.go`, add `"marc/internal/authz"` to the import block (alongside the existing `marc/internal/auth`, `marc/internal/db/sqlc`, etc.):

```go
	"marc/internal/auth"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
```

- [ ] **Step 2: Call the helper from `Register`**

In `Register`, right before `c.JSON(http.StatusCreated, tokens)` (currently the last two lines of the function):

```go
	tokens, err := h.issueTokens(c, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pendaftaran berjaya tapi gagal log masuk, sila log masuk semula"})
		return
	}

	notifyManagementOfPendingMember(ctx, h.queries, user.ID)

	c.JSON(http.StatusCreated, tokens)
```

- [ ] **Step 3: Add the helper function**

Append to `internal/http/handlers/auth.go` (after `Register`, before `generateMemberID` is a reasonable spot):

```go
// notifyManagementOfPendingMember fan-out notification "ahli baru
// menunggu kelulusan" kepada semua management (Stage 11). Best-effort —
// kegagalan notification tak patut gagalkan pendaftaran yang dah
// berjaya (padanan pattern notifyOwner, Stage 10).
func notifyManagementOfPendingMember(ctx context.Context, q *sqlc.Queries, newUserID uuid.UUID) {
	managementIDs, err := q.ListManagementUserIDs(ctx, authz.CategoryManagement)
	if err != nil {
		log.Printf("gagal senarai management untuk notify ahli pending: %v", err)
		return
	}
	for _, recipientID := range managementIDs {
		if _, err := q.CreateNotification(ctx, sqlc.CreateNotificationParams{
			RecipientID: recipientID,
			ActorID:     newUserID,
			Type:        "member_pending",
			PostID:      pgtype.UUID{},
			CommentID:   pgtype.UUID{},
		}); err != nil {
			log.Printf("gagal cipta notification member_pending: %v", err)
		}
	}
}
```

- [ ] **Step 4: Build**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/http/handlers/auth.go
git commit -m "Notify management on new pending registration (Stage 11)"
```

---

### Task 8: End-to-end live verification + full sweep + docs

**Files:**
- Modify: `TODO.md` (mark Stage 11 backend done, record verification results — same convention as Stage 10)
- Modify: `DATABASE.md:178` (update the `profiles` schema summary row to mention `status`/`approved_by`/`approved_at`)
- No code files modified in this task — verification + documentation only.

- [ ] **Step 1: Fresh local test DB**

```bash
dropdb marc_test 2>/dev/null; createdb marc_test
DATABASE_URL="postgres://$(whoami)@localhost:5432/marc_test?sslmode=disable" \
  JWT_SECRET="test-secret-at-least-32-bytes-long-xxxx" \
  PORT=8081 go run ./cmd/api &
sleep 2
```

- [ ] **Step 2: Register two accounts (one to stay pending, one to approve), verify both start pending and blocked**

```bash
BASE=http://localhost:8081

curl -sS -w "\nHTTP:%{http_code}\n" -X POST $BASE/auth/register -H "Content-Type: application/json" \
  -d '{"email":"pending-test@example.com","password":"secret123"}'
curl -sS -w "\nHTTP:%{http_code}\n" -X POST $BASE/auth/register -H "Content-Type: application/json" \
  -d '{"email":"approve-test@example.com","password":"secret123"}'

TOKEN_PENDING=$(curl -sS -X POST $BASE/auth/login -H "Content-Type: application/json" \
  -d '{"email":"pending-test@example.com","password":"secret123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['access_token'])")
TOKEN_APPROVE=$(curl -sS -X POST $BASE/auth/login -H "Content-Type: application/json" \
  -d '{"email":"approve-test@example.com","password":"secret123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['access_token'])")

echo "=== GET /me while pending (expect 200, status=pending) ==="
curl -sS -w "\nHTTP:%{http_code}\n" $BASE/me -H "Authorization: Bearer $TOKEN_PENDING"

echo "=== GET /members while pending (expect 403) ==="
curl -sS -w "\nHTTP:%{http_code}\n" $BASE/members -H "Authorization: Bearer $TOKEN_PENDING"

echo "=== POST /auth/verify-email/request while pending (expect 403) ==="
curl -sS -w "\nHTTP:%{http_code}\n" -X POST $BASE/auth/verify-email/request -H "Authorization: Bearer $TOKEN_PENDING"
```

Expected: register both → 201; login both → tokens; `/me` → 200 with `"status":"pending"`; `/members` → 403 `"akaun anda belum diluluskan pihak pengurusan"`; `/auth/verify-email/request` → 403 same message.

- [ ] **Step 3: Approve one, reject the other, using a management account**

This requires an existing management account. Use the same one from prior stages' testing (see `TODO.md` Stage 3/10 for how a management test user was seeded), or seed one directly:

```bash
psql "$DATABASE_URL" -c "
  update profiles set role_id = (select id from roles where category = 'management' limit 1)
  where user_id = (select id from users where email = 'approve-test@example.com');
"
```

Wait — do NOT approve-test's own role for this; seed a THIRD account as management instead:

```bash
curl -sS -X POST $BASE/auth/register -H "Content-Type: application/json" \
  -d '{"email":"mgmt-test@example.com","password":"secret123"}'
psql "$DATABASE_URL" -c "
  update profiles set role_id = (select id from roles where category = 'management' limit 1), status = 'approved'
  where user_id = (select id from users where email = 'mgmt-test@example.com');
"
TOKEN_MGMT=$(curl -sS -X POST $BASE/auth/login -H "Content-Type: application/json" \
  -d '{"email":"mgmt-test@example.com","password":"secret123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['access_token'])")

echo "=== GET /members?status=pending as management (expect 2: pending-test + approve-test) ==="
curl -sS -w "\nHTTP:%{http_code}\n" "$BASE/members?status=pending" -H "Authorization: Bearer $TOKEN_MGMT"

UID_APPROVE=$(psql "$DATABASE_URL" -tAc "select id from users where email = 'approve-test@example.com'")
UID_PENDING=$(psql "$DATABASE_URL" -tAc "select id from users where email = 'pending-test@example.com'")

echo "=== POST /members/:id/approve (expect 200, status=approved) ==="
curl -sS -w "\nHTTP:%{http_code}\n" -X POST "$BASE/members/$UID_APPROVE/approve" -H "Authorization: Bearer $TOKEN_MGMT"

echo "=== POST /members/:id/reject (expect 200, status=rejected) ==="
curl -sS -w "\nHTTP:%{http_code}\n" -X POST "$BASE/members/$UID_PENDING/reject" -H "Authorization: Bearer $TOKEN_MGMT"

echo "=== ahli biasa cuba approve (expect 403) ==="
curl -sS -w "\nHTTP:%{http_code}\n" -X POST "$BASE/members/$UID_APPROVE/approve" -H "Authorization: Bearer $TOKEN_APPROVE"
```

Expected: pending list shows 2 entries; approve → 200 with `"status":"approved"`; reject → 200 with `"status":"rejected"`; non-management approve attempt → 403.

- [ ] **Step 4: Confirm the approved account now has full access, the rejected one still doesn't**

```bash
echo "=== approved account: GET /members (expect 200 now) ==="
curl -sS -w "\nHTTP:%{http_code}\n" $BASE/members -H "Authorization: Bearer $TOKEN_APPROVE"

echo "=== approved account: verify-email/request (expect 200/204, not 403) ==="
curl -sS -w "\nHTTP:%{http_code}\n" -X POST $BASE/auth/verify-email/request -H "Authorization: Bearer $TOKEN_APPROVE"

echo "=== rejected account: GET /me (expect 200, status=rejected) ==="
curl -sS -w "\nHTTP:%{http_code}\n" $BASE/me -H "Authorization: Bearer $TOKEN_PENDING"

echo "=== rejected account: GET /members (expect still 403) ==="
curl -sS -w "\nHTTP:%{http_code}\n" $BASE/members -H "Authorization: Bearer $TOKEN_PENDING"
```

Expected: approved account unlocked (`/members` 200, verify-email/request no longer 403); rejected account's `/me` shows `"status":"rejected"`, `/members` still 403.

- [ ] **Step 5: Confirm in-app notifications were created**

```bash
psql "$DATABASE_URL" -c "select type, recipient_id, actor_id from notifications where type like 'member_%' order by created_at;"
```

Expected: 3 `member_pending` rows (one per registration, recipient = the seeded management user — note `mgmt-test` itself won't have a `member_pending` row for its own registration since it registered before being promoted, but `pending-test`/`approve-test`/`mgmt-test`'s own registrations each fan out to whoever was management *at insert time* — if zero management existed yet when the first two registered, expect 0 rows for those two and only later ones to have recipients; call this out explicitly in the TODO.md write-up rather than treating a mismatch as a bug), plus 1 `member_approved` row (recipient = approve-test's user id) and 1 `member_rejected` row (recipient = pending-test's user id).

- [ ] **Step 6: Kill test server, drop test DB**

```bash
lsof -ti:8081 | xargs -r kill
dropdb marc_test
```

- [ ] **Step 7: Full repo sweep**

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...
golangci-lint run ./...
```

Expected: all clean (no `gofmt -l` output, no vet/build/test/lint errors).

- [ ] **Step 8: Update `DATABASE.md`**

In `DATABASE.md`, line 178, change:

```
| `profiles` | member_id (`MARC{YYYY}/{MM}/{0000}`), display_name, phone, role_id, email_verified |
```

to:

```
| `profiles` | member_id (`MARC{YYYY}/{MM}/{0000}`), display_name, phone, role_id, email_verified, status (`pending`/`approved`/`rejected`, Stage 11), approved_by, approved_at |
```

- [ ] **Step 9: Update `TODO.md`**

Move the Stage 11 backend section from "belum design" to done, following the exact style of the Stage 10 write-up already in `TODO.md` (list what's implemented with `[x]`, then a "Verified end-to-end lawan Postgres sebenar" bullet list summarizing Step 2–5's actual results). Also remove the "Stage 11 (status approval pendaftaran) — soalan design belum dijawab" line from "Belum putus / perlu bincang lagi" (the questions are now answered — spec is at `docs/superpowers/specs/2026-08-07-member-approval-status-design.md`). Note in the write-up that frontend Stage 11 (`marc_flutter/TODO.md`) is still pending, unblocked by this.

- [ ] **Step 10: Commit**

```bash
git add DATABASE.md TODO.md
git commit -m "Stage 11 backend: member approval status, verified end-to-end"
```

---

## Self-Review Notes

- **Spec coverage:** data model (Task 1), gate order/flow (Tasks 4–6, verified Task 8), approver role reusing `IsManagement` (Task 6), reject-keeps-data (Task 6, `RejectProfile` never deletes), backfill-existing-to-approved (Task 1), email+in-app notifications both directions (Tasks 6–7), `PATCH /me` exempt (Task 5/6) — all covered.
- **Known rough edge, called out rather than hidden:** Task 8 Step 5's notification count depends on how many management accounts existed *at each registration's time* — the verification step explains this rather than asserting a fixed count that could spuriously "fail."
- **Out of scope confirmed:** no `admin` role, no `suspended` status, no bulk-approve, no Flutter changes (tracked separately in `marc_flutter/TODO.md` Stage 11, already written) — matches the spec's "Out of scope" section exactly.
