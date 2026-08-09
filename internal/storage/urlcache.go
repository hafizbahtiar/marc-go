package storage

import (
	"context"
	"sync"
	"time"
)

// URLCache simpan URL yang dah ditandatangani supaya permintaan berulang
// untuk objek yang SAMA mendapat rentetan URL yang SAMA.
//
// Ini bukan pengoptimuman prestasi — menandatangani ialah HMAC setempat,
// murah. Ia untuk **kestabilan URL**.
//
// Presigned URL mengandungi `X-Amz-Date`. Menandatangani semula pada
// setiap permintaan menghasilkan URL berbeza setiap kali, dan cache imej
// pada peranti dikunci ikut URL — jadi setiap tatalan feed akan memuat
// turun semula setiap gambar yang sama. Itu memusnahkan cache klien DAN
// menghentam bucket yang dikadar-hadkan.
//
// Dengan cache: semua permintaan dalam satu tetingkap dapat URL yang
// sama, jadi cache cakera klien berkena.
type URLCache interface {
	Get(ctx context.Context, key string) (string, bool)
	Set(ctx context.Context, key, url string, ttl time.Duration)
}

type memoryURLCache struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	url       string
	expiresAt time.Time
}

// NewMemoryURLCache — cache setempat per-instance.
//
// Cukup untuk satu instance. Dengan berbilang replika, setiap instance
// menandatangani URLnya sendiri, jadi klien yang mencapai instance
// berlainan akan dapat URL berlainan dan terlepas cache. Guna cache
// bersandar-Redis untuk mengelak itu.
func NewMemoryURLCache() URLCache {
	c := &memoryURLCache{entries: make(map[string]memoryEntry)}
	go c.sweep()
	return c
}

func (c *memoryURLCache) Get(_ context.Context, key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.url, true
}

func (c *memoryURLCache) Set(_ context.Context, key, url string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = memoryEntry{url: url, expiresAt: time.Now().Add(ttl)}
}

// sweep buang entri luput supaya map tak membesar tanpa had — setiap
// kunci objek yang pernah dipaparkan akan kekal selamanya kalau tidak.
func (c *memoryURLCache) sweep() {
	for {
		time.Sleep(10 * time.Minute)
		now := time.Now()
		c.mu.Lock()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
