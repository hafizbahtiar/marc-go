package receipt

import (
	"bytes"
	"testing"
	"time"
)

func TestGeneratePDF(t *testing.T) {
	out, err := GeneratePDF(Donation{
		MemberID:    "MARC-000123",
		DonorName:   "Nurul Aïsyah binti Zulkifli",
		DonorEmail:  "nurul@example.com",
		AmountCents: 150_050,
		Currency:    "myr",
		GatewayRef:  "pi_3QabcdEFGHijkLmn0PqRstUv",
		PaidAt:      time.Date(2026, 8, 9, 14, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("GeneratePDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output bukan PDF, 8 bait pertama: %q", out[:min(8, len(out))])
	}
	if len(out) < 1000 {
		t.Fatalf("PDF terlalu kecil (%d bait) — kemungkinan halaman kosong", len(out))
	}
}

// Sumbangan awanama tanpa nama/emel/member id tak boleh panic atau
// tinggalkan sel kosong — semua ada fallback.
func TestGeneratePDFAnonymous(t *testing.T) {
	out, err := GeneratePDF(Donation{AmountCents: 1000, Currency: "myr", GatewayRef: "pi_x"})
	if err != nil {
		t.Fatalf("GeneratePDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatal("output bukan PDF")
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{0, "myr", "RM0.00"},
		{500, "myr", "RM5.00"},
		{150_050, "myr", "RM1,500.50"},
		{123_456_789, "MYR", "RM1,234,567.89"},
		{100_000, "", "RM1,000.00"},
		{2500, "usd", "USD 25.00"},
	}
	for _, c := range cases {
		if got := formatAmount(c.cents, c.currency); got != c.want {
			t.Errorf("formatAmount(%d, %q) = %q, mahu %q", c.cents, c.currency, got, c.want)
		}
	}
}

func TestAmountInWords(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{100, "Ringgit Malaysia Satu sahaja"},
		{1100, "Ringgit Malaysia Sebelas sahaja"},
		{2500, "Ringgit Malaysia Dua Puluh Lima sahaja"},
		{10_000, "Ringgit Malaysia Seratus sahaja"},
		{25_050, "Ringgit Malaysia Dua Ratus Lima Puluh Dan Lima Puluh Sen sahaja"},
		{100_000, "Ringgit Malaysia Seribu sahaja"},
		{150_000, "Ringgit Malaysia Seribu Lima Ratus sahaja"},
		{1_234_500, "Ringgit Malaysia Dua Belas Ribu Tiga Ratus Empat Puluh Lima sahaja"},
		{50, "Ringgit Malaysia Lima Puluh Sen sahaja"},
	}
	for _, c := range cases {
		if got := amountInWords(c.cents, "myr"); got != c.want {
			t.Errorf("amountInWords(%d) = %q, mahu %q", c.cents, got, c.want)
		}
	}

	// Ejaan Melayu tak sesuai untuk mata wang lain — panel eja dilangkau.
	if got := amountInWords(2500, "usd"); got != "" {
		t.Errorf("amountInWords bukan MYR = %q, mahu kosong", got)
	}
}
