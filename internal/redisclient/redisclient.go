// Package redisclient bungkus sambungan Redis kongsi.
//
// Ikut corak yang sama dengan perkhidmatan pilihan lain dalam projek ni
// (R2, Stripe, Resend, OneSignal): kalau `REDIS_URL` kosong, client jadi
// **no-op yang terdayakan-palsu** dan app tetap boot. Ciri yang bergantung
// padanya jatuh balik kepada tingkah laku setempat, bukan crash.
//
// Kenapa itu penting di sini: Redis dalam app ni ialah pengganda skala,
// bukan simpanan kebenaran. Tiada data yang HANYA wujud dalam Redis —
// kalau ia hilang, kita hilang penyelarasan antara instance, bukan data.
// Reka bentuk yang menjadikan Redis wajib akan menukar kebergantungan
// pilihan menjadi titik kegagalan tunggal tanpa sebab.
package redisclient

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb *redis.Client
}

// New sambung ke Redis. URL kosong = client dimatikan (bukan ralat).
//
// Tak membuat I/O — sambungan sebenar berlaku malas pada arahan pertama.
// Guna Ping untuk mengesahkan kebolehcapaian semasa boot.
func New(url string) (*Client, error) {
	if url == "" {
		return &Client{}, nil
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}

	// Timeout eksplisit: lalai go-redis agak murah hati, dan permintaan
	// HTTP kita ada bajet masanya sendiri. Redis yang perlahan tak patut
	// menahan permintaan pengguna.
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second

	return &Client{rdb: redis.NewClient(opts)}, nil
}

// Enabled sama ada Redis dikonfigur. Caller patut jatuh balik kepada
// tingkah laku setempat bila false.
func (c *Client) Enabled() bool { return c != nil && c.rdb != nil }

// Ping sahkan Redis benar-benar boleh dicapai. Dipanggil sekali semasa
// boot supaya salah konfigurasi muncul dalam log serta-merta, bukan pada
// permintaan pengguna pertama.
func (c *Client) Ping(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.rdb.Ping(ctx).Err()
}

// Redis dedahkan client mentah untuk kegunaan lanjut. Pulang nil bila
// dimatikan — caller MESTI semak Enabled() dahulu.
func (c *Client) Redis() *redis.Client {
	if !c.Enabled() {
		return nil
	}
	return c.rdb
}

func (c *Client) Close() error {
	if !c.Enabled() {
		return nil
	}
	return c.rdb.Close()
}

// URLCache — cache rentetan berumur pendek yang dikongsi semua instance.
//
// Digunakan untuk URL R2 yang ditandatangani. Tanpa cache KONGSI, setiap
// replika menandatangani URL sendiri; klien yang mencapai instance
// berlainan dapat URL berlainan untuk gambar yang SAMA, dan cache imej
// pada peranti (dikunci ikut URL) terlepas setiap kali.
//
// Pulang nil bila Redis dimatikan — caller patut jatuh balik kepada cache
// dalam-memori.
func (c *Client) URLCache(prefix string) *URLCache {
	if !c.Enabled() {
		return nil
	}
	return &URLCache{rdb: c.rdb, prefix: prefix}
}

type URLCache struct {
	rdb    *redis.Client
	prefix string
}

func (u *URLCache) Get(ctx context.Context, key string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	val, err := u.rdb.Get(ctx, u.prefix+key).Result()
	if err != nil {
		// Terlepas cache ATAU Redis bermasalah — dua-dua bermakna
		// "tandatangan baharu". Jangan gagalkan permintaan sebab cache
		// tak dapat dibaca.
		return "", false
	}
	return val, true
}

func (u *URLCache) Set(ctx context.Context, key, url string, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = u.rdb.Set(ctx, u.prefix+key, url, ttl).Err()
}
