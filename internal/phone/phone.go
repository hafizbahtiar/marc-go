// Package phone sahkan format nombor telefon ikut negara — SATU fungsi
// setiap negara (cth `NormalizeMY`), bukan satu regex generik cuba
// litupi semua negara sekali gus. Tambah negara lain kelak = tambah
// fungsi baharu (`NormalizeSG`, dll), tak sentuh yang sedia ada.
//
// Buat masa ini cuma Malaysia — keputusan produk 2026-08-15 (nombor
// telefon jadi wajib semasa /auth/register, sebab ToyyibPay perlukan
// `billPhone` bukan-kosong pada createBill).
package phone

import (
	"regexp"
	"strings"
)

// myMobileRegex — nombor mudah alih Malaysia dalam bentuk TEMPATAN
// ternormal (`0` di depan, tiada dash/space). Dua bentuk:
//   - 01[0,2,3,4,6,7,8,9] + 7 digit (10 digit jumlah) — prefix biasa
//     (010/012/013/014/016/017/018/019). 1 DIKECUALIKAN daripada kelas
//     digit ketiga ni (bukan sekadar 5) — kalau tidak, "011" + 7 digit
//     (10 digit) turut lulus cabang ni, bertindih dgn cabang 011 di
//     bawah yang sengaja 11 digit.
//   - 011 + 8 digit (11 digit jumlah) — prefix 011 ada satu digit lagi.
var myMobileRegex = regexp.MustCompile(`^(01[02-46-9]\d{7}|011\d{8})$`)

// myCleanRegex — aksara dibuang sebelum padan: space (termasuk tab/
// newline via `\s`), dash, kurungan. Guna regex (bukan senarai literal
// aksara) supaya padan cara `RegExp(r'[\s\-()]')` di Dart bersihkan
// (`shared/phone.dart`) — dua-dua bahasa kena bersih input SAMA supaya
// tak ada kes klien terima tapi server tolak (atau sebaliknya).
var myCleanRegex = regexp.MustCompile(`[\s\-()]`)

// NormalizeMY bersihkan (buang space/dash/kurungan) dan sahkan nombor
// mudah alih Malaysia. Terima awalan `+60`, `60`, atau `0`. Pulang bentuk
// TEMPATAN ternormal (`0XXXXXXXXX`) dan `true` kalau sah — caller SIMPAN
// nilai pulangan ni, bukan input mentah, supaya bentuk yang disimpan
// konsisten tak kira apa format pengguna taip.
func NormalizeMY(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = myCleanRegex.ReplaceAllString(s, "")

	switch {
	case strings.HasPrefix(s, "+60"):
		s = "0" + s[3:]
	case strings.HasPrefix(s, "60") && len(s) >= 11:
		s = "0" + s[2:]
	}

	if !myMobileRegex.MatchString(s) {
		return "", false
	}
	return s, true
}
