package receipt

import "testing"

func TestFilename(t *testing.T) {
	cases := []struct {
		name       string
		label      string
		ref        string
		fallbackID string
		want       string
	}{
		{
			name:  "ref biasa",
			label: "Pendaftaran",
			ref:   "TBP123456789",
			want:  "Resit-Pendaftaran-MARC-TBP123456789.pdf",
		},
		{
			name:       "ref kosong jatuh balik ke fallbackID",
			label:      "Aktiviti",
			ref:        "",
			fallbackID: "550e8400-e29b-41d4-a716-446655440000",
			want:       "Resit-Aktiviti-MARC-550e8400-e29b-41d4-a716-446655440000.pdf",
		},
		{
			name:  "aksara tak selamat ditukar ke tanda sempang",
			label: "Sokongan",
			ref:   "pi_3Qab/cd EFG?hij",
			want:  "Resit-Sokongan-MARC-pi_3Qab-cd-EFG-hij.pdf",
		},
		{
			name:       "ref DAN fallback kosong — jangan hasilkan nama fail rosak",
			label:      "Pendaftaran",
			ref:        "",
			fallbackID: "",
			want:       "Resit-Pendaftaran-MARC-MARC.pdf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Filename(c.label, c.ref, c.fallbackID)
			if got != c.want {
				t.Errorf("Filename(%q, %q, %q) = %q, mahu %q",
					c.label, c.ref, c.fallbackID, got, c.want)
			}
		})
	}
}
