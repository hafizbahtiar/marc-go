package handlers

import (
	"context"
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

// sessionsAuthHandler - AuthHandler minimum utk ujian sesi. emailClient
// nil selamat di sini: ListMySessions/RevokeMySession tak hantar emel.
func sessionsAuthHandler(pool *pgxpool.Pool) *AuthHandler {
	return NewAuthHandler(pool, auth.NewJWT("ujian-rahsia", time.Minute), time.Hour, nil, "", "", "")
}

// seedRefreshToken cipta satu baris refresh_tokens milik userID. ttl
// negatif = token luput (untuk sahkan tapisan expires_at).
func seedRefreshToken(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, userAgent, ip string, ttl time.Duration) uuid.UUID {
	t.Helper()
	row, err := sqlc.New(pool).CreateRefreshToken(context.Background(), sqlc.CreateRefreshTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(uuid.NewString()),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
		FamilyID:  uuid.New(),
		UserAgent: ptrToText(userAgent),
		CreatedIp: ptrToText(ip),
	})
	if err != nil {
		t.Fatalf("seed refresh token: %v", err)
	}
	return row.ID
}

func callListSessions(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/me/sessions", nil)
	c.Set("userID", userID)

	sessionsAuthHandler(pool).ListMySessions(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func callRevokeSession(t *testing.T, pool *pgxpool.Pool, userID, sessionID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/me/sessions/"+sessionID.String(), nil)
	c.Params = gin.Params{{Key: "id", Value: sessionID.String()}}
	c.Set("userID", userID)

	sessionsAuthHandler(pool).RevokeMySession(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func refreshTokenExists(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from refresh_tokens where id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("kira refresh token: %v", err)
	}
	return n > 0
}

func decodeSessions(t *testing.T, rec *httptest.ResponseRecorder) []sessionResponse {
	t.Helper()
	var body []sessionResponse
	if err := decodeJSON(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestListMySessionsHanyaMilikPemanggil(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")
	orangLain := seedMember(t, ctx, pool, "ahli", "approved")

	mine := seedRefreshToken(t, pool, me, "iPhone Safari", "203.0.113.7", time.Hour)
	theirs := seedRefreshToken(t, pool, orangLain, "Android Chrome", "198.51.100.9", time.Hour)

	rec := callListSessions(t, pool, me)
	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}

	body := decodeSessions(t, rec)
	if len(body) != 1 {
		t.Fatalf("sesi = %d, mahu 1 (hanya milik pemanggil). Badan: %s", len(body), rec.Body.String())
	}
	if body[0].ID != mine.String() {
		t.Fatalf("id = %s, mahu %s", body[0].ID, mine)
	}
	if body[0].UserAgent == nil || *body[0].UserAgent != "iPhone Safari" {
		t.Fatalf("user_agent = %v, mahu 'iPhone Safari'", body[0].UserAgent)
	}
	if body[0].CreatedIP == nil || *body[0].CreatedIP != "203.0.113.7" {
		t.Fatalf("created_ip = %v, mahu '203.0.113.7'", body[0].CreatedIP)
	}
	for _, s := range body {
		if s.ID == theirs.String() {
			t.Fatal("sesi ahli lain bocor dalam senarai")
		}
	}
}

// Baris yang dah luput bukan lagi "sesi aktif" - tak patut ditunjuk
// walaupun ia masih wujud dalam jadual sehingga kerja pembersihan jalan.
func TestListMySessionsAbaikanYangLuput(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")

	hidup := seedRefreshToken(t, pool, me, "Hidup", "203.0.113.1", time.Hour)
	seedRefreshToken(t, pool, me, "Luput", "203.0.113.2", -time.Hour)

	body := decodeSessions(t, callListSessions(t, pool, me))
	if len(body) != 1 || body[0].ID != hidup.String() {
		t.Fatalf("mahu hanya sesi hidup (%s), dapat %+v", hidup, body)
	}
}

// Metadata nullable: baris lama (sebelum migration user_agent/created_ip)
// mesti keluar sebagai null, bukan meletupkan handler.
func TestListMySessionsMetadataKosongJadiNull(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")
	seedRefreshToken(t, pool, me, "", "", time.Hour)

	body := decodeSessions(t, callListSessions(t, pool, me))
	if len(body) != 1 {
		t.Fatalf("sesi = %d, mahu 1", len(body))
	}
	if body[0].UserAgent != nil || body[0].CreatedIP != nil {
		t.Fatalf("metadata kosong patut null, dapat %+v", body[0])
	}
}

func TestRevokeMySessionPadamBarisBetul(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")

	sasaran := seedRefreshToken(t, pool, me, "Device A", "203.0.113.1", time.Hour)
	kekal := seedRefreshToken(t, pool, me, "Device B", "203.0.113.2", time.Hour)

	rec := callRevokeSession(t, pool, me, sasaran)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d, mahu 204. Badan: %s", rec.Code, rec.Body.String())
	}
	if refreshTokenExists(t, pool, sasaran) {
		t.Fatal("sesi sasaran patut dipadam")
	}
	if !refreshTokenExists(t, pool, kekal) {
		t.Fatal("sesi lain TAK patut dipadam")
	}
}

// Ownership: id sesi milik ahli lain mesti 404 DAN barisnya mesti kekal
// utuh - bukti tapisan user_id benar-benar dalam query DELETE, bukan
// sekadar semakan selepas fetch.
func TestRevokeMySessionMilikOrangLain404(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")
	orangLain := seedMember(t, ctx, pool, "ahli", "approved")

	theirs := seedRefreshToken(t, pool, orangLain, "Device Orang Lain", "198.51.100.9", time.Hour)

	rec := callRevokeSession(t, pool, me, theirs)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("kod = %d, mahu 404. Badan: %s", rec.Code, rec.Body.String())
	}
	if !refreshTokenExists(t, pool, theirs) {
		t.Fatal("sesi ahli lain dipadam - ownership TIDAK dikuatkuasakan dalam query")
	}
}

func TestRevokeMySessionIDTakWujud404(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	me := seedMember(t, ctx, pool, "ahli", "approved")

	rec := callRevokeSession(t, pool, me, uuid.New())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("kod = %d, mahu 404. Badan: %s", rec.Code, rec.Body.String())
	}
}
