package handlers

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
)

// randomChatID -- telegram_chat_id ada unique constraint di DB, dan
// ujian live ni jalan atas DB yang TAK di-truncate antara larian
// `go test` berasingan. Literal berangka spt 4004/5005 akan langgar
// constraint pada larian kedua. Julat positif int32 cukup besar utk
// elak perlanggaran dalam satu larian, dan realistik sbg chat ID
// Telegram sebenar (ID group/user Telegram muat dlm int32/int53).
func randomChatID(t *testing.T) int64 {
	t.Helper()
	return rand.Int64N(1 << 31)
}

func telegramHandler(pool *pgxpool.Pool) *TelegramHandler {
	return NewTelegramHandler(pool, "MarcKelabBot")
}

func telegramTokenRequestCall(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/me/telegram-link/token", nil)
	c.Set("userID", userID)

	telegramHandler(pool).RequestLinkToken(c)
	c.Writer.WriteHeaderNow()
	return rec
}

func countTelegramLinkTokens(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from telegram_link_tokens where user_id = $1`,
		userID).Scan(&n); err != nil {
		t.Fatalf("kira token: %v", err)
	}
	return n
}

func decodeJSON(b []byte, v interface{}) error {
	return json.Unmarshal(b, v)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestRequestTelegramLinkTokenMenciptaToken(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	rec := telegramTokenRequestCall(t, pool, userID)

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}
	if got := countTelegramLinkTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d, mahu 1", got)
	}
	var body struct {
		DeepLink string `json:"deep_link"`
	}
	if err := decodeJSON(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !contains(body.DeepLink, "https://t.me/MarcKelabBot?start=") {
		t.Fatalf("deep_link tak sepadan corak: %s", body.DeepLink)
	}
}

// Permintaan kedua mesti membunuh token pertama -- padanan invarian
// reset kata laluan.
func TestRequestTelegramLinkTokenKeduaMembatalkanYangPertama(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	telegramTokenRequestCall(t, pool, userID)
	telegramTokenRequestCall(t, pool, userID)

	if got := countTelegramLinkTokens(t, pool, userID); got != 1 {
		t.Fatalf("token = %d selepas dua permintaan, mahu 1", got)
	}
}

func TestRequestTelegramLinkTokenTanpaBotUsernamePulang503(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/me/telegram-link/token", nil)
	c.Set("userID", userID)

	NewTelegramHandler(pool, "").RequestLinkToken(c)
	c.Writer.WriteHeaderNow()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("kod = %d, mahu 503", rec.Code)
	}
	if got := countTelegramLinkTokens(t, pool, userID); got != 0 {
		t.Errorf("token = %d ditulis walaupun ciri dimatikan", got)
	}
}

func TestDeleteTelegramLinkIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	for i := 0; i < 2; i++ {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodDelete, "/me/telegram-link", nil)
		c.Set("userID", userID)

		telegramHandler(pool).DeleteLink(c)
		c.Writer.WriteHeaderNow()

		if rec.Code != http.StatusNoContent {
			t.Fatalf("panggilan %d: kod = %d, mahu 204", i+1, rec.Code)
		}
	}
}

func TestDeleteTelegramLinkKosongkanLajur(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	// Seed binding SEBENAR (TelegramChatID.Valid: true) -- kalau cuma
	// UserID diberi, TelegramChatID kekal zero-value (Valid: false) dan
	// ujian ni lulus walau DeleteLink tak buat apa-apa, sebab tiada apa
	// utk dikosongkan pun.
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           userID,
		TelegramChatID:   pgtype.Int8{Int64: randomChatID(t), Valid: true},
		TelegramUsername: pgtype.Text{String: "sblm_nyahikat", Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID); err != nil || !row.TelegramChatID.Valid {
		t.Fatalf("seed gagal -- binding tak tertulis (err=%v, valid=%v)", err, row.TelegramChatID.Valid)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/me/telegram-link", nil)
	c.Set("userID", userID)
	telegramHandler(pool).DeleteLink(c)
	c.Writer.WriteHeaderNow()

	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Error("telegram_chat_id masih terisi selepas nyahikat")
	}
}

func TestMeResponseMemaparkanKeadaanTelegram(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil sblm bind: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Fatal("telegram_chat_id patut kosong sblm binding")
	}

	chatID := randomChatID(t)
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           userID,
		TelegramChatID:   pgtype.Int8{Int64: chatID, Valid: true},
		TelegramUsername: pgtype.Text{String: "ahliuji", Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	row, err = sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil selepas bind: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != chatID {
		t.Fatalf("telegram_chat_id = %+v, mahu %d", row.TelegramChatID, chatID)
	}
	if !row.TelegramUsername.Valid || row.TelegramUsername.String != "ahliuji" {
		t.Fatalf("telegram_username = %+v, mahu ahliuji", row.TelegramUsername)
	}
}

func seedTelegramLinkToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, ttl time.Duration) string {
	t.Helper()
	raw, err := auth.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into telegram_link_tokens (user_id, token_hash, expires_at)
		 values ($1, $2, now() + $3::interval)`,
		userID, auth.HashToken(raw), ttl.String()); err != nil {
		t.Fatalf("sisip token: %v", err)
	}
	return raw
}

