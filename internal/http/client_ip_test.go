package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestClientIPMerentasProksi mengunci tingkah laku yang dua sistem
// bergantung padanya secara SENYAP: had kadar (RateLimiter.Limit kunci
// baldi pada c.ClientIP()) dan pengesanan guna-semula refresh token
// (consumedIPMatches, internal/http/handlers/auth.go membandingkan
// consumed_ip dengan IP permintaan).
//
// Portal web (marc_next) ialah BFF -- pelayar tak pernah menyentuh
// backend ini, jadi setiap permintaan web tiba dari SATU alamat: pelayan
// Next atas rangkaian peribadi Railway. Tanpa penyiaran semula
// X-Forwarded-For, seluruh portal berkongsi satu baldi had kadar, dan
// setiap guna-semula token web kelihatan sebagai "IP sama" kepada
// tetingkap anggun -- melemahkan pengesanan kecurian yang ia dibina
// untuk menangkap.
//
// Kes di bawah ialah kontrak yang marc_next bergantung padanya. Ia
// TIDAK menguji kod kita sendiri: ia menguji Gin + trustedProxyRanges,
// yang bermakna menambah atau membuang julat dalam senarai itu akan
// menggagalkan ujian ini dan bukan menyebabkan pepijat senyap dalam
// produksi.
func TestClientIPMerentasProksi(t *testing.T) {
	gin.SetMode(gin.TestMode)

	kes := []struct {
		nama       string
		remoteAddr string
		xff        string
		mahu       string
	}{
		{
			// Laluan BFF. marc_next menyiarkan semula rantaian masuk
			// APA ADANYA (lihat lib/api/client.ts) -- ia tak memilih
			// satu entri, kerana lelaran kanan-ke-kiri Gin di bawah
			// itulah yang membuang entri palsu.
			nama:       "Next atas rangkaian peribadi menyiarkan IP ahli",
			remoteAddr: "100.64.3.7:54321",
			xff:        "203.0.113.9",
			mahu:       "203.0.113.9",
		},
		{
			// Kalau penyiaran semula itu pernah dibuang, ini yang
			// backend nampak -- dan had kadar runtuh kepada satu baldi
			// dikongsi untuk semua pengguna web.
			nama:       "tiada XFF, jatuh kepada alamat Next",
			remoteAddr: "100.64.3.7:54321",
			xff:        "",
			mahu:       "100.64.3.7",
		},
		{
			nama:       "proksi awam Railway, satu entri",
			remoteAddr: "100.64.0.2:443",
			xff:        "203.0.113.9",
			mahu:       "203.0.113.9",
		},
		{
			// Entri yang disuntik klien duduk di KIRI; Gin melelar dari
			// kanan dan berhenti pada IP bukan-proksi yang pertama, jadi
			// nilai palsu tak pernah menang. Inilah sebab marc_next
			// menyiarkan rantaian penuh dan bukan mengambil elemen [0].
			nama:       "klien memalsukan XFF, Railway menambah IP sebenar",
			remoteAddr: "100.64.0.2:443",
			xff:        "1.2.3.4, 203.0.113.9",
			mahu:       "203.0.113.9",
		},
		{
			// Loopback SENGAJA tiada dalam trustedProxyRanges. Kesannya:
			// dalam pembangunan tempatan, XFF diabaikan dan setiap
			// permintaan dikira sebagai 127.0.0.1. Tingkah laku
			// penyiaran semula hanya boleh diperhatikan di staging.
			nama:       "loopback bukan proksi dipercayai",
			remoteAddr: "127.0.0.1:9999",
			xff:        "203.0.113.9",
			mahu:       "127.0.0.1",
		},
	}

	for _, k := range kes {
		t.Run(k.nama, func(t *testing.T) {
			r := gin.New()
			if err := r.SetTrustedProxies(trustedProxyRanges); err != nil {
				t.Fatalf("set trusted proxies: %v", err)
			}

			var dapat string
			r.GET("/", func(c *gin.Context) { dapat = c.ClientIP() })

			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = k.remoteAddr
			if k.xff != "" {
				req.Header.Set("X-Forwarded-For", k.xff)
			}
			r.ServeHTTP(httptest.NewRecorder(), req)

			if dapat != k.mahu {
				t.Errorf("ClientIP() = %q, mahu %q", dapat, k.mahu)
			}
		})
	}
}
