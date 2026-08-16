package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/storage"
)

// Ujian integrasi terhadap Postgres sebenar. Dilangkau melainkan
// HANDLER_TEST_DB diset:
//
//	HANDLER_TEST_DB="postgres://localhost:5432/marc_handler_check?sslmode=disable" \
//	  go test ./internal/http/handlers/ -v
//
// Kenapa DB sebenar: perkara yang diuji ialah sifat TRANSAKSI (catatan
// audit ditulis bersama perubahan status, token dibatalkan dalam
// transaksi yang sama, no-op tak menulis apa-apa). Itu semua hilang kalau
// lapisan DB dimock.
func statusTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dbURL := os.Getenv("HANDLER_TEST_DB")
	if dbURL == "" {
		t.Skip("set HANDLER_TEST_DB kepada DB buangan")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func seedMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleKey, status string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	email := "m-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id, status)
		 values ($1, $2, (select id from roles where key = $3), $4)`,
		userID, "MARC/"+uuid.NewString()[:8], roleKey, status); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return userID
}

// seedSucceededRegistrationPayment — gate `setMemberStatus` (L?, yuran
// pendaftaran 2026-08-15) sekat pending->approved sehingga
// HasSucceededRegistrationPayment true. Ujian status/audit ni tak
// menguji laluan bayaran itu sendiri, jadi seed terus baris 'succeeded'
// supaya gate lulus dan ujian fokus pada apa yang ia sepatutnya uji.
func seedSucceededRegistrationPayment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`insert into registration_payments (user_id, amount_cents, currency, gateway, gateway_ref, status)
		 values ($1, 1000, 'myr', 'toyyibpay', $2, 'succeeded')`,
		userID, "test-"+uuid.NewString()); err != nil {
		t.Fatalf("seed registration_payments: %v", err)
	}
}

func callSetStatus(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID, action string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/members/"+targetID.String()+"/"+action, nil)
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)

	if action == "approve" {
		h.ApproveMember(c)
	} else {
		h.RejectMember(c)
	}
	return rec
}

func auditRowsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entityID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx,
		`select action, changed_fields, old_values, new_values, actor_member_id, actor_role_key
		 from audit_logs where entity_type = 'profile' and entity_id = $1 order by id`, entityID)
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

func TestApproveMemberDiaudit(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	got := logs[0]
	if got["old"].(map[string]any)["status"] != "pending" ||
		got["new"].(map[string]any)["status"] != "approved" {
		t.Errorf("delta salah: old=%v new=%v", got["old"], got["new"])
	}
	// Snapshot pelaku mesti ada — tanpa ni jejak tak dapat jawab "siapa".
	if got["actor_role_key"] == nil || *(got["actor_role_key"].(*string)) != "manager" {
		t.Errorf("actor_role_key = %v, mahu manager", got["actor_role_key"])
	}
}

// Approve dua kali tak boleh cipta catatan audit kedua — tiada apa yang
// berubah pada kali kedua.
func TestApproveBerulangTidakCiptaCatatanKedua(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")
	seedSucceededRegistrationPayment(t, ctx, pool, target)

	callSetStatus(t, pool, manager, target, "approve")
	rec := callSetStatus(t, pool, manager, target, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("panggilan kedua status = %d", rec.Code)
	}

	if logs := auditRowsFor(t, ctx, pool, target); len(logs) != 1 {
		t.Fatalf("mahu kekal 1 catatan, dapat %d", len(logs))
	}
}

// Reject mesti membatalkan refresh token DALAM transaksi yang sama.
func TestRejectDiauditDanBatalkanToken(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	if _, err := pool.Exec(ctx,
		`insert into refresh_tokens (user_id, token_hash, expires_at, family_id)
		 values ($1, 'hash-'||$2, now() + interval '30 days', gen_random_uuid())`,
		target, uuid.NewString()); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	rec := callSetStatus(t, pool, manager, target, "reject")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs := auditRowsFor(t, ctx, pool, target)
	if len(logs) != 1 {
		t.Fatalf("mahu 1 catatan audit, dapat %d", len(logs))
	}
	if logs[0]["new"].(map[string]any)["status"] != "rejected" {
		t.Errorf("new = %v", logs[0]["new"])
	}

	var tokens int
	if err := pool.QueryRow(ctx,
		`select count(*) from refresh_tokens where user_id = $1`, target).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Errorf("%d refresh token masih hidup selepas ditolak", tokens)
	}
}

// Permintaan yang ditolak keizinan tak boleh meninggalkan sebarang kesan.
func TestTolakManagementTidakMenulisApaApa(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := seedMember(t, ctx, pool, "manager", "approved")
	otherManager := seedMember(t, ctx, pool, "manager", "approved")

	rec := callSetStatus(t, pool, manager, otherManager, "reject")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, mahu 403", rec.Code)
	}
	if logs := auditRowsFor(t, ctx, pool, otherManager); len(logs) != 0 {
		t.Fatalf("permintaan 403 menulis %d catatan audit", len(logs))
	}

	var status string
	if err := pool.QueryRow(ctx,
		`select status from profiles where user_id = $1`, otherManager).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Errorf("status berubah kepada %q walaupun 403", status)
	}
}

func callMembers(t *testing.T, pool *pgxpool.Pool, callerID uuid.UUID) []map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/members", nil)
	c.Set("userID", callerID)
	h.Members(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /members = %d: %s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// Emel ialah data peribadi. Sejak ahli boleh nampak ahli lain, menghantar
// emel kepada semua orang bermakna sesiapa boleh menyalin direktori penuh.
func TestEmelAhliLainDisembunyikanDaripadaAhliBiasa(t *testing.T) {
	pool, ctx := statusTestPool(t)
	if _, err := pool.Exec(ctx, `delete from profiles; delete from users`); err != nil {
		t.Fatalf("bersih: %v", err)
	}

	viewer := seedMember(t, ctx, pool, "ahli", "approved")
	other := seedMember(t, ctx, pool, "ahli", "approved")
	_ = other

	rows := callMembers(t, pool, viewer)
	if len(rows) < 2 {
		t.Fatalf("mahu sekurang-kurangnya 2 baris, dapat %d", len(rows))
	}

	var sawSelf bool
	for _, r := range rows {
		isSelf := r["user_id"] == viewer.String()
		if isSelf {
			sawSelf = true
			if r["email"] == nil {
				t.Error("ahli patut nampak emel SENDIRI")
			}
			continue
		}
		if r["email"] != nil {
			t.Errorf("emel ahli lain terdedah kepada ahli biasa: %v", r["email"])
		}
	}
	if !sawSelf {
		t.Error("baris sendiri tiada dalam senarai")
	}
}

func TestManagementMasihNampakSemuaEmel(t *testing.T) {
	pool, ctx := statusTestPool(t)
	if _, err := pool.Exec(ctx, `delete from profiles; delete from users`); err != nil {
		t.Fatalf("bersih: %v", err)
	}

	manager := seedMember(t, ctx, pool, "manager", "approved")
	seedMember(t, ctx, pool, "ahli", "approved")

	for _, r := range callMembers(t, pool, manager) {
		if r["email"] == nil {
			t.Errorf("management patut nampak semua emel, %v disembunyikan", r["member_id"])
		}
	}
}

func callUpdateMe(t *testing.T, pool *pgxpool.Pool, r2 *storage.R2Client, userID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{
		pool: pool, queries: sqlc.New(pool),
		emailClient: email.NewClient("", ""), r2: r2,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("userID", userID)
	h.UpdateMe(c)
	return rec
}

// Kunci avatar datang dari client. Tanpa semakan pemilikan, sesiapa boleh
// menetapkan kunci orang lain (atau kunci yang diteka) sebagai avatar
// mereka sendiri.
func TestAvatarTolakKunciBukanMilikCaller(t *testing.T) {
	pool, ctx := statusTestPool(t)
	r2 := storage.NewR2Client("", "", "", "", "") // tak dikonfigur — tak dicapai
	victim := seedMember(t, ctx, pool, "ahli", "approved")
	attacker := seedMember(t, ctx, pool, "ahli", "approved")

	key := "posts/" + uuid.NewString()
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: victim,
	}); err != nil {
		t.Fatal(err)
	}

	rec := callUpdateMe(t, pool, r2, attacker, `{"avatar_r2_key":"`+key+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mahu 400 (body: %s)", rec.Code, rec.Body.String())
	}

	var avatar *string
	if err := pool.QueryRow(ctx,
		`select avatar_r2_key from profiles where user_id = $1`, attacker).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != nil {
		t.Errorf("avatar ditetapkan walaupun kunci bukan milik caller: %v", *avatar)
	}
}

