package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
)

// Query-level tests for VerifyStaffID / CorrectStaffID (Task 3 of the
// staff-id verification plan). Real Postgres required - see
// statusTestPool in profile_status_live_test.go for why (this exercises
// the actual unique/not-null constraints added by
// 20260902100000_add_staff_id.sql, which a mock can't reproduce).
//
// This file deliberately does NOT reuse the shared seedMember() helper
// from profile_status_live_test.go: seedMember's insert predates the
// staff_id NOT NULL constraint and does not supply a value for it, so
// it fails with a not-null violation once that migration is applied
// (confirmed while writing this file: TestApproveMemberDiaudit fails
// the same way against a freshly migrated DB). Fixing seedMember is
// out of scope for Task 3 - it's Task 4/5 territory (register/approve
// flows that decide how staff_id gets populated at seed time) - so
// these tests seed their own rows with staff_id set explicitly instead.

// createTestPendingProfile seeds a profile with member_id NULL and
// staff_id_verified_at NULL - the starting state VerifyStaffID expects.
func createTestPendingProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	email := "staffid-pending-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = 'ahli'), 'pending')`,
		userID, "EMP-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed pending profile: %v", err)
	}
	return userID
}

// createTestApprovedProfile seeds a profile that is already approved
// and staff-id-verified, with member_id already assigned - matching
// what the backfill migration/normal approval flow produces.
func createTestApprovedProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleKey string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	email := "staffid-approved-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, staff_id, staff_id_verified_at, role_id, status)
		 values ($1, $2, $3, now(), (select id from roles where key = $4), 'approved')`,
		userID, "MARC/"+uuid.NewString()[:8], "EMP-"+uuid.NewString()[:8], roleKey); err != nil {
		t.Fatalf("seed approved profile: %v", err)
	}
	return userID
}

func verifiedByOf(userID uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: userID, Valid: true}
}

func TestVerifyStaffID_FirstCallSucceeds(t *testing.T) {
	pool, ctx := statusTestPool(t)
	q := sqlc.New(pool)

	target := createTestPendingProfile(t, ctx, pool)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	// member_id is unique across profiles - a hardcoded literal (as in
	// the plan's illustrative example) would collide across test runs
	// against a persistent, non-wiped test DB, so mint a fresh one.
	memberID := "MARC2026/09/" + uuid.NewString()[:8]

	got, err := q.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
		UserID:     target,
		VerifiedBy: verifiedByOf(manager),
		MemberID:   pgtype.Text{String: memberID, Valid: true},
	})
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if !got.StaffIDVerifiedAt.Valid {
		t.Fatal("expected staff_id_verified_at set")
	}
	if got.MemberID.String != memberID {
		t.Fatalf("expected member_id assigned, got %v", got.MemberID)
	}
}

func TestVerifyStaffID_SecondCallReturnsNoRows(t *testing.T) {
	pool, ctx := statusTestPool(t)
	q := sqlc.New(pool)

	target := createTestPendingProfile(t, ctx, pool)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")

	_, err := q.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
		UserID:     target,
		VerifiedBy: verifiedByOf(manager),
		MemberID:   pgtype.Text{String: "MARC2026/09/" + uuid.NewString()[:8], Valid: true},
	})
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}

	_, err = q.VerifyStaffID(ctx, sqlc.VerifyStaffIDParams{
		UserID:     target,
		VerifiedBy: verifiedByOf(manager),
		MemberID:   pgtype.Text{String: "MARC2026/09/" + uuid.NewString()[:8], Valid: true},
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows on second verify, got %v", err)
	}
}

func TestCorrectStaffID_UpdatesEvenWhenVerified(t *testing.T) {
	pool, ctx := statusTestPool(t)
	q := sqlc.New(pool)

	target := createTestApprovedProfile(t, ctx, pool, "ahli") // already verified per backfill/approval

	// staff_id unik merentas profiles - literal tetap akan berlanggar bila
	// ujian dijalankan semula atas DB ujian yang tak dibuang (padanan
	// seeder lain dalam fail ni).
	corrected := "EMP-" + uuid.NewString()[:8]

	got, err := q.CorrectStaffID(ctx, sqlc.CorrectStaffIDParams{UserID: target, StaffID: corrected})
	if err != nil {
		t.Fatal(err)
	}
	if got.StaffID != corrected {
		t.Fatalf("expected corrected staff_id, got %q", got.StaffID)
	}
	if !got.StaffIDVerifiedAt.Valid {
		t.Fatal("correcting staff_id must NOT clear verified_at")
	}
}
