package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// ipRateLimiter simpan satu token-bucket limiter per client IP. State
// dalam memory (bukan Redis) — cukup untuk single-instance deployment;
// kalau nanti scale ke banyak instance Go, pindah ke shared store (Redis)
// supaya had dikuatkuasakan across instance, bukan per-instance.
type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	burst    int
}

func newIPRateLimiter(r rate.Limit, burst int) *ipRateLimiter {
	rl := &ipRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		r:        r,
		burst:    burst,
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limiters[ip]
	if !exists {
		limiter = rate.NewLimiter(rl.r, rl.burst)
		rl.limiters[ip] = limiter
	}
	return limiter
}

// cleanupLoop buang entry lama-lama supaya map ni tak membesar tak
// terhingga — setiap IP unik yang pernah hit route ni akan tinggal
// selama-lamanya kalau tak dibersih. Limiter yang tokennya dah penuh
// semula (tak dipakai sekejap) selamat dibuang, dicipta balik bila IP
// tu request lagi.
func (rl *ipRateLimiter) cleanupLoop() {
	for {
		time.Sleep(10 * time.Minute)
		now := time.Now()
		rl.mu.Lock()
		for ip, limiter := range rl.limiters {
			if limiter.TokensAt(now) >= float64(rl.burst) {
				delete(rl.limiters, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit bina middleware Gin. r = kadar request dibenarkan (per saat),
// burst = berapa banyak boleh terkumpul serta-merta sebelum ditolak.
// Dipakai untuk route sensitif (login/register) — elak brute-force/spam.
func RateLimit(r rate.Limit, burst int) gin.HandlerFunc {
	limiter := newIPRateLimiter(r, burst)
	return func(c *gin.Context) {
		if !limiter.getLimiter(c.ClientIP()).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "terlalu banyak percubaan, cuba lagi sebentar",
			})
			return
		}
		c.Next()
	}
}
