package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
)

// Lapisan query reset kata laluan (L32). Diuji berasingan daripada
// handler supaya invarian skema (unik, cascade, sekali-guna melalui
// padam) dipegang oleh ujiannya sendiri.

func TestPasswordResetTokenPusinganPenuh(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	raw, err := auth.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}

	created, err := q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(raw),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	got, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw))
	if err != nil {
		t.Fatalf("GetPasswordResetTokenByHash: %v", err)
	}
	if got.ID != created.ID || got.UserID != userID {
		t.Fatalf("baris tak sepadan: got=%v created=%v", got.ID, created.ID)
	}

	// Token MENTAH tak boleh mencari apa-apa — hanya hash disimpan.
	if _, err := q.GetPasswordResetTokenByHash(ctx, raw); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token MENTAH memadankan baris — token disimpan tanpa hash")
	}

	if err := q.DeletePasswordResetTokensByUser(ctx, userID); err != nil {
		t.Fatalf("DeletePasswordResetTokensByUser: %v", err)
	}
	if _, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token masih wujud selepas dipadam")
	}
}

func TestUpdateUserPasswordMenukarHash(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	baharu, err := auth.HashPassword("kata-laluan-baharu")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
		ID: userID, PasswordHash: baharu,
	}); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	user, err := q.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if !auth.VerifyPassword(user.PasswordHash, "kata-laluan-baharu") {
		t.Fatal("kata laluan baharu tak disahkan selepas kemas kini")
	}
}

// `on delete cascade` — memadam user mesti membawa tokennya sekali,
// kalau tidak baris yatim menghalang pemadaman akaun.
func TestPasswordResetTokenCascadeBilaUserDipadam(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	raw, _ := auth.GenerateOpaqueToken()
	if _, err := q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(raw),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	if _, err := pool.Exec(ctx, `delete from profiles where user_id = $1`, userID); err != nil {
		t.Fatalf("padam profil: %v", err)
	}
	if _, err := pool.Exec(ctx, `delete from users where id = $1`, userID); err != nil {
		t.Fatalf("padam user (cascade token gagal?): %v", err)
	}

	if _, err := q.GetPasswordResetTokenByHash(ctx, auth.HashToken(raw)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("token bertahan selepas user dipadam — cascade tak berkuat kuasa")
	}
}
