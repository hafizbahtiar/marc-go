package http

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/email"
	"marc/internal/http/handlers"
	"marc/internal/http/middleware"
)

// trustedProxyRanges — Railway (dan PaaS lain macam dia) hantar request ke
// container melalui edge/proxy dalaman atas private network. Trust range
// RFC1918 standard supaya c.ClientIP() betul-betul baca X-Forwarded-For
// (untuk logging/rate-limit yang tepat), tapi tak trust SEMUA proxy
// (default Gin yang insecure — client boleh spoof X-Forwarded-For terus).
var trustedProxyRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

func NewRouter(
	pool *pgxpool.Pool,
	jwtSvc *auth.JWT,
	refreshTTL time.Duration,
	emailClient *email.Client,
	publicBaseURL string,
) *gin.Engine {
	r := gin.Default()
	if err := r.SetTrustedProxies(trustedProxyRanges); err != nil {
		log.Fatalf("set trusted proxies: %v", err)
	}

	r.GET("/healthz", handlers.Health)

	authHandler := handlers.NewAuthHandler(pool, jwtSvc, refreshTTL, emailClient, publicBaseURL)

	authGroup := r.Group("/auth")
	authGroup.POST("/register", authHandler.Register)
	authGroup.POST("/login", authHandler.Login)
	authGroup.POST("/refresh", authHandler.Refresh)
	authGroup.POST("/logout", authHandler.Logout)
	authGroup.POST("/verify-email/confirm", authHandler.ConfirmEmailVerification)
	authGroup.GET("/verify-email/confirm", authHandler.ConfirmEmailVerificationLink)

	protectedAuthGroup := r.Group("/auth", middleware.RequireAuth(jwtSvc))
	protectedAuthGroup.POST("/verify-email/request", authHandler.RequestEmailVerification)

	profileHandler := handlers.NewProfileHandler(pool)
	deviceTokenHandler := handlers.NewDeviceTokenHandler(pool)

	protected := r.Group("/", middleware.RequireAuth(jwtSvc))
	protected.GET("/me", profileHandler.Me)
	protected.PATCH("/me", profileHandler.UpdateMe)
	protected.GET("/members", profileHandler.Members)
	protected.POST("/device-tokens", deviceTokenHandler.Upsert)
	protected.DELETE("/device-tokens/:id", deviceTokenHandler.Delete)

	return r
}
