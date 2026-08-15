package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS benarkan permintaan fetch() cross-origin dari laman web MARC
// (marc_astro) untuk laluan AWAM terpilih SAHAJA — dipasang per-route,
// BUKAN global. Skop sengaja sempit: kebanyakan endpoint app ni
// memerlukan Bearer token dalam header (bukan cookie sesi), jadi CORS
// longgar tidak membuka vector CSRF di sini seperti app berasaskan
// cookie — tapi tiada sebab buka CORS pada endpoint yang tak perlu.
// Keputusan produk 2026-08-16: laman web (marc.hafizbahtiar.com)
// perlukan ni utk halaman pengesahan emel (`POST /auth/verify-email/
// confirm`), yang dipanggil terus dari browser selepas ahli klik
// pautan dalam emel.
//
// Senarai origin dibenarkan diserah EKSPLISIT (bukan wildcard "*") —
// wildcard bersama respons JSON sensitif ialah tabiat buruk walau
// endpoint ni sendiri tak bawa kredential; senarai eksplisit juga
// buat niat jelas dibaca drpd config, bukan diteka.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			allowed[o] = true
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			// Vary: Origin — respons ni berbeza ikut Origin peminta, jadi
			// cache perantara (CDN/proksi) tak boleh kongsi salinan antara
			// origin berbeza.
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Max-Age", "3600")
		}

		// Preflight — pelayar hantar OPTIONS dahulu utk POST JSON (bukan
		// "simple request"). Jawab terus, jangan sampai ke handler sebenar.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
