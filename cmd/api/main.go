package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/joho/godotenv"

	"marc/internal/auth"
	"marc/internal/config"
	"marc/internal/db"
	"marc/internal/email"
	httpapi "marc/internal/http"
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
	router := httpapi.NewRouter(pool, jwtSvc, cfg.RefreshTokenTTL, emailClient, cfg.PublicBaseURL, logger)

	log.Printf("listening on :%s", cfg.Port)
	if err := router.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server: %v", err)
	}
}
