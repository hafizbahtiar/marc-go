package disposableemail

import "testing"

func TestIsDisposable(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"someone@yopmail.com", true},
		{"someone@mailinator.com", true},
		{"someone@gmail.com", false},
		{"someone@hafizbahtiar98gmail.com", false},
		// Pengecualian eksplisit — allowlist alamat PENUH menang atas
		// domain pelupusan (keputusan produk 2026-08-15).
		{"google@yopmail.com", false},
		{"apple@yopmail.com", false},
		// Alamat lain pada domain yopmail SAMA tetap disekat — allowlist
		// alamat PENUH, bukan domain.
		{"randomperson@yopmail.com", true},
	}
	for _, c := range cases {
		if got := IsDisposable(c.email); got != c.want {
			t.Errorf("IsDisposable(%q) = %v, mahu %v", c.email, got, c.want)
		}
	}
}

func TestDomainOf(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"someone@yopmail.com", "yopmail.com"},
		{"SOMEONE@YOPMAIL.COM", "yopmail.com"},
		{"invalid-email", ""},
		{"trailing@", ""},
	}
	for _, c := range cases {
		if got := DomainOf(c.email); got != c.want {
			t.Errorf("DomainOf(%q) = %q, mahu %q", c.email, got, c.want)
		}
	}
}

// TestIsAllowed — Opus verify 2026-08-15 tangkap bug: laluan jadual DB
// `blocked_email_domains` (auth.go) asalnya tak runding dengan
// allowlist ni langsung, jadi management tambah "yopmail.com" ke
// jadual akan senyap kunci keluar dua akaun tester. IsAllowed diexport
// khas supaya auth.go boleh semak SEMULA sebelum panggil DB.
func TestIsAllowed(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"google@yopmail.com", true},
		{"apple@yopmail.com", true},
		{"randomperson@yopmail.com", false},
		{"someone@gmail.com", false},
	}
	for _, c := range cases {
		if got := IsAllowed(c.email); got != c.want {
			t.Errorf("IsAllowed(%q) = %v, mahu %v", c.email, got, c.want)
		}
	}
}

func TestStaticListLoaded(t *testing.T) {
	if len(staticDomains) < 1000 {
		t.Fatalf("senarai statik terbenam terlalu kecil (%d domain) — domains.txt mungkin gagal embed", len(staticDomains))
	}
}
