// Package certificate mengandungi logik sijil yang TULEN — tiada DB, tiada
// R2, tiada rangkaian. Itu yang menjadikannya boleh diuji tanpa infra,
// sama seperti internal/receipt.
package certificate

import "time"

// CheckinWindowPadding — berapa lama sebelum/selepas sesi kehadiran masih
// boleh ditanda sebagai check-in biasa.
//
// Tanpa had ini, kehadiran boleh ditanda seminggu kemudian tanpa jejak, dan
// sijil bergantung padanya. Menanda di luar tetingkap memerlukan tindakan
// pindaan berasingan yang dicatat audit.
const CheckinWindowPadding = 2 * time.Hour

// IsEligible — layakkah pendaftaran ini menerima sijil?
//
// Perbandingan dibuat dalam integer (attended*100 >= total*threshold) dan
// bukan float, supaya kes sempadan seperti 2/3 pada ambang 66 vs 67
// berkelakuan sama pada setiap platform.
func IsEligible(attended, totalSessions, thresholdPct int) bool {
	if totalSessions <= 0 {
		return false
	}
	return attended*100 >= totalSessions*thresholdPct
}

// WithinCheckinWindow — bolehkah kehadiran ditanda sekarang untuk sesi ini?
func WithinCheckinWindow(now, sessionStart, sessionEnd time.Time) bool {
	return !now.Before(sessionStart.Add(-CheckinWindowPadding)) &&
		!now.After(sessionEnd.Add(CheckinWindowPadding))
}
