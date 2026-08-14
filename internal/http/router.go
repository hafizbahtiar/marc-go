package http

import (
	"log"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/handlers"
	"marc/internal/http/middleware"
	"marc/internal/payment"
	"marc/internal/push"
	"marc/internal/redisclient"
	"marc/internal/storage"
)

// trustedProxyRanges — Railway hantar request ke container melalui proxy
// dalaman atas "Railway Private Network", yang guna CGNAT range
// 100.64.0.0/10 (bukan RFC1918 biasa) — verified terus daripada log
// produksi (c.ClientIP() asalnya papar 100.64.x.x sebagai client, bukan
// IP awam sebenar, sebelum range ni ditambah). RFC1918 standard turut
// disertakan untuk keserasian persekitaran lain (Docker/VPC dalaman).
var trustedProxyRanges = []string{
	"100.64.0.0/10",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// authRateLimit — 5 percubaan seminit setiap IP (burst 5) untuk route
// paling sensitif kepada brute-force/spam (login, register). Angka ni
// baseline biasa; ketat lagi kalau mula nampak abuse.
var authRateLimit = rate.Every(12 * time.Second)

const authRateBurst = 5

func NewRouter(
	pool *pgxpool.Pool,
	jwtSvc *auth.JWT,
	refreshTTL time.Duration,
	emailClient *email.Client,
	publicBaseURL string,
	emailVerifyURL string,
	logger *slog.Logger,
	r2Client *storage.R2Client,
	pushSvc *push.Service,
	paymentGateways map[string]payment.Gateway,
	redisCli *redisclient.Client,
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestLogger(logger), middleware.MaxBodySize(1<<20))
	if err := r.SetTrustedProxies(trustedProxyRanges); err != nil {
		log.Fatalf("set trusted proxies: %v", err)
	}

	r.GET("/healthz", handlers.Health)

	authHandler := handlers.NewAuthHandler(pool, jwtSvc, refreshTTL, emailClient, publicBaseURL, emailVerifyURL)

	// Satu factory, tiga had bernama. Nama MESTI unik — dalam Redis ia
	// yang mengasingkan baldi; tanpa itu login dan upload berkongsi kuota.
	rateLimiter := middleware.NewRateLimiter(redisCli)
	authRateLimiter := rateLimiter.Limit("auth", authRateLimit, authRateBurst)

	authGroup := r.Group("/auth")
	authGroup.POST("/register", authRateLimiter, authHandler.Register)
	authGroup.POST("/login", authRateLimiter, authHandler.Login)
	authGroup.POST("/refresh", authRateLimiter, authHandler.Refresh)
	authGroup.POST("/logout", authRateLimiter, authHandler.Logout)
	authGroup.POST("/verify-email/confirm", authRateLimiter, authHandler.ConfirmEmailVerification)
	authGroup.GET("/verify-email/confirm", authRateLimiter, authHandler.ConfirmEmailVerificationLink)

	protectedAuthGroup := r.Group("/auth", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)))
	protectedAuthGroup.POST("/verify-email/request", authRateLimiter, authHandler.RequestEmailVerification)

	profileHandler := handlers.NewProfileHandler(pool, emailClient, r2Client)
	deviceTokenHandler := handlers.NewDeviceTokenHandler(pool)

	protected := r.Group("/", middleware.RequireAuth(jwtSvc))
	protected.GET("/me", profileHandler.Me)
	protected.PATCH("/me", profileHandler.UpdateMe)
	protected.POST("/auth/logout-all", authHandler.LogoutAll)

	// approved (Stage 11) — /members, /device-tokens, dan approve/reject
	// sendiri perlu status=approved. /me sengaja TAK di sini (lihat
	// RequireApprovedStatus).
	approved := r.Group("/", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)))
	approved.GET("/members", profileHandler.Members)
	approved.POST("/members/:id/approve", profileHandler.ApproveMember)
	approved.POST("/members/:id/reject", profileHandler.RejectMember)
	approved.PATCH("/members/:id/role", profileHandler.UpdateMemberRole)

	// Jejak audit (management sahaja, dikuatkuasakan dalam handler).
	approved.GET("/audit-logs", handlers.NewAuditHandler(pool).List)
	approved.GET("/roles", profileHandler.ListRoles)
	approved.POST("/device-tokens", deviceTokenHandler.Upsert)
	approved.DELETE("/device-tokens/:id", deviceTokenHandler.Delete)
	approved.DELETE("/device-tokens/by-onesignal/:onesignalId", deviceTokenHandler.DeleteByOnesignalID)

	// Posts (Stage 10) — perlu email_verified, lapisan tambahan atas
	// RequireAuth. Payment gate akan ditambah dalam middleware ni bila
	// payment system siap (bukan sekarang).
	postHandler := handlers.NewPostHandler(pool, r2Client, pushSvc)
	commentHandler := handlers.NewCommentHandler(pool, pushSvc, r2Client)
	notificationHandler := handlers.NewNotificationHandler(pool)
	uploadHandler := handlers.NewUploadHandler(pool, r2Client)

	verified := r.Group("/", middleware.RequireAuth(jwtSvc), middleware.RequireApprovedStatus(sqlc.New(pool)), middleware.RequireVerifiedEmail(sqlc.New(pool)))
	verified.GET("/posts", postHandler.List)
	verified.POST("/posts", postHandler.Create)
	verified.GET("/posts/:id", postHandler.Get)
	verified.PATCH("/posts/:id", postHandler.Update)
	verified.DELETE("/posts/:id", postHandler.Delete)
	verified.POST("/posts/:id/like", postHandler.Like)
	verified.DELETE("/posts/:id/like", postHandler.Unlike)

	verified.GET("/posts/:id/comments", commentHandler.List)
	verified.POST("/posts/:id/comments", commentHandler.Create)
	verified.PATCH("/comments/:id", commentHandler.Update)
	verified.DELETE("/comments/:id", commentHandler.Delete)
	verified.POST("/comments/:id/like", commentHandler.Like)
	verified.DELETE("/comments/:id/like", commentHandler.Unlike)

	// Aktiviti — baca untuk sesiapa yang approved, tulis perlu email
	// disahkan. Semakan "pengurusan sahaja" dibuat dalam handler
	// (authz.IsManagement), sama seperti audit log dan profil.
	activityHandler := handlers.NewActivityHandler(pool, pushSvc)

	approved.GET("/activity-categories", activityHandler.ListCategories)
	approved.GET("/activities", activityHandler.List)
	approved.GET("/activities/:id", activityHandler.Get)

	verified.POST("/activities", activityHandler.Create)
	verified.PATCH("/activities/:id", activityHandler.Update)
	verified.POST("/activities/:id/publish", activityHandler.Publish)
	verified.POST("/activities/:id/cancel", activityHandler.Cancel)
	verified.PUT("/activities/:id/sessions", activityHandler.ReplaceSessions)

	// Pendaftaran ahli. Membaca senarai sendiri cukup dengan `approved`,
	// tetapi MENDAFTAR duduk atas `verified` dengan sengaja: pendaftaran
	// ialah komitmen yang meletakkan nama sebenar ahli pada sijil.
	registrationHandler := handlers.NewRegistrationHandler(pool)

	approved.GET("/me/activities", registrationHandler.ListMine)
	verified.POST("/activities/:id/registration", registrationHandler.Register)
	verified.DELETE("/activities/:id/registration", registrationHandler.Cancel)
	verified.GET("/activities/:id/registrations", registrationHandler.ListForActivity)

	// Kehadiran per-sesi. Kedua-dua route pengurusan sahaja (dikuatkuasakan
	// dalam handler) — kehadiran ialah bukti yang menentukan siapa menerima
	// sijil, jadi ia tidak boleh ditanda oleh penerimanya sendiri.
	attendanceHandler := handlers.NewAttendanceHandler(pool)

	verified.POST("/activities/:id/sessions/:sid/attendance", attendanceHandler.Mark)
	verified.DELETE("/activities/:id/sessions/:sid/attendance/:rid", attendanceHandler.Unmark)

	// Sijil. Terbit/tarik-balik ialah pengurusan (dikuatkuasakan dalam
	// handler) dan duduk atas `verified`; membaca sijil SENDIRI cukup
	// dengan `approved` — sama garisan seperti /me/activities.
	certificateHandler := handlers.NewCertificateHandler(pool, r2Client, pushSvc, publicBaseURL)

	verified.POST("/activities/:id/certificates", certificateHandler.Issue)
	verified.POST("/certificates/:id/revoke", certificateHandler.Revoke)

	approved.GET("/me/certificates", certificateHandler.ListMine)
	approved.GET("/me/certificates/:id/file", certificateHandler.Download)

	// Pengesahan sijil — AWAM, atas router akar dan bukan `approved`: kod QR
	// pada sijil bercetak discan oleh majikan yang tiada akaun MARC.
	//
	// Baldi had kadar DINAMAKAN. Baldi tanpa nama berkongsi kunci Redis
	// dengan 'auth' dan 'upload', jadi trafik pengesahan awam akan
	// menghabiskan kuota log masuk ahli.
	// Laluan dan nama baldi ialah pemalar dieksport dalam `handlers` supaya
	// ia tak boleh terpesong daripada rujukan lain (lihat komennya di sana).
	verifyRateLimiter := rateLimiter.Limit(handlers.VerifyRateLimitBucket, rate.Every(2*time.Second), 20)
	r.GET(handlers.VerifyCertificateRoute, verifyRateLimiter, certificateHandler.Verify)

	uploadRateLimiter := rateLimiter.Limit("upload", rate.Every(6*time.Second), 5)
	verified.POST("/uploads/presign", uploadRateLimiter, uploadHandler.Presign)

	verified.GET("/notifications", notificationHandler.List)
	verified.POST("/notifications/:id/read", notificationHandler.MarkRead)
	verified.POST("/notifications/read-all", notificationHandler.MarkAllRead)

	// Donation (Stage 12, Stripe sahaja buat masa ni — SociaBuzz/threshold
	// routing belum wired) — route AWAM sengaja, guna OptionalAuth supaya
	// ahli MARC yang log masuk dikaitkan user_id tanpa wajibkan akaun
	// untuk donate. Handler bergantung payment.Gateway (interface), bukan
	// Stripe terus — tambah gateway baru = daftar dlm paymentGateways
	// (cmd/api/main.go), tiada perubahan di sini.
	donationHandler := handlers.NewDonationHandler(pool, paymentGateways, emailClient)
	donationRateLimiter := rateLimiter.Limit("donation", rate.Every(6*time.Second), 5)
	r.POST("/donations/checkout", donationRateLimiter, middleware.OptionalAuth(jwtSvc), donationHandler.Checkout)
	r.POST("/webhooks/:gateway", donationHandler.Webhook)

	return r
}
