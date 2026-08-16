package phone

import "testing"

func TestNormalizeMYFormatSah(t *testing.T) {
	cases := map[string]string{
		"0123456789":    "0123456789",  // prefix biasa, 10 digit
		"012-345 6789":  "0123456789",  // dash + space dibuang
		"+60123456789":  "0123456789",  // +60 -> 0
		"60123456789":   "0123456789",  // 60 (tanpa +) -> 0
		"01112345678":   "01112345678", // prefix 011, 11 digit
		"+601112345678": "01112345678",
	}
	for input, want := range cases {
		got, ok := NormalizeMY(input)
		if !ok {
			t.Errorf("NormalizeMY(%q) patut sah, ditolak", input)
			continue
		}
		if got != want {
			t.Errorf("NormalizeMY(%q) = %q, mahu %q", input, got, want)
		}
	}
}

func TestNormalizeMYFormatTidakSah(t *testing.T) {
	cases := []string{
		"",
		"123456789",     // tiada awalan 01
		"0151234567",    // 015 bukan prefix mudah alih rasmi
		"012345",        // terlalu pendek
		"0123456789012", // terlalu panjang
		"abcdefghij",
		"03-12345678", // talian tetap, bukan mudah alih
		"0111234567",  // "011"+7 digit (10 digit) — MESTI tolak, cabang
		// 011 sengaja 11 digit; kelas digit ketiga cabang biasa
		// kecualikan '1' khusus untuk elak pertindihan ni.
	}
	for _, input := range cases {
		if _, ok := NormalizeMY(input); ok {
			t.Errorf("NormalizeMY(%q) patut ditolak, tapi lulus", input)
		}
	}
}
