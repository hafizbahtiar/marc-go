package handlers

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestVerifyResponseHanyaMedanAwam — TRIPWIRE PRIVASI.
//
// KALAU UJIAN INI GAGAL, ANDA BARU SAHAJA MENAMBAH (ATAU MEMBUANG) SATU
// MEDAN PADA RESPONS AWAM `GET /verify/certificates/:token`.
//
// Route itu satu-satunya dalam modul aktiviti yang tiada auth: sesiapa di
// internet yang ada token boleh membacanya, kerana kod QR pada sijil bercetak
// discan oleh majikan yang tiada akaun MARC. Setiap medan yang muncul di sini
// disiarkan kepada dunia. Nama penerima sudah pun berada di situ; emel,
// user_id, id keahlian, status keahlian, r2_key, atau sebab tarik balik TIDAK
// sepatutnya.
//
// Jangan sekadar meluaskan senarai `mahu` di bawah supaya build hijau semula.
// Tanya dahulu: adakah saya selesa medan ini berada pada halaman web awam?
// Kalau ya, luaskan senarai DENGAN SENGAJA — itulah gunanya ujian ini.
//
// Ia TANPA pangkalan data dengan sengaja. Ujian hujung-ke-hujung yang setara
// (TestVerifyTidakMendedahkanPII) di-SKIP bila ACTIVITY_TEST_DB tidak
// ditetapkan, iaitu pada setiap PR dalam CI — jadi ia sahaja bermakna
// kebocoran PII boleh dihantar dengan build hijau. Yang diuji di sini ialah
// sempadan penyirian, bukan query, jadi ia tidak perlukan DB langsung.
func TestVerifyResponseHanyaMedanAwam(t *testing.T) {
	raw, err := json.Marshal(verifyResponse{})
	if err != nil {
		t.Fatalf("siri verifyResponse: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("nyahkod semula: %v", err)
	}

	ada := make([]string, 0, len(payload))
	for k := range payload {
		ada = append(ada, k)
	}
	sort.Strings(ada)

	mahu := []string{
		"activity_date", "activity_title", "issued_at",
		"recipient_name", "serial", "status",
	}

	if len(ada) != len(mahu) {
		t.Fatalf("set medan awam = %v, mahu %v", ada, mahu)
	}
	for i := range mahu {
		if ada[i] != mahu[i] {
			t.Fatalf("set medan awam = %v, mahu %v", ada, mahu)
		}
	}
}
