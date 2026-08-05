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
