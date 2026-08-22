package config

import (
	"testing"
	"time"
)

// Pakej ni TULEN (env → struct, tiada I/O), jadi ujiannya benar-benar
// berjalan dalam CI — tak macam kebanyakan ujian repo ni yang SKIP tanpa
// Postgres. Itu yang menjadikannya berbaloi diuji rapat: ia salah satu
// daripada sedikit tempat di mana regresi benar-benar ditangkap sebelum
// merge (lihat TODO.md L14/L36).
//
// Yang diuji ialah kontrak yang ditulis pada komen `Load`: app SENTIASA
// boot kecuali dua env wajib hilang, dan nilai tak sah jatuh ke default
// dan bukan menggagalkan boot.

func TestLoadTolakEnvWajibYangHilang(t *testing.T) {
	for _, tc := range []struct{ nama, dbURL, secret string }{
		{"dua-dua kosong", "", ""},
		{"DATABASE_URL kosong", "", "s3cret"},
		{"JWT_SECRET kosong", "postgres://x", ""},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.dbURL)
			t.Setenv("JWT_SECRET", tc.secret)

			if _, err := Load(); err == nil {
				t.Fatal("Load() berjaya tanpa env wajib — app akan boot dengan " +
					"auth rosak atau tanpa DB")
			}
		})
	}
}

// Kontrak teras: SEMUA env lain optional. Satu pun tak boleh menggagalkan
// boot — kalau ada yang mula wajib, ujian ni yang jerit.
func TestLoadBootDenganDuaEnvWajibSahaja(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/marc")
	t.Setenv("JWT_SECRET", "s3cret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal walau kedua-dua env wajib diisi: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("Port = %q, mahu \"8080\"", cfg.Port)
	}
	if cfg.PublicBaseURL != "http://localhost:8080" {
		t.Errorf("PublicBaseURL = %q — lalai patut dibina drpd PORT", cfg.PublicBaseURL)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Errorf("AccessTokenTTL = %v, mahu 15m", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 30*24*time.Hour {
		t.Errorf("RefreshTokenTTL = %v, mahu 30 hari", cfg.RefreshTokenTTL)
	}
	if cfg.RegistrationFeeCents != 1000 {
		t.Errorf("RegistrationFeeCents = %d, mahu 1000", cfg.RegistrationFeeCents)
	}
}

// PublicBaseURL lalai dibina drpd PORT — dua getEnv("PORT") berasingan
// dalam ekspresi yang sama, jadi ia senyap terpesong kalau salah satu
// diubah tanpa yang lain.
func TestPublicBaseURLLalaiIkutPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/marc")
	t.Setenv("JWT_SECRET", "s3cret")
	t.Setenv("PORT", "9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicBaseURL != "http://localhost:9999" {
		t.Errorf("PublicBaseURL = %q — tak ikut PORT yang ditetapkan", cfg.PublicBaseURL)
	}
}

func TestGetEnvDays(t *testing.T) {
	const key = "TEST_RETENTION_DAYS"

	for _, tc := range []struct {
		nama  string
		set   bool
		raw   string
		fallb int
		mahu  time.Duration
	}{
		{"tak diset → fallback", false, "", 90, 90 * 24 * time.Hour},
		{"kosong → fallback", true, "", 90, 90 * 24 * time.Hour},
		{"nilai sah", true, "7", 90, 7 * 24 * time.Hour},
		{"bukan nombor → fallback", true, "sembilan", 90, 90 * 24 * time.Hour},
		{"negatif → fallback", true, "-1", 90, 90 * 24 * time.Hour},
		// SIFAR ialah nilai BERMAKNA, bukan tak sah: ia mematikan sapuan
		// (lihat retention.RunOnce, `if r.policy.X > 0`). Kalau ia jatuh ke
		// fallback, mematikan polisi simpanan jadi mustahil melalui env.
		{"sifar mematikan sapuan, BUKAN fallback", true, "0", 90, 0},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.raw)
			}
			if got := getEnvDays(key, tc.fallb); got != tc.mahu {
				t.Errorf("getEnvDays(%q) = %v, mahu %v", tc.raw, got, tc.mahu)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	const key = "TEST_FEE_CENTS"

	for _, tc := range []struct {
		nama  string
		set   bool
		raw   string
		mahu  int
		fallb int
	}{
		{"tak diset → fallback", false, "", 1000, 1000},
		{"nilai sah", true, "2500", 2500, 1000},
		{"bukan nombor → fallback", true, "RM25", 1000, 1000},
		{"negatif → fallback", true, "-5", 1000, 1000},
		// Beza drpd getEnvDays: yuran SIFAR tiada makna (bil ToyyibPay
		// dengan billAmount=0 akan ditolak gateway), jadi ia dilayan
		// sebagai tak sah dan jatuh ke fallback.
		{"sifar → fallback (yuran sifar tiada makna)", true, "0", 1000, 1000},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.raw)
			}
			if got := getEnvInt(key, tc.fallb); got != tc.mahu {
				t.Errorf("getEnvInt(%q) = %d, mahu %d", tc.raw, got, tc.mahu)
			}
		})
	}
}

func TestGetEnvList(t *testing.T) {
	const key = "TEST_CORS_ORIGINS"

	for _, tc := range []struct {
		nama string
		set  bool
		raw  string
		mahu []string
	}{
		{"tak diset → nil", false, "", nil},
		{"kosong → nil", true, "", nil},
		{"satu entri", true, "https://a.com", []string{"https://a.com"}},
		{"banyak entri", true, "https://a.com,https://b.com", []string{"https://a.com", "https://b.com"}},
		{"ruang dipangkas", true, " https://a.com , https://b.com ", []string{"https://a.com", "https://b.com"}},
		{"koma mengekor diabaikan", true, "https://a.com,", []string{"https://a.com"}},
		{"entri kosong di tengah diabaikan", true, "https://a.com,,https://b.com", []string{"https://a.com", "https://b.com"}},
		{"koma sahaja → kosong", true, ",,,", []string{}},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.raw)
			}
			got := getEnvList(key)
			if len(got) != len(tc.mahu) {
				t.Fatalf("getEnvList(%q) = %v, mahu %v", tc.raw, got, tc.mahu)
			}
			for i := range got {
				if got[i] != tc.mahu[i] {
					t.Errorf("entri %d = %q, mahu %q", i, got[i], tc.mahu[i])
				}
			}
		})
	}
}

// Origin CORS yang tersalah eja jadi rentetan kosong akan memadankan
// header `Origin` yang HILANG kalau ia pernah masuk ke dalam peta
// `allowed` (lihat middleware.CORS). getEnvList yang membuang entri
// kosong ialah pertahanan pertama terhadap itu.
func TestGetEnvListTidakPernahPulangEntriKosong(t *testing.T) {
	t.Setenv("TEST_ORIGINS_MESSY", " , https://a.com ,, , ")
	for i, o := range getEnvList("TEST_ORIGINS_MESSY") {
		if o == "" {
			t.Fatalf("entri %d ialah rentetan kosong — ia boleh jadi origin "+
				"yang dibenarkan secara tak sengaja", i)
		}
	}
}
