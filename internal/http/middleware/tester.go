package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"marc/internal/db/sqlc"
)

// BlockTesterWrites tolak tindakan KEWANGAN sebenar (checkout bayaran)
// untuk role "tester" — akaun review Google Play/App Store (keputusan
// produk 2026-08-15). Tester berkelakuan macam ahli biasa untuk SEMUA
// tindakan LAIN (daftar, post, like, daftar aktiviti, lihat skrin) —
// reviewer perlu uji aliran app sebenar untuk lulus review; checkout
// bayaran SAHAJA disekat, elak caj sebenar tertimbul drpd akaun review
// automatik. Tindakan pengurusan (luluskan ahli, tukar role, urus
// kategori, terbit/batal aktiviti) TAK perlu disekat di sini — role
// tester ada category 'ahli' (bukan 'management'), jadi semua gate
// authz.IsManagement/authz.IsAtLeastRole sedia ada dah tolak tester
// terus, tanpa perubahan.
//
// Letak SELEPAS RequireAuth ATAU OptionalAuth. Kalau request tak
// authenticated (cth donation awam), middleware ni lepas terus — tiada
// laluan pintas sebab tester mesti log masuk dahulu utk cuba checkout,
// jadi UserIDOptional sentiasa `ok` untuk mereka.
func BlockTesterWrites(q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := UserIDOptional(c)
		if !ok {
			c.Next()
			return
		}

		roleKey, err := q.GetRoleKeyByUserID(c.Request.Context(), userID)
		if err != nil {
			// Gagal tertutup (padanan RequireApprovedStatus/
			// RequireVerifiedEmail) — DB tak boleh disemak bukan alasan
			// untuk biar laluan bayaran terbuka.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
			return
		}
		if roleKey == "tester" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "akaun tester tidak boleh membuat bayaran sebenar"})
			return
		}
		c.Next()
	}
}
