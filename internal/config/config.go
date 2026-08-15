package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                 string
	DatabaseURL          string
	JWTSecret            string
	AccessTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	OneSignalAppID       string
	OneSignalAPIKey      string
	ResendAPIKey         string
	EmailFrom            string
	PublicBaseURL        string
	EmailVerifyURL       string
	R2AccountID          string
	R2AccessKeyID        string
	R2SecretKey          string
	R2Bucket             string
	R2PublicURL          string
	StripeSecretKey      string
	StripePublishableKey string
	StripeWebhookSecret  string

	// ToyyibPay — yuran ahli (Stage 12, belum wired ke handler; lihat
	// TODO.md bahagian Payment untuk keputusan produk yang belum dibuat).
	// Akaun BERASINGAN drpd Stripe.
	ToyyibPayBaseURL      string
	ToyyibPaySecretKey    string
	ToyyibPayCategoryCode string

	// RegistrationFeeCents — yuran pendaftaran ahli SEKALI BAYAR (bukan
	// berulang), dikenakan via ToyyibPay semasa ahli baharu daftar (lihat
	// TODO.md bahagian Payment). Default RM10 (1000 sen) — PLACEHOLDER,
	// nilai sebenar belum diputuskan management, tukar via env sebelum
	// production.
	RegistrationFeeCents int

	// Optional — kosong = ciri yang bergantung padanya jatuh balik kepada
	// tingkah laku setempat (per-instance), bukan gagal.
	RedisURL string

	// Polisi simpanan. Boleh ubah tanpa deploy semula (env var), sebab ni
	// keputusan POLISI dan bukan keputusan teknikal — lihat
	// internal/retention.
	AuditPIIRetention        time.Duration
	AuditRecordRetention     time.Duration
	UploadTombstoneRetention time.Duration
	// PaymentLogRetention — 3 bulan default (keputusan produk 2026-08-15,
	// lihat internal/paymentlog). Boleh raw_payload bawa PII pembayar
	// (billTo/billEmail/billPhone ToyyibPay), sama justifikasi env-configurable
	// macam polisi lain di atas.
	PaymentLogRetention time.Duration
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
		// Optional — kalau kosong, upload post image (R2) jadi disabled
		// (endpoint pulang error jelas, bukan crash).
		R2AccountID:   os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey:   os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:      os.Getenv("R2_BUCKET_NAME"),
		R2PublicURL:   os.Getenv("R2_PUBLIC_URL"),
		// Optional — kalau kosong, donation checkout (Stage 12) jadi
		// disabled (503 graceful, bukan crash) — sama pattern R2 di atas.
		StripeSecretKey:      os.Getenv("STRIPE_SECRET_KEY"),
		StripePublishableKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),
		StripeWebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),

		// Optional — kalau kosong, ToyyibPayGateway.Enabled() pulang
		// false (sama pattern Stripe/R2 di atas). BaseURL kosong jatuh
		// balik ke produksi (https://toyyibpay.com) di dalam
		// NewToyyibPayGateway — set ke https://dev.toyyibpay.com untuk
		// sandbox.
		ToyyibPayBaseURL:      os.Getenv("TOYYIBPAY_BASE_URL"),
		ToyyibPaySecretKey:    os.Getenv("TOYYIBPAY_SECRET_KEY"),
		ToyyibPayCategoryCode: os.Getenv("TOYYIBPAY_CATEGORY_CODE"),

		// Default RM10 (1000 sen) — placeholder, lihat komen field.
		RegistrationFeeCents: getEnvInt("REGISTRATION_FEE_CENTS", 1000),

		RedisURL: os.Getenv("REDIS_URL"),

		// Default: metadata permintaan (IP/user-agent) hidup 90 hari —
		// cukup untuk menyiasat penyalahgunaan, tak lebih. Catatan audit
		// itu sendiri hidup 12 bulan. Set kepada 0 untuk matikan sapuan.
		AuditPIIRetention:        getEnvDays("AUDIT_PII_RETENTION_DAYS", 90),
		AuditRecordRetention:     getEnvDays("AUDIT_RECORD_RETENTION_DAYS", 365),
		UploadTombstoneRetention: getEnvDays("UPLOAD_TOMBSTONE_RETENTION_DAYS", 30),
		// Default 90 hari (~3 bulan, keputusan produk 2026-08-15).
		PaymentLogRetention: getEnvDays("PAYMENT_LOG_RETENTION_DAYS", 90),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required")
	}

	return cfg, nil
}

// getEnvDays baca tempoh dalam HARI. Nilai tak sah dilog dan default
// digunakan — polisi simpanan yang salah taip tak patut menghalang app
// daripada boot, tapi ia juga tak patut senyap.
func getEnvDays(key string, fallbackDays int) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return time.Duration(fallbackDays) * 24 * time.Hour
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		log.Printf("config: %s=%q tidak sah, guna default %d hari", key, raw, fallbackDays)
		return time.Duration(fallbackDays) * 24 * time.Hour
	}
	return time.Duration(days) * 24 * time.Hour
}

// getEnvInt padanan pola getEnvDays — nilai tak sah dilog dan default
// digunakan, bukan gagalkan boot.
func getEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		log.Printf("config: %s=%q tidak sah, guna default %d", key, raw, fallback)
		return fallback
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
