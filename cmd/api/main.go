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

	"marc/internal/activitysweep"
	"marc/internal/auth"
	"marc/internal/config"
	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	httpapi "marc/internal/http"
	"marc/internal/onesignal"
	"marc/internal/payment"
	"marc/internal/push"
	"marc/internal/reaper"
	"marc/internal/redisclient"
	"marc/internal/retention"
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

	// Redis pilihan. Sahkan kebolehcapaian semasa boot supaya salah
	// konfigurasi muncul di sini dan bukan pada permintaan pengguna
	// pertama — tapi JANGAN gagalkan boot: tiada apa dalam app ni yang
	// menyimpan kebenaran dalam Redis, jadi kehilangannya bermakna hilang
	// penyelarasan antara instance, bukan hilang data.
	redisCli, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redisCli.Close()
	switch {
	case !redisCli.Enabled():
		log.Printf("redis: REDIS_URL kosong — ciri berkaitan guna state setempat")
	default:
		if err := redisCli.Ping(ctx); err != nil {
			log.Printf("AMARAN redis: dikonfigur tapi tak dapat dicapai: %v", err)
		} else {
			log.Printf("redis: bersambung")
		}
	}

	jwtSvc := auth.NewJWT(cfg.JWTSecret, cfg.AccessTokenTTL)
	emailClient := email.NewClient(cfg.ResendAPIKey, cfg.EmailFrom)
	r2Client := storage.NewR2Client(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey, cfg.R2Bucket, cfg.R2PublicURL)
	// R2_PUBLIC_URL tak lagi diperlukan untuk memapar gambar — SignedURL
	// bina URL dari endpoint S3 dan berfungsi pada bucket persendirian.
	// Kalau ia masih diset, bucket berkemungkinan masih terdedah secara
	// awam, yang membatalkan tujuan URL bertandatangan.
	if r2Client.Enabled() && r2Client.HasPublicURL() {
		log.Printf("AMARAN: R2_PUBLIC_URL masih diset — kalau Public Development URL masih hidup di Cloudflare, objek boleh diambil TANPA tandatangan dan URL bertandatangan tak melindungi apa-apa")
	}
	// URL R2 yang ditandatangani dicache supaya rentetan URL kekal stabil
	// dalam satu tetingkap — cache imej peranti dikunci ikut URL, jadi
	// menandatangani semula setiap permintaan akan memaksa muat turun
	// semula setiap gambar. Cache Redis (bukan per-instance) supaya semua
	// replika memulangkan URL yang sama.
	if cache := redisCli.URLCache("r2:signed:"); cache != nil {
		r2Client.SetURLCache(cache)
	}

	onesignalClient := onesignal.NewClient(cfg.OneSignalAppID, cfg.OneSignalAPIKey)
	pushSvc := push.NewService(sqlc.New(pool), onesignalClient)

	// Payment gateway registry (Stage 12) — tambah SociaBuzz sini bila
	// siap, satu baris, tiada perubahan lain.
	//
	// "toyyibpay" kini diwiring ke RegistrationPaymentHandler (yuran
	// pendaftaran ahli SEKALI BAYAR — lihat TODO.md bahagian Payment).
	// Enabled() pulang false sehingga TOYYIBPAY_SECRET_KEY/
	// TOYYIBPAY_CATEGORY_CODE diisi. callbackURL/returnURL kini route
	// SEBENAR yang berdaftar dalam router.go (bukan placeholder /dues/...
	// lagi).
	paymentGateways := map[string]payment.Gateway{
		"stripe": payment.NewStripeGateway(cfg.StripeSecretKey, cfg.StripeWebhookSecret),
		"toyyibpay": payment.NewToyyibPayGateway(
			cfg.ToyyibPayBaseURL,
			cfg.ToyyibPaySecretKey,
			cfg.ToyyibPayCategoryCode,
			cfg.PublicBaseURL+"/registration-payments/webhook/toyyibpay",
			cfg.PublicBaseURL+"/registration-payments/return/toyyibpay",
		),
		// "toyyibpay-activity" — instance KEDUA, kredential SAMA (satu
		// akaun ToyyibPay), tapi callbackURL/returnURL berbeza (dibakar
		// tetap semasa dibina, lihat komennya di internal/http/router.go
		// atas laluan /activity-registrations/...). Wired ke
		// ActivityRegistrationPaymentHandler (yuran aktiviti berbayar,
		// activities.fee_cents) — BUKAN "toyyibpay" (yuran pendaftaran
		// ahli sekali bayar).
		"toyyibpay-activity": payment.NewToyyibPayGateway(
			cfg.ToyyibPayBaseURL,
			cfg.ToyyibPaySecretKey,
			cfg.ToyyibPayCategoryCode,
			cfg.PublicBaseURL+"/activity-registrations/webhook/toyyibpay",
			cfg.PublicBaseURL+"/activity-registrations/return/toyyibpay",
		),
	}

	// Pembersih storan (Stage 10 lanjutan) — gambar post yang dipadam dan
	// karangan post yang ditinggalkan sebelum ni kekal dalam R2 selamanya.
	reaper.New(sqlc.New(pool), r2Client, 15*time.Minute).Start(ctx)

	// Polisi simpanan — jalan sekali sehari (lihat internal/retention).
	retention.New(sqlc.New(pool), retention.Policy{
		AuditPII:        cfg.AuditPIIRetention,
		AuditRecord:     cfg.AuditRecordRetention,
		UploadTombstone: cfg.UploadTombstoneRetention,
	}, 24*time.Hour).Start(ctx)

	// Sapuan pendaftaran aktiviti berbayar yang ditinggalkan — bebaskan
	// slot kapasiti yang tersilap dipegang (lihat internal/activitysweep).
	// Kadar sama dengan reaper (15 minit); umur lapuk (45 minit) dikawal
	// dalam package itu sendiri.
	activitysweep.New(sqlc.New(pool), 15*time.Minute).Start(ctx)

	router := httpapi.NewRouter(pool, jwtSvc, cfg.RefreshTokenTTL, emailClient, cfg.PublicBaseURL, cfg.EmailVerifyURL, logger, r2Client, pushSvc, paymentGateways, cfg.RegistrationFeeCents, redisCli)

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
