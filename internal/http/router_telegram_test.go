package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"marc/internal/http/handlers"
)

const telegramTestToken = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

// telegramTestRouter bina bot + router ujian dgn pengesan panggilan
// handler (atomic counter).
//
// PENTING: bot.Bot.WebhookHandler() perpustakaan ni TIDAK PERNAH
// menulis status HTTP eksplisit -- baca source
// (webhook_handler.go): parameter `http.ResponseWriter` diabaikan
// terus (`_ http.ResponseWriter`) pd SETIAP cabang, termasuk bila
// header rahsia tak padan. Go tulis 200 implisit tak kira apa
// berlaku. Jadi menguji "kod = 401 bila rahsia salah" (spt didakwa
// carian web generik semasa brainstorm) MUSTAHIL dgn library ni --
// satu-satunya cara nampak kesan sebenar rahsia yg tak padan ialah
// tengok SAMA ADA handler DIPANGGIL, bukan kod respons.
func telegramTestRouter(t *testing.T, calls *atomic.Int32) *gin.Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b, err := tgbot.New(telegramTestToken,
		tgbot.WithSkipGetMe(),
		tgbot.WithWebhookSecretToken("rahsia-ujian"),
		tgbot.WithDefaultHandler(func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
			calls.Add(1)
		}),
	)
	if err != nil {
		cancel()
		t.Fatalf("bot.New: %v", err)
	}
	go b.StartWebhook(ctx)
	t.Cleanup(cancel)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/webhooks/telegram", gin.WrapF(b.WebhookHandler()))
	return r
}

const telegramTestUpdateBody = `{"update_id": 1, "message": {"message_id": 1, "date": 0, "chat": {"id": 1, "type": "private"}, "text": "/start"}}`

func TestWebhookTelegramTolakTanpaHeaderRahsiaBetul(t *testing.T) {
	var calls atomic.Int32
	r := telegramTestRouter(t, &calls)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(telegramTestUpdateBody))
	req.Header.Set("Content-Type", "application/json")
	// TIADA header X-Telegram-Bot-Api-Secret-Token disertakan.
	r.ServeHTTP(rec, req)

	// Beri masa goroutine StartWebhook proses -- kalau ia proses. 100ms
	// jauh lebih drpd cukup utk penghantaran channel setempat; ujian ni
	// sahkan KETIADAAN dlm tetingkap munasabah, bukan selama-lamanya.
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("handler dipanggil walau header rahsia salah/tiada")
	}
}

func TestWebhookTelegramTerimaDenganHeaderRahsiaBetul(t *testing.T) {
	var calls atomic.Int32
	r := telegramTestRouter(t, &calls)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(telegramTestUpdateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "rahsia-ujian")
	r.ServeHTTP(rec, req)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler dipanggil %d kali dlm 2s, mahu TEPAT 1", calls.Load())
	}
}

// TestTelegramHandlerNilMessageTidakPanik -- update Telegram jenis lain
// (poll, my_chat_member, dll) TIADA Message. Ini uji invarian sebenar
// (HandleUpdate.resolveStart tak dipanggil, tiada panik) MELALUI kod
// produksi sebenar, bukan HTTP layer -- status webhook tak boleh jadi
// bukti spt dijelaskan di atas. Pool nil SELAMAT di sini: HandleUpdate
// pulang awal pd `update.Message == nil` SEBELUM apa-apa query dibuat.
func TestTelegramHandlerNilMessageTidakPanik(t *testing.T) {
	h := handlers.NewTelegramHandler(nil, "MarcKelabBot")
	b, err := tgbot.New(telegramTestToken, tgbot.WithSkipGetMe(), tgbot.WithDefaultHandler(h.HandleUpdate))
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	h.HandleUpdate(context.Background(), b, &models.Update{ID: 1})
}
