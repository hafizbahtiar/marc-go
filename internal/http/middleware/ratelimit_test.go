package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"marc/internal/redisclient"
)

func serve(t *testing.T, mw gin.HandlerFunc, ip string, n int) []int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	codes := make([]int, n)
	for i := range n {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":12345"
		r.ServeHTTP(rec, req)
		codes[i] = rec.Code
	}
	return codes
}

func countOK(codes []int) int {
	n := 0
	for _, c := range codes {
		if c == http.StatusOK {
			n++
		}
	}
	return n
}

// Tanpa Redis, middleware mesti tetap berfungsi guna baldi setempat —
// app boleh boot dan berjalan tanpa Redis dikonfigur.
func TestTanpaRedisGunaBaldiSetempat(t *testing.T) {
	rl := NewRateLimiter(nil)
	mw := rl.Limit("auth", rate.Every(time.Hour), 3)

	codes := serve(t, mw, "1.2.3.4", 5)
	if got := countOK(codes); got != 3 {
		t.Fatalf("%d permintaan lulus, mahu 3 (burst)", got)
	}
	if codes[3] != http.StatusTooManyRequests {
		t.Errorf("permintaan ke-4 = %d, mahu 429", codes[3])
	}
}

// Had di-skop per IP — satu penyalahguna tak boleh menyekat orang lain.
func TestBaldiBerasinganSetiapIP(t *testing.T) {
	rl := NewRateLimiter(nil)
	mw := rl.Limit("auth", rate.Every(time.Hour), 2)

	serve(t, mw, "9.9.9.9", 5) // habiskan kuota IP ni
	codes := serve(t, mw, "8.8.8.8", 2)
	if got := countOK(codes); got != 2 {
		t.Fatalf("IP berbeza dihalang: %d lulus, mahu 2", got)
	}
}

// Client Redis yang dimatikan sama macam tiada Redis langsung.
func TestClientRedisDimatikanJatuhBalikSetempat(t *testing.T) {
	c, err := redisclient.New("")
	if err != nil {
		t.Fatal(err)
	}
	rl := NewRateLimiter(c)
	if rl.redis != nil {
		t.Fatal("client dimatikan tak patut menghasilkan limiter Redis")
	}
	if got := countOK(serve(t, rl.Limit("x", rate.Every(time.Hour), 1), "5.5.5.5", 3)); got != 1 {
		t.Errorf("%d lulus, mahu 1", got)
	}
}

// Redis tak dapat dicapai MESTI gagal-terbuka kepada had setempat —
// Redis mati tak boleh mengunci ahli keluar daripada log masuk.
func TestRedisTakBolehDicapaiGagalTerbuka(t *testing.T) {
	// Port yang tiada apa-apa mendengar (bukan 6399 — itu Redis ujian).
	c, err := redisclient.New("redis://127.0.0.1:6301/0")
	if err != nil {
		t.Fatal(err)
	}
	rl := NewRateLimiter(c)

	codes := serve(t, rl.Limit("auth", rate.Every(time.Hour), 2), "7.7.7.7", 4)
	if got := countOK(codes); got != 2 {
		t.Fatalf("%d lulus, mahu 2 — patut jatuh balik ke baldi setempat, "+
			"bukan tolak semua (gagal-tertutup) atau benarkan semua", got)
	}
}

// ---- Lawan Redis sebenar (dilangkau melainkan REDIS_TEST_URL diset) ----
//
//	REDIS_TEST_URL="redis://localhost:6379/15" go test ./internal/http/middleware/ -v
//
// Skrip Lua ialah bahagian paling berisiko: ia kena atomik dan kena
// mengekalkan semantik isi-semula. Itu tak boleh disahkan tanpa Redis.

func redisRL(t *testing.T) *RateLimiter {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("set REDIS_TEST_URL untuk uji lawan Redis sebenar")
	}
	c, err := redisclient.New(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Skipf("Redis tak dapat dicapai: %v", err)
	}
	t.Cleanup(func() { _ = c.Redis().FlushDB(t.Context()).Err() })
	_ = c.Redis().FlushDB(t.Context()).Err()
	return NewRateLimiter(c)
}

func TestRedisKuatkuasakanBurst(t *testing.T) {
	rl := redisRL(t)
	codes := serve(t, rl.Limit("auth", rate.Every(time.Hour), 3), "10.0.0.1", 6)
	if got := countOK(codes); got != 3 {
		t.Fatalf("%d lulus, mahu 3", got)
	}
}

// Inti sebenar: DUA middleware berasingan (meniru dua instance app)
// mesti berkongsi baldi yang SAMA. Ini yang state setempat tak boleh buat.
func TestRedisDikongsiAntaraInstance(t *testing.T) {
	rl := redisRL(t)
	a := rl.Limit("auth", rate.Every(time.Hour), 3)
	b := rl.Limit("auth", rate.Every(time.Hour), 3)

	okA := countOK(serve(t, a, "10.0.0.2", 2))
	okB := countOK(serve(t, b, "10.0.0.2", 2))

	if okA+okB != 3 {
		t.Fatalf("jumlah lulus = %d (A=%d B=%d), mahu 3 — baldi tak dikongsi",
			okA+okB, okA, okB)
	}
}

// Nama berbeza = baldi berbeza. Tanpa ni, login yang dihabiskan akan
// turut menyekat upload.
func TestRedisNamaMengasingkanBaldi(t *testing.T) {
	rl := redisRL(t)
	auth := rl.Limit("auth", rate.Every(time.Hour), 1)
	upload := rl.Limit("upload", rate.Every(time.Hour), 1)

	serve(t, auth, "10.0.0.3", 3) // habiskan baldi auth
	if got := countOK(serve(t, upload, "10.0.0.3", 1)); got != 1 {
		t.Fatal("baldi upload terjejas oleh auth — nama tak mengasingkan")
	}
}

func TestRedisIsiSemulaIkutMasa(t *testing.T) {
	rl := redisRL(t)
	// 20 token sesaat -> satu token setiap 50ms.
	mw := rl.Limit("refill", rate.Limit(20), 1)

	if got := countOK(serve(t, mw, "10.0.0.4", 2)); got != 1 {
		t.Fatalf("burst awal salah: %d lulus, mahu 1", got)
	}
	time.Sleep(200 * time.Millisecond)
	if got := countOK(serve(t, mw, "10.0.0.4", 1)); got != 1 {
		t.Fatal("token tak diisi semula selepas masa berlalu")
	}
}
