package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Pakej ni TULEN — tiada DB, tiada rangkaian. Ujiannya benar-benar
// berjalan dalam CI (tak macam ujian live repo ni yang SKIP tanpa
// Postgres), jadi ia antara sedikit tempat yang regresi auth ditangkap
// SEBELUM merge. Lihat TODO.md L36.

const testSecret = "ujian-rahsia-jangan-guna-dalam-produksi"

// ---- JWT ----

func TestAccessTokenPusinganPenuh(t *testing.T) {
	j := NewJWT(testSecret, 15*time.Minute)
	want := uuid.New()

	token, err := j.GenerateAccessToken(want)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	got, err := j.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	if got != want {
		t.Errorf("user id = %v, mahu %v", got, want)
	}
}

func TestAccessTokenLuputDitolak(t *testing.T) {
	// TTL negatif = token yang sudah luput pada saat ia dijana.
	j := NewJWT(testSecret, -time.Minute)

	token, err := j.GenerateAccessToken(uuid.New())
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	if _, err := j.ParseAccessToken(token); err == nil {
		t.Fatal("token LUPUT diterima — sesi tak pernah tamat")
	}
}

func TestAccessTokenRahsiaLainDitolak(t *testing.T) {
	issuer := NewJWT(testSecret, 15*time.Minute)
	token, err := issuer.GenerateAccessToken(uuid.New())
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	verifier := NewJWT("rahsia-yang-berbeza-sama-sekali", 15*time.Minute)
	if _, err := verifier.ParseAccessToken(token); err == nil {
		t.Fatal("token ditandatangani rahsia LAIN diterima — sesiapa yang " +
			"boleh jana JWT boleh menyamar sebagai mana-mana ahli")
	}
}

// Kekeliruan algoritma (`alg` confusion) — token `alg: none` mesti
// ditolak.
//
// BUKAN tripwire bagi semakan `t.Method.(*jwt.SigningMethodHMAC)` dalam
// `ParseAccessToken`. Disahkan 2026-08-22: membuang semakan itu, ujian
// ni TETAP lulus. Sebabnya jwt/v5 sendiri menaip kuncinya —
// `signingMethodNone.Verify` menuntut kunci ialah
// `UnsafeAllowNoneSignatureType` (none.go:31) dan keyfunc kita
// memulangkan `[]byte`, jadi pustaka yang menolaknya. RSA/ECDSA sama:
// `Verify` mereka menuntut jenis kunci khusus, bukan `[]byte`.
//
// Jadi semakan HMAC eksplisit itu ialah pertahanan-berlapis, bukan
// penanggung beban dalam jwt/v5 v5.3.1 — ia mesti disemak dengan
// MEMBACA, bukan dianggap dilitupi ujian.
//
// Ujian ni tetap berbaloi: ia mengunci TINGKAH LAKU yang boleh dilihat.
// Kalau naik taraf pustaka melonggarkan penaipan kunci, atau kalau
// seseorang menukar keyfunc supaya memulangkan sentinel itu, ini yang
// jerit.
func TestParseAccessTokenTolakAlgBukanHMAC(t *testing.T) {
	j := NewJWT(testSecret, 15*time.Minute)
	claims := jwt.RegisteredClaims{
		Subject:   uuid.New().String(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}

	// `alg: none` — jwt/v5 memerlukan sentinel khas untuk membenarkan
	// penandatanganan tanpa kunci, tepat kerana ia berbahaya.
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenNone, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("jana token alg=none: %v", err)
	}

	if _, err := j.ParseAccessToken(tokenNone); err == nil {
		t.Fatal("token `alg: none` DITERIMA — sesiapa boleh mengarang token " +
			"untuk mana-mana user id tanpa sebarang rahsia")
	}
}