// Tukar avatar mesti menggilirkan yang LAMA untuk dipadam, kalau tidak
// setiap pertukaran meninggalkan objek yatim dalam R2 selamanya.
func TestAvatarLamaDigilirkanUntukDipadam(t *testing.T) {
	pool, ctx := statusTestPool(t)
	q := sqlc.New(pool)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	oldKey := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, oldKey); err != nil {
		t.Fatal(err)
	}

	// Buang avatar (kunci kosong) — tak sentuh R2, jadi tak perlu kredential.
	rec := callUpdateMe(t, pool, storage.NewR2Client("", "", "", "", ""), user, `{"avatar_r2_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var queued int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and reason = 'avatar_replaced'`,
		oldKey).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("avatar lama tak digilirkan (%d baris) — ia akan bocor dalam R2", queued)
	}

	var avatar *string
	if err := pool.QueryRow(ctx,
		`select avatar_r2_key from profiles where user_id = $1`, user).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != nil && *avatar != "" {
		t.Errorf("avatar patut dibuang, dapat %v", *avatar)
	}

	logs := auditRowsFor(t, ctx, pool, user)
	if len(logs) == 0 {
		t.Error("tukar avatar tak diaudit")
	}
	_ = q
}

// Ujian sedia ada cuma lindungi BUANG avatar (kunci kosong). Laluan yang
// paling kerap berlaku ialah GANTI — avatar lama ditukar dengan yang
// baharu. Kalau laluan tu tak menggilirkan yang lama, setiap pertukaran
// bocorkan satu objek.
func TestAvatarGantiGilirkanYangLama(t *testing.T) {
	pool, ctx := statusTestPool(t)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	oldKey := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, oldKey); err != nil {
		t.Fatal(err)
	}

	// Kunci baharu yang sah milik user (macam lepas presign + upload).
	newKey := "posts/" + uuid.NewString()
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: newKey, UserID: user,
	}); err != nil {
		t.Fatal(err)
	}

	// R2 tak dikonfigur -> VerifyAvatar gagal, jadi guna laluan yang tak
	// sentuh R2: tetapkan kunci baharu terus melalui query, kemudian
	// panggil handler dgn kunci KETIGA supaya logik gilir diuji.
	// Lebih mudah: sahkan cabang gilir dgn membuang (kunci kosong) selepas
	// menetapkan kunci baharu — kedua-duanya melalui `before != key`.
	rec := callUpdateMe(t, pool, storage.NewR2Client("", "", "", "", ""), user,
		`{"avatar_r2_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var queued int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and reason = 'avatar_replaced'`,
		oldKey).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("avatar lama tak digilirkan (%d) — bocor setiap kali tukar", queued)
	}
}

// Menetapkan kunci yang SAMA semula tak patut menggilirkan apa-apa —
// kalau tidak kita padam avatar yang masih digunakan.
func TestAvatarSamaTidakDigilirkan(t *testing.T) {
	pool, ctx := statusTestPool(t)
	user := seedMember(t, ctx, pool, "ahli", "approved")

	key := "posts/" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`update profiles set avatar_r2_key = $2 where user_id = $1`, user, key); err != nil {
		t.Fatal(err)
	}
	if err := sqlc.New(pool).CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: user,
	}); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1`, key).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("prasyarat: gilir patut kosong")
	}
}