func TestResolveStartTanpaTokenAhliBaharuBalasGreeting(t *testing.T) {
	pool := activityTestPool(t)
	reply := telegramHandler(pool).resolveStart(context.Background(), randomChatID(t), "sesiapa", "")
	if !contains(reply, "play.google.com") {
		t.Fatalf("balasan tak ada pautan Play Store: %s", reply)
	}
}

func TestResolveStartTokenLuputDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, -time.Minute)

	reply := telegramHandler(pool).resolveStart(ctx, randomChatID(t), "u", token)

	if !contains(reply, "tidak sah") && !contains(reply, "luput") {
		t.Fatalf("balasan tak sebut token tak sah/luput: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Valid {
		t.Error("binding tertulis walaupun token luput")
	}
}

func TestResolveStartBerjayaMenulisBinding(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)

	chatID := randomChatID(t)
	reply := telegramHandler(pool).resolveStart(ctx, chatID, "ujian_user", token)

	if contains(reply, "tidak sah") || contains(reply, "Ralat") {
		t.Fatalf("balasan tak dijangka: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != chatID {
		t.Fatalf("telegram_chat_id = %+v, mahu %d", row.TelegramChatID, chatID)
	}
	if !row.TelegramUsername.Valid || row.TelegramUsername.String != "ujian_user" {
		t.Fatalf("telegram_username = %+v, mahu ujian_user", row.TelegramUsername)
	}
}

// Ujian perlumbaan -- padanan TestConfirmResetSekaliGunaDiBawahPerlumbaan.
// Buang tuntutan atomik (Consume...) MESTI buat ujian ni gagal.
func TestResolveStartSekaliGunaDiBawahPerlumbaan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	berjaya := 0
	for i := 0; i < n; i++ {
		chatID := randomChatID(t) // dijana di goroutine UTAMA, bukan dlm closure serentak
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			reply := telegramHandler(pool).resolveStart(ctx, chatID, "u", token)
			if !contains(reply, "tidak sah") {
				mu.Lock()
				berjaya++
				mu.Unlock()
			}
		}(chatID)
	}
	wg.Wait()

	if berjaya != 1 {
		t.Fatalf("%d permintaan serentak berjaya, mahu TEPAT 1", berjaya)
	}
}

func TestResolveStartChatTerikatAkaunLainDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userA := seedMember(t, ctx, pool, "ahli", "approved")
	userB := seedMember(t, ctx, pool, "ahli", "approved")

	chatID := randomChatID(t)
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userA,
		TelegramChatID: pgtype.Int8{Int64: chatID, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding userA: %v", err)
	}

	token := seedTelegramLinkToken(t, ctx, pool, userB, time.Hour)
	reply := telegramHandler(pool).resolveStart(ctx, chatID, "userB_tg", token)

	if !contains(reply, "disambungkan ke akaun MARC lain") {
		t.Fatalf("balasan tak sebut pertindihan: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userA)
	if err != nil {
		t.Fatalf("baca profil userA: %v", err)
	}
	if !row.TelegramChatID.Valid || row.TelegramChatID.Int64 != chatID {
		t.Fatal("binding userA berubah -- sepatutnya tak tersentuh")
	}
}

func TestResolveStartUserSediaBindingGantiChatBaharu(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	chatLama := randomChatID(t)
	chatBaharu := randomChatID(t)
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userID,
		TelegramChatID: pgtype.Int8{Int64: chatLama, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding lama: %v", err)
	}

	token := seedTelegramLinkToken(t, ctx, pool, userID, time.Hour)
	reply := telegramHandler(pool).resolveStart(ctx, chatBaharu, "chat_baharu", token)

	if contains(reply, "tidak sah") || contains(reply, "Ralat") {
		t.Fatalf("balasan tak dijangka: %s", reply)
	}
	row, err := sqlc.New(pool).GetProfileByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("baca profil: %v", err)
	}
	if row.TelegramChatID.Int64 != chatBaharu {
		t.Fatalf("telegram_chat_id = %d, mahu %d (chat baharu)", row.TelegramChatID.Int64, chatBaharu)
	}
}

func TestResolveStartChatSediaTerikatBalasSudahDisambung(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	chatID := randomChatID(t)
	if err := sqlc.New(pool).SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:         userID,
		TelegramChatID: pgtype.Int8{Int64: chatID, Valid: true},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	reply := telegramHandler(pool).resolveStart(ctx, chatID, "u", "")

	if !contains(reply, "dah disambung") {
		t.Fatalf("balasan tak sebut 'dah disambung': %s", reply)
	}
}
