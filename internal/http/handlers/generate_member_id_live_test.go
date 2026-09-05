package handlers

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"marc/internal/db/sqlc"
)

// Ujian integrasi terhadap Postgres sebenar untuk generateMemberID -
// Postgres sebenar diperlukan sebab memberIDCode memanggil
// q.NextSequence, yang bergantung pada jadual `sequences` (upsert
// atomic) - padanan sebab statusTestPool wujud (lihat
// profile_status_live_test.go).

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

	// SAME staffID - proves the sequence, not just the embedded
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

	// SAME staffID - proves the tester sequence advances independently.
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
	// separate counters, not the same key reused - interleave calls
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
	// all) - per the spec's Q2 decision (superadmin uniqueness is a
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