func TestParseAccessTokenTolakSampahDanSubjectCacat(t *testing.T) {
	j := NewJWT(testSecret, 15*time.Minute)

	// Subject yang BUKAN UUID: ditandatangani dengan betul, jadi
	// tandatangan lulus — cuma `uuid.Parse` yang menahannya.
	claims := jwt.RegisteredClaims{
		Subject:   "bukan-uuid-langsung",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	for nama, token := range map[string]string{
		"kosong":                    "",
		"bukan jwt":                 "sampah",
		"tiga bahagian tapi sampah": "a.b.c",
		"subject bukan uuid":        signed,
		"tandatangan diusik":        signed + "x",
	} {
		t.Run(nama, func(t *testing.T) {
			if _, err := j.ParseAccessToken(token); err == nil {
				t.Fatalf("token %q diterima", nama)
			}
		})
	}
}

func TestAccessTTLDidedahkan(t *testing.T) {
	// Handler memulangkan nilai ni sebagai `expires_in`, jadi ia
	// sebahagian kontrak API, bukan butiran dalaman.
	j := NewJWT(testSecret, 15*time.Minute)
	if got := j.AccessTTL(); got != 15*time.Minute {
		t.Errorf("AccessTTL() = %v, mahu 15m", got)
	}
}

// ---- Kata laluan ----

func TestPasswordPusinganPenuh(t *testing.T) {
	hash, err := HashPassword("kata-laluan-betul")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !VerifyPassword(hash, "kata-laluan-betul") {
		t.Error("kata laluan BETUL ditolak")
	}
	if VerifyPassword(hash, "kata-laluan-salah") {
		t.Error("kata laluan SALAH diterima")
	}
}

// Hash bcrypt mesti bergaram: dua hash bagi kata laluan yang SAMA tak
// boleh serupa, kalau tidak DB yang bocor mendedahkan ahli mana yang
// berkongsi kata laluan.
func TestPasswordHashBergaram(t *testing.T) {
	a, err := HashPassword("sama")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("sama")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if a == b {
		t.Fatal("dua hash bagi kata laluan sama adalah SERUPA — tiada garam")
	}
	if !VerifyPassword(a, "sama") || !VerifyPassword(b, "sama") {
		t.Fatal("hash bergaram tak boleh disahkan semula")
	}
}

// bcrypt menolak input melebihi 72 bait. Handler mengehadkan `max=72`
// pada tag binding — ujian ni merekod SEBAB had itu wujud, supaya tiada
// siapa melonggarkannya tanpa menyedari HashPassword akan mula gagal.
func TestPasswordLebih72BaitDitolakBcrypt(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", 73)); err == nil {
		t.Fatal("bcrypt menerima >72 bait — had `max=72` pada handler " +
			"mungkin tak lagi diperlukan, sahkan sebelum melonggarkannya")
	}
}

func TestVerifyPasswordHashCacat(t *testing.T) {
	// Laluan ni berlaku sebenar: `dummyPasswordHash` dibandingkan bila
	// emel tak wujud. Hash cacat mesti pulang false, bukan panik.
	if VerifyPassword("bukan-hash-bcrypt", "apa-apa") {
		t.Fatal("hash cacat mengesahkan sebagai BETUL")
	}
}

// ---- Token legap ----

func TestGenerateOpaqueTokenUnikDanCukupPanjang(t *testing.T) {
	const n = 200
	seen := make(map[string]bool, n)

	for i := 0; i < n; i++ {
		tok, err := GenerateOpaqueToken()
		if err != nil {
			t.Fatalf("GenerateOpaqueToken: %v", err)
		}
		// 32 bait rawak → 43 aksara base64url tanpa padding.
		if len(tok) < 40 {
			t.Fatalf("token terlalu pendek (%d aksara) — entropi tak cukup "+
				"untuk kelayakan pembawa", len(tok))
		}
		if seen[tok] {
			t.Fatalf("token BERULANG selepas %d janaan", i)
		}
		seen[tok] = true
	}
}

// Token base64url tanpa padding SELAMAT dalam URL — ia masuk ke pautan
// pengesahan emel (`?token=`) dan laluan pengesahan sijil, jadi aksara
// yang perlu di-escape akan pecah secara senyap.
func TestGenerateOpaqueTokenSelamatDalamURL(t *testing.T) {
	for i := 0; i < 50; i++ {
		tok, err := GenerateOpaqueToken()
		if err != nil {
			t.Fatalf("GenerateOpaqueToken: %v", err)
		}
		if strings.ContainsAny(tok, "+/=?&#% ") {
			t.Fatalf("token %q mengandungi aksara yang perlu di-escape dalam URL", tok)
		}
	}
}

func TestHashTokenStabilDanSatuArah(t *testing.T) {
	tok, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}

	h1, h2 := HashToken(tok), HashToken(tok)
	if h1 != h2 {
		t.Fatal("HashToken tak deterministik — carian ikut hash takkan pernah padan")
	}
	if h1 == tok {
		t.Fatal("HashToken memulangkan input — token disimpan sebagai teks biasa")
	}
	if len(h1) != 64 {
		t.Errorf("panjang hash = %d, mahu 64 (hex SHA-256)", len(h1))
	}
	if HashToken(tok) == HashToken(tok+"x") {
		t.Fatal("input berbeza menghasilkan hash sama")
	}
}
