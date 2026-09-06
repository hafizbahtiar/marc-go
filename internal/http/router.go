package http

import (
	"log"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	tgbot "github.com/go-telegram/bot"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/handlers"
	"marc/internal/http/middleware"
	"marc/internal/payment"
	"marc/internal/paymentreconcile"
	"marc/internal/push"
	"marc/internal/redisclient"
	"marc/internal/storage"
)

// trustedProxyRanges - Railway hantar request ke container melalui proxy
// dalaman atas "Railway Private Network", yang guna CGNAT range
// 100.64.0.0/10 (bukan RFC1918 biasa) - verified terus daripada log
// produksi (c.ClientIP() asalnya papar 100.64.x.x sebagai client, bukan
// IP awam sebenar, sebelum range ni ditambah). RFC1918 standard turut
// disertakan untuk keserasian persekitaran lain (Docker/VPC dalaman).
var trustedProxyRanges = []string{
	"100.64.0.0/10",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// authRateLimit - 5 percubaan seminit setiap IP (burst 5) untuk route
// paling sensitif kepada brute-force/spam (login, register). Angka ni
// baseline biasa; ketat lagi kalau mula nampak abuse.
var authRateLimit = rate.Every(12 * time.Second)

const authRateBurst = 5

// authSessionRateLimit - baldi berasingan drpd 'auth' untuk /refresh dan
// /logout (L26). Kedua-dua route ni ialah penyelenggaraan sesi rutin
// (dipanggil automatik oleh app bila token nak luput), BUKAN permukaan
// terdedah kepada brute-force macam login/register - jadi tak patut
// kongsi kuota ketat 12s/5 dengan route sensitif tu. Senario IP dikongsi
// (wifi kelab, CGNAT) boleh sebabkan ramai ahli refresh serentak
// menghabiskan baldi 'auth' yang sama, lalu login/register sah turut
// disekat. Lebih longgar drpd 'auth' tapi masih terhad.
var authSessionRateLimit = rate.Every(3 * time.Second)

const authSessionRateBurst = 10

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
	registrationFeeCents int,
	gatewayChargeCents int,
	registrationBillExpiryMinutes int,
	redisCli *redisclient.Client,
	paymentReconciler *paymentreconcile.Reconciler,
	corsAllowedOrigins []string,
	registrationPaymentReturnURL string,
	activityPaymentReturnURL string,
	certificateVerifyURL string,
	passwordResetURL string,
	telegramHandler *handlers.TelegramHandler,
	tgBot *tgbot.Bot,
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestLogger(logger), middleware.MaxBodySize(1<<20))
	if err := r.SetTrustedProxies(trustedProxyRanges); err != nil {
		log.Fatalf("set trusted proxies: %v", err)
	}

	r.GET("/healthz", handlers.Health)

	authHandler := handlers.NewAuthHandler(pool, jwtSvc, refreshTTL, emailClient, publicBaseURL, emailVerifyURL, passwordResetURL)
	legacyMemberImportHandler := handlers.NewLegacyMemberImportHandler(pool, emailClient, publicBaseURL)

	// Satu factory, had bernama. Nama MESTI unik - dalam Redis ia
	// yang mengasingkan baldi; tanpa itu login dan upload berkongsi kuota.
	rateLimiter := middleware.NewRateLimiter(redisCli)
	authRateLimiter := rateLimiter.Limit("auth", authRateLimit, authRateBurst)
	authSessionRateLimiter := rateLimiter.Limit("auth-session", authSessionRateLimit, authSessionRateBurst)
	// Baldi BERASINGAN daripada 'auth' (pengajaran L26): trafik reset tak
	// patut menghabiskan kuota log masuk ahli, dan sebaliknya. Seketat
	// 'auth' sebab setiap permintaan yang berjaya mencetuskan penghantaran
	// emel.
	passwordResetRateLimiter := rateLimiter.Limit("password-reset", authRateLimit, authRateBurst)
	// Baldi berasingan drpd login/register - resend emel pengesahan
	// sah, tak patut habiskan kuota auth. Had ketat per-akaun (60s +
	// 5/24jam) duduk dalam handler; ni cuma elak flood dari satu IP.
	verifyEmailRequestRateLimiter := rateLimiter.Limit("verify-email-request", rate.Every(10*time.Second), 3)

	authGroup := r.Group("/auth")
	authGroup.POST("/register", authRateLimiter, authHandler.Register)
	authGroup.POST("/login", authRateLimiter, authHandler.Login)
	authGroup.POST("/refresh", authSessionRateLimiter, authHandler.Refresh)
	authGroup.POST("/logout", authSessionRateLimiter, authHandler.Logout)
	// CORS dipasang khusus di sini - satu-satunya laluan awam dipanggil
	// via fetch() dari laman web (marc.hafizbahtiar.com), bukan navigasi
	// pelayar penuh. Lihat internal/http/middleware/cors.go.
	verifyEmailCORS := middleware.CORS(corsAllowedOrigins, "POST, OPTIONS")
	authGroup.POST("/verify-email/confirm", verifyEmailCORS, authRateLimiter, authHandler.ConfirmEmailVerification)
	authGroup.OPTIONS("/verify-email/confirm", verifyEmailCORS)
	authGroup.GET("/verify-email/confirm", authRateLimiter, authHandler.ConfirmEmailVerificationLink)
	authGroup.POST("/password-reset/request", passwordResetRateLimiter, authHandler.RequestPasswordReset)
	// CORS + OPTIONS: laluan ni dipanggil oleh halaman Astro melalui
	// fetch() silang-origin, sama seperti verify-email/confirm. Instance
	// BERASINGAN drpd verifyEmailCORS walaupun konfigurasinya sama -
	// menamakannya ikut laluan yang ia lindungi menjadikan niat boleh
	// dibaca, dan kedua-duanya bebas berubah kemudian.
	passwordResetCORS := middleware.CORS(corsAllowedOrigins, "POST, OPTIONS")
	authGroup.POST("/password-reset/confirm", passwordResetCORS, passwordResetRateLimiter, authHandler.ConfirmPasswordReset)
	authGroup.OPTIONS("/password-reset/confirm", passwordResetCORS)
	authGroup.POST("/legacy-member-claim/request", authRateLimiter, legacyMemberImportHandler.RequestClaim)
	authGroup.POST("/legacy-member-claim/complete", authRateLimiter, legacyMemberImportHandler.CompleteClaim)

	queries := sqlc.New(pool)
	requireAuth := middleware.RequireAuth(jwtSvc, queries)

	protectedAuthGroup := r.Group("/auth", requireAuth, middleware.RequireApprovedStatus(queries))
	protectedAuthGroup.POST("/verify-email/request", verifyEmailRequestRateLimiter, authHandler.RequestEmailVerification)

	profileHandler := handlers.NewProfileHandler(pool, emailClient, r2Client, registrationFeeCents, paymentGateways["toyyibpay"])
	deviceTokenHandler := handlers.NewDeviceTokenHandler(pool)

	// profileUpdateRateLimiter (L25) - /me tak ada mekanisme dedup macam
	// like (setiap PATCH memang tindakan sah, bukan spam berulang secara
	// semulajadi), jadi had kadar diletak pada route, bukan handler.
	// 3s/10 - guna harian biasa (edit profil) tak kerap sangat, tapi
	// longgar drpd 'auth'/'donation' sebab bukan permukaan sensitif.
	profileUpdateRateLimiter := rateLimiter.Limit("profile-update", rate.Every(3*time.Second), 10)

	// accountDeletionRateLimiter - padanan pola profileUpdateRateLimiter.
	// Bukan permukaan spam-sensitif (satu ahli, satu permintaan idempoten),
	// tapi tetap dihad sikit sebab ia tulis catatan audit setiap panggilan
	// yang berjaya cipta baris baharu.
	accountDeletionRateLimiter := rateLimiter.Limit("account-deletion-request", rate.Every(3*time.Second), 10)

	protected := r.Group("/", requireAuth)
	protected.GET("/me", profileHandler.Me)
	protected.PATCH("/me", profileUpdateRateLimiter, profileHandler.UpdateMe)
	protected.POST("/me/deletion-request", accountDeletionRateLimiter, profileHandler.RequestAccountDeletion)
	protected.POST("/auth/logout-all", authHandler.LogoutAll)

	// Sesi aktif (self-service) - sama gate `protected` dgn /me di atas.
	// Adik-beradik satu-baris kepada /auth/logout-all: senarai device yang
	// log masuk + "log keluar device ni" sahaja.
	protected.GET("/me/sessions", authHandler.ListMySessions)
	protected.POST("/me/sessions/revoke", profileUpdateRateLimiter, authHandler.RevokeMySessions)
	protected.DELETE("/me/sessions/:id", profileUpdateRateLimiter, authHandler.RevokeMySession)

	// Alamat ahli (self-service) - sama gate `protected` dgn /me di atas
	// (RequireAuth sahaja, tiada RequireApprovedStatus - padanan alasan
	// yang sama: data peribadi profil, bukan aktiviti kelab).
	protected.GET("/me/addresses", profileHandler.ListMyAddresses)
	protected.POST("/me/addresses", profileUpdateRateLimiter, profileHandler.CreateAddress)
	protected.PATCH("/me/addresses/:id", profileUpdateRateLimiter, profileHandler.UpdateAddress)
	protected.DELETE("/me/addresses/:id", profileUpdateRateLimiter, profileHandler.DeleteAddress)

	// Baldi berasingan drpd 'auth'/'password-reset' (pengajaran L26):
	// trafik binding Telegram tak patut kongsi kuota dgn laluan lain.
	telegramLinkRateLimiter := rateLimiter.Limit("telegram-link", authRateLimit, authRateBurst)
	protected.POST("/me/telegram-link/token", telegramLinkRateLimiter, telegramHandler.RequestLinkToken)
	protected.DELETE("/me/telegram-link", telegramHandler.DeleteLink)

	// Webhook Telegram -- AWAM, dipanggil Telegram (bukan app). tgBot
	// nil bila TELEGRAM_BOT_TOKEN kosong ATAU bot.New() gagal (token
	// tak sah) -- route tak didaftar langsung, bukan 503 runtime,
	// padanan corak lain dlm sistem ni (route hilang, bukan handler
	// yg semak config setiap panggilan).
	if tgBot != nil {
		r.POST("/webhooks/telegram", gin.WrapF(tgBot.WebhookHandler()))
	}

	// approved (Stage 11) - /members, /device-tokens, dan approve/reject
	// sendiri perlu status=approved. /me sengaja TAK di sini (lihat
	// RequireApprovedStatus).
	approved := r.Group("/", requireAuth, middleware.RequireApprovedStatus(queries))
	approved.GET("/members", profileHandler.Members)
	// Profil SATU ahli (view-only, tiered) - laluan dua segmen, tiada
	// pertembungan dgn /members (satu segmen) atau tindakan tiga segmen
	// di bawah (Gin padan ikut kedalaman laluan penuh, bukan awalan).
	approved.GET("/members/:id", profileHandler.GetMemberDetail)
	approved.POST("/members/:id/approve", profileHandler.ApproveMember)
	approved.POST("/members/:id/reject", profileHandler.RejectMember)
	approved.POST("/members/:id/verify-staff-id", profileHandler.VerifyStaffID)
	approved.PATCH("/members/:id/staff-id", profileHandler.CorrectStaffID)
	approved.PATCH("/members/:id/member-id", profileHandler.CorrectMemberID)
	approved.POST("/members/:id/cancel-registration-payment", profileHandler.CancelMemberRegistrationPayment)
	approved.PATCH("/members/:id/role", profileHandler.UpdateMemberRole)
	// Status AKTIF/TAK AKTIF keahlian - BERASINGAN drpd approve/reject
	// (status kelulusan). Gate hierarki rank sama pola /role di atas.
	approved.PATCH("/members/:id/active", profileHandler.UpdateMemberActive)
	// Bahagian/jawatan ahli - manager KE ATAS, gate rank SETARAF DAN KE
	// BAWAH (bukan strictly lebih tinggi macam /role & /active di atas -
	// lihat komen UpdateMemberDepartment).
	approved.PATCH("/members/:id/department", profileHandler.UpdateMemberDepartment)

	// Jejak audit (management sahaja, dikuatkuasakan dalam handler).
	approved.GET("/audit-logs", handlers.NewAuditHandler(pool).List)

	// Domain emel pelupusan disekat (pelengkap senarai statik terbenam
	// internal/disposableemail - SUPERADMIN sahaja, dikuatkuasakan dalam
	// handler, bukan management umum: jadual ni kawal siapa boleh
	// DAFTAR langsung, root-level config). Diletak `approved` konsisten
	// dengan corak /audit-logs (route group longgar, handler yg
	// kuatkuasakan siling sebenar).
	blockedEmailDomainsHandler := handlers.NewBlockedEmailDomainsHandler(pool)
	approved.GET("/admin/blocked-email-domains", blockedEmailDomainsHandler.List)
	approved.POST("/admin/blocked-email-domains", blockedEmailDomainsHandler.Create)
	approved.DELETE("/admin/blocked-email-domains/:domain", blockedEmailDomainsHandler.Delete)

	// Rujukan bahagian/jabatan organisasi - superadmin SAHAJA, sama gate
	// & rasional dgn blockedEmailDomainsHandler di atas.
	departmentsHandler := handlers.NewDepartmentsHandler(pool)
	approved.GET("/admin/departments", departmentsHandler.List)
	approved.POST("/admin/departments", departmentsHandler.Create)
	approved.PATCH("/admin/departments/:code", departmentsHandler.Update)
	approved.DELETE("/admin/departments/:code", departmentsHandler.Delete)
	accountLifecycleHandler := handlers.NewAccountLifecycleHandler(pool)
	approved.GET("/admin/account-deletion-requests", accountLifecycleHandler.List)
	approved.POST("/admin/account-deletion-requests/:id/execute", accountLifecycleHandler.Execute)
	approved.POST("/admin/legacy-member-import/dry-run", legacyMemberImportHandler.DryRun)
	approved.GET("/admin/legacy-member-import/batches", legacyMemberImportHandler.ListBatches)
	approved.GET("/admin/legacy-member-import/:id", legacyMemberImportHandler.GetBatch)
	approved.POST("/admin/legacy-member-import/:id/import", legacyMemberImportHandler.Import)
	// Baca-sahaja, manager ke atas - pemilih bahagian utk
	// PATCH /members/:id/department (bukan skrin CRUD superadmin di atas).
	approved.GET("/departments", departmentsHandler.ListForAssignment)

	// Pencetus manual internal/paymentreconcile (management sahaja,
	// dikuatkuasakan dalam handler) - padanan pola /audit-logs di atas.
	// Sama logik dengan sapuan latar berkala (cmd/api/main.go), cuma
	// dicetuskan on-demand.
	approved.POST("/admin/payments/reconcile", handlers.NewPaymentReconcileHandler(paymentReconciler, sqlc.New(pool)).Run)

	// Sejarah bayaran (bacaan sahaja) - lihat internal/http/handlers/payments.go.
	paymentsHandler := handlers.NewPaymentsHandler(pool, gatewayChargeCents)
	// `protected` (bukan `approved`) SENGAJA - checkout yuran pendaftaran
	// sendiri duduk atas `protected` (baris /registration-payments/checkout
	// di bawah), jadi ahli `pending` yang DAH bayar mesti boleh tengok
	// sejarah bayaran sendiri, bukan dapat 403 sebelum status approved
	// (Opus verify 2026-08-15 tangkap gap ni).
	protected.GET("/me/payments", paymentsHandler.Mine)
	approved.GET("/admin/payments", paymentsHandler.ListAll)

	// Resit PDF - sama gate `protected` dgn /me/payments di atas (ahli
	// pending yang dah bayar mesti boleh muat turun resit sendiri). Had
	// kadar padan `uploadRateLimiter` (PutObject R2 setiap panggilan,
	// bukan sekadar bacaan DB) - elak abuse jana+muat naik berulang.
	receiptRateLimiter := rateLimiter.Limit("payment-receipt", rate.Every(6*time.Second), 5)
	protected.GET("/me/payments/registration/:id/receipt", receiptRateLimiter, paymentsHandler.RegistrationReceipt)
	protected.GET("/me/payments/activity/:id/receipt", receiptRateLimiter, paymentsHandler.ActivityReceipt)
	protected.GET("/me/payments/donation/:id/receipt", receiptRateLimiter, paymentsHandler.DonationReceipt)

	// Bacaan agregat skrin Utama app - kumpulan `approved` (bukan
	// `verified`): mengira notifikasi sendiri tidak mendedahkan
	// kandungan, dan client memapar skrin "sahkan emel" sebelum sempat
	// memanggil endpoint ini.
	approved.GET("/dashboard", handlers.NewDashboardHandler(pool, registrationFeeCents).Get)

	approved.GET("/roles", profileHandler.ListRoles)
	approved.POST("/device-tokens", deviceTokenHandler.Upsert)
	approved.DELETE("/device-tokens/:id", deviceTokenHandler.Delete)
	approved.DELETE("/device-tokens/by-onesignal/:onesignalId", deviceTokenHandler.DeleteByOnesignalID)

	// Posts (Stage 10) - perlu email_verified, lapisan tambahan atas
	// RequireAuth. Payment gate akan ditambah dalam middleware ni bila
	// payment system siap (bukan sekarang).
	postHandler := handlers.NewPostHandler(pool, r2Client, pushSvc)
	commentHandler := handlers.NewCommentHandler(pool, pushSvc, r2Client)
	notificationHandler := handlers.NewNotificationHandler(pool)
	uploadHandler := handlers.NewUploadHandler(pool, r2Client)

	// postCreateRateLimiter / commentCreateRateLimiter (L25) - like/unlike
	// dielak spam via dedup dalam handler (tiada row baru = tiada notify);
	// post dan comment TIADA konsep "dah wujud" macam tu - setiap create
	// MEMANG row baru, jadi had kadar kena diletak pada route. 3s/10 -
	// tindakan biasa harian (bukan jarang macam donation/upload) tapi
	// tetap terhad supaya tak jadi gelung spam notifikasi/push.
	postCreateRateLimiter := rateLimiter.Limit("post-create", rate.Every(3*time.Second), 10)
	commentCreateRateLimiter := rateLimiter.Limit("comment-create", rate.Every(3*time.Second), 10)

	verified := r.Group("/", requireAuth, middleware.RequireApprovedStatus(queries), middleware.RequireVerifiedEmail(queries))
	verified.GET("/posts", postHandler.List)
	verified.POST("/posts", postCreateRateLimiter, postHandler.Create)
	verified.GET("/posts/:id", postHandler.Get)
	verified.PATCH("/posts/:id", postHandler.Update)
	verified.DELETE("/posts/:id", postHandler.Delete)
	verified.POST("/posts/:id/like", postHandler.Like)
	verified.DELETE("/posts/:id/like", postHandler.Unlike)

	verified.GET("/posts/:id/comments", commentHandler.List)
	verified.POST("/posts/:id/comments", commentCreateRateLimiter, commentHandler.Create)
	verified.PATCH("/comments/:id", commentHandler.Update)
	verified.DELETE("/comments/:id", commentHandler.Delete)
	verified.POST("/comments/:id/like", commentHandler.Like)
	verified.DELETE("/comments/:id/like", commentHandler.Unlike)

	// Aktiviti - baca untuk sesiapa yang approved, tulis perlu email
	// disahkan. Semakan "pengurusan sahaja" dibuat dalam handler
	// (authz.IsManagement), sama seperti audit log dan profil.
	activityHandler := handlers.NewActivityHandler(pool, pushSvc)

	approved.GET("/activity-categories", activityHandler.ListCategories)
	verified.POST("/activity-categories", activityHandler.CreateCategory)
	verified.PATCH("/activity-categories/:id", activityHandler.UpdateCategory)
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
	// dalam handler) - kehadiran ialah bukti yang menentukan siapa menerima
	// sijil, jadi ia tidak boleh ditanda oleh penerimanya sendiri.
	attendanceHandler := handlers.NewAttendanceHandler(pool)

	verified.POST("/activities/:id/sessions/:sid/attendance", attendanceHandler.Mark)
	verified.DELETE("/activities/:id/sessions/:sid/attendance/:rid", attendanceHandler.Unmark)

	// Sijil. Terbit/tarik-balik ialah pengurusan (dikuatkuasakan dalam
	// handler) dan duduk atas `verified`; membaca sijil SENDIRI cukup
	// dengan `approved` - sama garisan seperti /me/activities.
	// certificateVerifyURL optional - lihat komen CertificateVerifyURL,
	// config.go. Kosong = tiada perubahan tingkah laku sedia ada (QR
	// terus ke laluan JSON awam Go, publicBaseURL + /verify/certificates/:token).
	certificateHandler := handlers.NewCertificateHandler(pool, r2Client, pushSvc, publicBaseURL, certificateVerifyURL)
	certificateTemplateHandler := handlers.NewCertificateTemplateHandler(pool)

	verified.POST("/activities/:id/certificates", certificateHandler.Issue)
	verified.POST("/certificates/:id/revoke", certificateHandler.Revoke)
	verified.GET("/admin/certificate-templates", certificateTemplateHandler.List)
	verified.GET("/admin/certificate-templates/:id", certificateTemplateHandler.Get)
	verified.PATCH("/admin/certificate-templates/:id", certificateTemplateHandler.Update)
	verified.POST("/admin/certificate-templates/:id/publish", certificateTemplateHandler.Publish)

	approved.GET("/me/certificates", certificateHandler.ListMine)
	approved.GET("/me/certificates/:id/file", certificateHandler.Download)

	// Pengesahan sijil - AWAM, atas router akar dan bukan `approved`: kod QR
	// pada sijil bercetak discan oleh majikan yang tiada akaun MARC.
	//
	// Baldi had kadar DINAMAKAN. Baldi tanpa nama berkongsi kunci Redis
	// dengan 'auth' dan 'upload', jadi trafik pengesahan awam akan
	// menghabiskan kuota log masuk ahli.
	// Laluan dan nama baldi ialah pemalar dieksport dalam `handlers` supaya
	// ia tak boleh terpesong daripada rujukan lain (lihat komennya di sana).
	verifyRateLimiter := rateLimiter.Limit(handlers.VerifyRateLimitBucket, rate.Every(2*time.Second), 20)
	// CORS - kalau QR dibakar ke halaman Astro (CertificateVerifyBaseURL
	// diisi), Astro fetch() laluan JSON ni cross-origin. GET simple
	// request tak trigger preflight OPTIONS, tapi respons masih perlu
	// header Access-Control-Allow-Origin untuk JS baca. Lihat cors.go.
	certVerifyCORS := middleware.CORS(corsAllowedOrigins, "GET, OPTIONS")
	r.GET(handlers.VerifyCertificateRoute, certVerifyCORS, verifyRateLimiter, certificateHandler.Verify)

	uploadRateLimiter := rateLimiter.Limit("upload", rate.Every(6*time.Second), 5)
	verified.POST("/uploads/presign", uploadRateLimiter, uploadHandler.Presign)

	verified.GET("/notifications", notificationHandler.List)
	verified.POST("/notifications/:id/read", notificationHandler.MarkRead)
	verified.POST("/notifications/read-all", notificationHandler.MarkAllRead)
	verified.DELETE("/notifications/read", notificationHandler.DeleteRead)
	verified.DELETE("/notifications/selected", notificationHandler.DeleteSelected)
	verified.DELETE("/notifications/:id", notificationHandler.Delete)

	// Donation (Stage 12, Stripe sahaja buat masa ni - SociaBuzz/threshold
	// routing belum wired) - route AWAM sengaja, guna OptionalAuth supaya
	// ahli MARC yang log masuk dikaitkan user_id tanpa wajibkan akaun
	// untuk donate. Handler bergantung payment.Gateway (interface), bukan
	// Stripe terus - tambah gateway baru = daftar dlm paymentGateways
	// (cmd/api/main.go), tiada perubahan di sini.
	donationHandler := handlers.NewDonationHandler(pool, paymentGateways, emailClient)
	donationRateLimiter := rateLimiter.Limit("donation", rate.Every(6*time.Second), 5)
	r.POST("/donations/checkout", donationRateLimiter, middleware.OptionalAuth(jwtSvc, queries), middleware.BlockTesterWrites(sqlc.New(pool)), donationHandler.Checkout)
	r.POST("/webhooks/:gateway", donationHandler.Webhook)
	paymentStatusHandler := handlers.NewPaymentStatusHandler(paymentGateways)
	paymentStatusRateLimiter := rateLimiter.Limit("payment-status", rate.Every(3*time.Second), 10)
	paymentStatusCORS := middleware.CORS(corsAllowedOrigins, "GET, OPTIONS")
	r.GET("/payment-status/:gateway/:reference", paymentStatusCORS, paymentStatusRateLimiter, paymentStatusHandler.Check)

	// Yuran pendaftaran ahli (Stage 12, ToyyibPay, SEKALI BAYAR - bukan
	// dues berulang, bukan yuran aktiviti). Checkout duduk atas `protected`
	// (RequireAuth SAHAJA, sama group dengan /me) sengaja: ahli `pending`
	// yang belum diluluskan MESTI boleh bayar semasa menunggu - kalau
	// diletak atas `approved` mereka takkan sampai ke sini langsung.
	// Webhook AWAM, gateway dihardcode "toyyibpay" (satu-satunya gateway
	// ciri ni guna) - lihat komen RegistrationPaymentHandler.Webhook.
	registrationPaymentHandler := handlers.NewRegistrationPaymentHandler(pool, paymentGateways["toyyibpay"], registrationFeeCents, gatewayChargeCents, registrationBillExpiryMinutes, emailClient)
	// Had kadar (Opus verify 2026-08-15 tandakan MEDIUM tanpanya): checkout
	// padan bucket `donation` (sama corak - tindakan pembayaran sengaja,
	// jarang berulang secara sah). Webhook padan `verifyRateLimiter`
	// (2s/20 burst, bukan 6s/5 macam checkout) sebab ToyyibPay sendiri
	// yang panggil endpoint ni - VerifyWebhook buat outbound HTTP poll
	// (getBillTransactions, 15s timeout) setiap panggilan, jadi endpoint
	// ni lebih mahal per-request drpd webhook Stripe (sah HMAC tempatan
	// sahaja) dan lebih terdedah kepada amplification tanpa had.
	registrationCheckoutRateLimiter := rateLimiter.Limit("registration-payment-checkout", rate.Every(6*time.Second), 5)
	registrationWebhookRateLimiter := rateLimiter.Limit("registration-payment-webhook", rate.Every(2*time.Second), 20)
	protected.POST("/registration-payments/checkout", registrationCheckoutRateLimiter, middleware.BlockTesterWrites(sqlc.New(pool)), registrationPaymentHandler.Checkout)
	r.POST("/registration-payments/webhook/toyyibpay", registrationWebhookRateLimiter, registrationPaymentHandler.Webhook)
	r.GET("/registration-payments/return/toyyibpay", redirectIfConfigured(registrationPaymentReturnURL), registrationPaymentHandler.ReturnPage)

	// Config checkout GENERIK (bukan spesifik satu modul) - client (mana-
	// mana skrin checkout) papar breakdown invoice "Yuran"+"Caj
	// Pemprosesan"="Jumlah". `protected` (RequireAuth sahaja) sama sebab
	// sama di atas - ahli pending checkout yuran pendaftaran perlu nilai
	// ni juga. Tiada rate limiter khusus: bacaan statik, tiada kerja DB.
	paymentConfigHandler := handlers.NewPaymentConfigHandler(gatewayChargeCents)
	protected.GET("/payment-config", paymentConfigHandler.Get)

	// Yuran AKTIVITI (activities.fee_cents) - berasingan konseptual drpd
	// yuran pendaftaran ahli di atas (padanan ActivityRegistrationPaymentHandler
	// vs RegistrationPaymentHandler, jangan keliru dua-dua).
	//
	// Gateway "toyyibpay-activity" (bukan "toyyibpay") sengaja: instance
	// ToyyibPayGateway bakar callbackURL/returnURL TETAP semasa dibina
	// (lihat NewToyyibPayGateway, cmd/api/main.go) - bill yang dicipta guna
	// instance "toyyibpay" akan sentiasa callback ke
	// /registration-payments/webhook/toyyibpay, tak kira apa route yang
	// kita daftarkan di sini. Instance KEDUA (kredential SAMA, URL callback/
	// return BERBEZA) ialah satu-satunya cara ToyyibPay benar-benar panggil
	// balik /activity-registrations/webhook/toyyibpay tanpa menyentuh
	// toyyibpay.go atau registration_payment.go (dua-dua di luar skop).
	activityRegistrationPaymentHandler := handlers.NewActivityRegistrationPaymentHandler(pool, paymentGateways["toyyibpay-activity"], gatewayChargeCents, emailClient)
	activityPaymentCheckoutRateLimiter := rateLimiter.Limit("activity-payment-checkout", rate.Every(6*time.Second), 5)
	verified.POST("/activities/:id/registration/checkout", activityPaymentCheckoutRateLimiter, middleware.BlockTesterWrites(sqlc.New(pool)), activityRegistrationPaymentHandler.Checkout)
	r.POST("/activity-registrations/webhook/toyyibpay", registrationWebhookRateLimiter, activityRegistrationPaymentHandler.Webhook)
	r.GET("/activity-registrations/return/toyyibpay", redirectIfConfigured(activityPaymentReturnURL), activityRegistrationPaymentHandler.ReturnPage)

	return r
}

// redirectIfConfigured - padanan pola EmailVerifyURL: kalau `targetURL`
// diisi (Stage 8 lanjutan, portfolio-astro), 302 ke sana dgn query
// string ToyyibPay (status_id, billcode, dll) dikekalkan supaya halaman
// Astro boleh papar mesej ikut status. Kalau kosong, biar handler Go
// sendiri (ReturnPage) yang jawab macam sebelum ni - tiada perubahan
// tingkah laku sedia ada.
func redirectIfConfigured(targetURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if targetURL == "" {
			c.Next()
			return
		}
		dest := targetURL
		if c.Request.URL.RawQuery != "" {
			dest += "?" + c.Request.URL.RawQuery
		}
		c.Redirect(302, dest)
		c.Abort()
	}
}
