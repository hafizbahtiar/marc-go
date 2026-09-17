package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
)

func callRefresh(t *testing.T, h *AuthHandler, refresh string) (*httptest.ResponseRecorder, tokenPairResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/auth/refresh", h.Refresh)
	body, _ := json.Marshal(map[string]string{"refresh_token": refresh})
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var pair tokenPairResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &pair); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec, pair
}

// Aliran app dibuka semula lepas access luput: /me 401 -> refresh ->
// /me 200 dgn access baru -> refresh berikutnya pun lulus.
func TestAliranRefreshLepasAccessLuput(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")

	plain := uuid.NewString()
	family := uuid.New()
	if _, err := sqlc.New(pool).CreateRefreshToken(ctx, sqlc.CreateRefreshTokenParams{
		UserID:    me,
		TokenHash: auth.HashToken(plain),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		FamilyID:  family,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	expiredJWT := auth.NewJWT("ujian-rahsia", -time.Minute)
	oldAccess, _ := expiredJWT.GenerateAccessToken(me, family)

	jwtSvc := auth.NewJWT("ujian-rahsia", 15*time.Minute)
	h := NewAuthHandler(pool, jwtSvc, time.Hour, nil, "", "", "")

	if rec := callProtectedWithAccess(t, pool, jwtSvc, oldAccess); rec.Code != http.StatusUnauthorized {
		t.Fatalf("access luput: kod %d, mahu 401", rec.Code)
	}

	rec, pair := callRefresh(t, h, plain)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh 1: kod %d: %s", rec.Code, rec.Body.String())
	}
	if rec := callProtectedWithAccess(t, pool, jwtSvc, pair.AccessToken); rec.Code != http.StatusOK {
		t.Fatalf("/me lepas refresh: kod %d: %s", rec.Code, rec.Body.String())
	}

	rec, pair2 := callRefresh(t, h, pair.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh 2: kod %d: %s", rec.Code, rec.Body.String())
	}
	if rec := callProtectedWithAccess(t, pool, jwtSvc, pair2.AccessToken); rec.Code != http.StatusOK {
		t.Fatalf("/me lepas refresh 2: kod %d", rec.Code)
	}
}

// DB tak dapat dicapai (cth: Postgres baru bangun dari sleep) mesti
// 500, bukan 401 - mobile clear sesi pada 401 refresh.
func TestRefreshRalatDBJadi500Bukan401(t *testing.T) {
	pool := activityTestPool(t)
	dead, err := pgxpool.New(context.Background(), pool.Config().ConnString())
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	dead.Close()

	h := NewAuthHandler(dead, auth.NewJWT("ujian-rahsia", time.Minute), time.Hour, nil, "", "", "")
	rec, _ := callRefresh(t, h, uuid.NewString())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("kod = %d, mahu 500. Badan: %s", rec.Code, rec.Body.String())
	}
}

// Token yang dah dirotate diguna semula di luar grace -> 401 dan
// seluruh family direvoke (pengesanan curi token kekal berfungsi).
func TestRefreshReuseRevokeFamily(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")

	plain := uuid.NewString()
	family := uuid.New()
	if _, err := sqlc.New(pool).CreateRefreshToken(ctx, sqlc.CreateRefreshTokenParams{
		UserID:    me,
		TokenHash: auth.HashToken(plain),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		FamilyID:  family,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := NewAuthHandler(pool, auth.NewJWT("ujian-rahsia", time.Minute), time.Hour, nil, "", "", "")

	if rec, _ := callRefresh(t, h, plain); rec.Code != http.StatusOK {
		t.Fatalf("refresh 1: kod %d", rec.Code)
	}
	if _, err := pool.Exec(ctx, `update refresh_tokens set consumed_at = now() - interval '1 minute' where token_hash = $1`, auth.HashToken(plain)); err != nil {
		t.Fatalf("undur consumed_at: %v", err)
	}
	if rec, _ := callRefresh(t, h, plain); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reuse: kod %d, mahu 401", rec.Code)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from refresh_tokens where family_id = $1`, family).Scan(&n); err != nil {
		t.Fatalf("kira: %v", err)
	}
	if n != 0 {
		t.Fatalf("family masih ada %d baris, mahu direvoke", n)
	}
}
