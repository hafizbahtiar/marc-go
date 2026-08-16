package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

	"marc/internal/redisclient"
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

// RateLimit bina middleware Gin dengan state SETEMPAT (per-instance).
// r = kadar request dibenarkan (per saat), burst = berapa banyak boleh
// terkumpul serta-merta sebelum ditolak.
//
// Guna RateLimiter.Limit kalau ada Redis — had setempat jadi N kali lebih
// longgar bila ada N replika.
func RateLimit(r rate.Limit, burst int) gin.HandlerFunc {
	return (&RateLimiter{}).Limit("", r, burst)
}

// tokenBucketScript — token bucket teragih dalam Lua.
//
// Lua (bukan INCR/EXPIRE) sebab dua sebab. Pertama, baca-kira-tulis mesti
// atomik; dua instance yang menyemak serentak dengan MULTI biasa boleh
// dua-dua lulus. Kedua, ia mengekalkan semantik `rate.Limiter` yang sama
// (isi semula berterusan + burst) — kaunter tetingkap-tetap akan
// membenarkan 2x had di sempadan tetingkap.
const tokenBucketScript = `
local key   = KEYS[1]
local rate  = tonumber(ARGV[1])   -- token sesaat
local burst = tonumber(ARGV[2])
local now   = tonumber(ARGV[3])   -- milisaat

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil then
  tokens = burst
  ts = now
end

-- Isi semula ikut masa berlalu, dihadkan pada kapasiti baldi.
local elapsed = math.max(0, now - ts) / 1000.0
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- Luput selepas baldi sepatutnya penuh semula; kunci melahu tak kekal.
redis.call('EXPIRE', key, math.ceil(burst / rate) + 10)
return allowed
`

// RateLimiter bina middleware had kadar yang dikongsi antara instance
// bila Redis ada, dan jatuh balik kepada state setempat bila tidak.
type RateLimiter struct {
	redis  *redis.Client
	script *redis.Script
}

// NewRateLimiter — `client` boleh nil/dimatikan; middleware yang terhasil
// akan guna state setempat.
func NewRateLimiter(client *redisclient.Client) *RateLimiter {
	if client == nil || !client.Enabled() {
		return &RateLimiter{}
	}
	return &RateLimiter{
		redis:  client.Redis(),
		script: redis.NewScript(tokenBucketScript),
	}
}

// Limit bina middleware. `name` mengasingkan baldi antara had yang
// berbeza — tanpa ia, login/upload/donation akan berkongsi baldi yang
// SAMA dalam Redis dan saling menghabiskan kuota masing-masing. (Versi
// setempat tak ada masalah ni sebab setiap panggilan cipta map sendiri.)
func (rl *RateLimiter) Limit(name string, r rate.Limit, burst int) gin.HandlerFunc {
	local := newIPRateLimiter(r, burst)

	return func(c *gin.Context) {
		if !rl.allow(c, name, c.ClientIP(), r, burst, local) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "terlalu banyak percubaan, cuba lagi sebentar",
			})
			return
		}
		c.Next()
	}
}

func (rl *RateLimiter) allow(
	c *gin.Context,
	name, ip string,
	r rate.Limit,
	burst int,
	local *ipRateLimiter,
) bool {
	if rl.redis == nil {
		return local.getLimiter(ip).Allow()
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 200*time.Millisecond)
	defer cancel()

	key := fmt.Sprintf("ratelimit:%s:%s", name, ip)
	res, err := rl.script.Run(ctx, rl.redis, []string{key},
		float64(r), burst, time.Now().UnixMilli()).Int()
	if err != nil {
		// GAGAL-TERBUKA kepada had setempat, bukan gagal-tertutup.
		// Redis yang mati tak boleh mengunci ahli keluar daripada log
		// masuk — tapi "terbuka" di sini bermakna jatuh balik kepada
		// baldi per-instance, bukan membenarkan segalanya.
		log.Printf("rate limit: redis gagal, guna had setempat: %v", err)
		return local.getLimiter(ip).Allow()
	}
	return res == 1
}
