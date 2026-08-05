package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Port            string
	DatabaseURL     string
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	OneSignalAppID  string
	OneSignalAPIKey string
	ResendAPIKey    string
	EmailFrom       string
	PublicBaseURL   string
	EmailVerifyURL  string
}

func Load() (Config, error) {
	cfg := Config{
		Port:            getEnv("PORT", "8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		// Optional — kalau kosong, push notification jadi no-op senyap
		// (padanan dengan initOneSignal() di Flutter).
		OneSignalAppID:  os.Getenv("ONESIGNAL_APP_ID"),
		OneSignalAPIKey: os.Getenv("ONESIGNAL_API_KEY"),
		// Optional — kalau kosong, email verification jadi no-op
		// senyap (token tetap dijana + disimpan, cuma tak dihantar;
		// lihat log server).
		ResendAPIKey:  os.Getenv("RESEND_API_KEY"),
		EmailFrom:     os.Getenv("EMAIL_FROM"),
		PublicBaseURL: getEnv("PUBLIC_BASE_URL", "http://localhost:"+getEnv("PORT", "8080")),
		// Optional — page landing untuk link email verification (Stage 8,
		// portfolio-astro). Kalau kosong, fallback ke Go punya HTML page
		// sendiri (PublicBaseURL + /auth/verify-email/confirm).
		EmailVerifyURL: os.Getenv("EMAIL_VERIFY_URL"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
