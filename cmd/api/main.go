package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"marc/internal/auth"
	"marc/internal/config"
	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	httpapi "marc/internal/http"
	"marc/internal/onesignal"
	"marc/internal/payment"
	"marc/internal/push"
	"marc/internal/storage"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env tidak dijumpai, guna env sedia ada")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	jwtSvc := auth.NewJWT(cfg.JWTSecret, cfg.AccessTokenTTL)
	emailClient := email.NewClient(cfg.ResendAPIKey, cfg.EmailFrom)
	r2Client := storage.NewR2Client(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey, cfg.R2Bucket, cfg.R2PublicURL)
	onesignalClient := onesignal.NewClient(cfg.OneSignalAppID, cfg.OneSignalAPIKey)
	pushSvc := push.NewService(sqlc.New(pool), onesignalClient)

	// Payment gateway registry (Stage 12) — tambah ToyyibPay/SociaBuzz
	// sini bila siap, satu baris setiap satu, tiada perubahan lain.
	paymentGateways := map[string]payment.Gateway{
		"stripe": payment.NewStripeGateway(cfg.StripeSecretKey, cfg.StripeWebhookSecret),
	}

	router := httpapi.NewRouter(pool, jwtSvc, cfg.RefreshTokenTTL, emailClient, cfg.PublicBaseURL, cfg.EmailVerifyURL, logger, r2Client, pushSvc, paymentGateways)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("listening on :%s", cfg.Port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}
