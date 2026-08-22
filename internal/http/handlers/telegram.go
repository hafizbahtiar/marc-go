package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// telegramLinkTTL -- 10 minit, bukan 1 jam spt reset kata laluan.
// Aliran ni app->Telegram serta-merta (deep-link dibuka sebaik
// ditekan), bukan tunggu emel dibaca.
const telegramLinkTTL = 10 * time.Minute

type TelegramHandler struct {
	pool        *pgxpool.Pool
	queries     *sqlc.Queries
	botUsername string
}

// NewTelegramHandler -- botUsername kosong bermakna ciri binding
// Telegram DIMATIKAN sepenuhnya (503), padanan corak PasswordResetURL.
func NewTelegramHandler(pool *pgxpool.Pool, botUsername string) *TelegramHandler {
	return &TelegramHandler{
		pool:        pool,
		queries:     sqlc.New(pool),
		botUsername: botUsername,
	}
}

// RequestLinkToken -- POST /me/telegram-link/token. Auth.
func (h *TelegramHandler) RequestLinkToken(c *gin.Context) {
	if h.botUsername == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "binding Telegram belum tersedia",
		})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	// Permintaan baharu membunuh token lama -- padanan
	// RequestPasswordReset.
	if err := h.queries.DeleteTelegramLinkTokensByUser(ctx, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}

	token, err := auth.GenerateOpaqueToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}
	if _, err := h.queries.CreateTelegramLinkToken(ctx, sqlc.CreateTelegramLinkTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(telegramLinkTTL), Valid: true},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana pautan"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deep_link": fmt.Sprintf("https://t.me/%s?start=%s", h.botUsername, token),
	})
}

// DeleteLink -- DELETE /me/telegram-link. Auth. Idempoten: 204 sentiasa,
// tak kira sebelum ni terikat atau tidak.
func (h *TelegramHandler) DeleteLink(c *gin.Context) {
	userID := middleware.UserID(c)
	if err := h.queries.ClearTelegramLink(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal nyahikat"})
		return
	}
	c.Status(http.StatusNoContent)
}

// resolveStart -- logik TULEN /start, tanpa kebergantungan rangkaian
// Telegram. Diuji terus (lihat telegram_live_test.go) tanpa perlu
// mock HTTP -- HandleUpdate di bawah cuma wrapper nipis yang hantar
// rentetan pulangan ni sbg mesej bot.
func (h *TelegramHandler) resolveStart(ctx context.Context, chatID int64, username, token string) string {
	const (
		msgGreeting = "Selamat datang ke bot MARC! Muat turun app di " +
			"https://play.google.com/store/apps/details?id=com.hafizbahtiar.marc " +
			"untuk sambungkan akaun anda."
		msgTokenTidakSah  = "Pautan tidak sah atau sudah luput. Cuba jana pautan baharu dari app."
		msgPertindihan    = "Akaun Telegram ini sudah disambungkan ke akaun MARC lain. Guna akaun Telegram yang berbeza."
		msgSudahDisambung = "Akaun kamu dah disambungkan ke MARC."
		msgBerjaya        = "Akaun MARC anda berjaya disambungkan!"
		msgRalat          = "Ralat dalaman. Cuba lagi dari app."
	)

	chatIDParam := pgtype.Int8{Int64: chatID, Valid: true}

	if token == "" {
		// Chat ni dah bind akaun (mana-mana) -- balas status, jangan
		// greeting. Merangkumi kes "/start (chat dah bind akaun ni)"
		// tanpa perlu token sebab tujuan mesej ni cuma maklum balas
		// status, bukan tindakan.
		if _, err := h.queries.GetUserIDByTelegramChatID(ctx, chatIDParam); err == nil {
			return msgSudahDisambung
		}
		return msgGreeting
	}

	rec, err := h.queries.ConsumeTelegramLinkToken(ctx, auth.HashToken(token))
	if err != nil {
		return msgTokenTidakSah
	}
	if rec.ExpiresAt.Time.Before(time.Now()) {
		return msgTokenTidakSah
	}

	if existingUserID, err := h.queries.GetUserIDByTelegramChatID(ctx, chatIDParam); err == nil && existingUserID != rec.UserID {
		return msgPertindihan
	}

	if err := h.queries.SetTelegramLink(ctx, sqlc.SetTelegramLinkParams{
		UserID:           rec.UserID,
		TelegramChatID:   chatIDParam,
		TelegramUsername: pgtype.Text{String: username, Valid: username != ""},
	}); err != nil {
		log.Printf("telegram: simpan binding gagal (user=%s): %v", rec.UserID, err)
		return msgRalat
	}

	return msgBerjaya
}

// HandleUpdate -- tandatangan tepat bot.HandlerFunc, dihantar terus
// ke bot.WithDefaultHandler() (cmd/api/main.go). Webhook Telegram
// MESTI sentiasa terima 200 -- ralat "kpd pengguna" dihantar sbg
// mesej bot (lihat resolveStart), BUKAN status HTTP. bot.WebhookHandler()
// sendiri yg uruskan pengesahan header X-Telegram-Bot-Api-Secret-Token
// (bot.WithWebhookSecretToken semasa bot.New()) SEBELUM handler ni
// dipanggil.
func (h *TelegramHandler) HandleUpdate(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	if !strings.HasPrefix(text, "/start") {
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(text, "/start"))

	username := ""
	if update.Message.From != nil {
		username = update.Message.From.Username
	}

	reply := h.resolveStart(ctx, update.Message.Chat.ID, username, token)
	if reply == "" {
		return
	}
	if _, err := b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   reply,
	}); err != nil {
		log.Printf("telegram: hantar balasan gagal (chat=%d): %v", update.Message.Chat.ID, err)
	}
}
