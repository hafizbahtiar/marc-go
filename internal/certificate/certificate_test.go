package certificate

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/go-pdf/fpdf"
)

func testData() Data {
	return Data{
		Serial:        "MARC-2026-000123",
		RecipientName: "Ahmad bin Abdullah",
		ActivityTitle: "Kejohanan Badminton Terbuka 2026",
		CategoryName:  "Badminton",
		ActivityDate:  time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		VerifyURL:     "https://marc.example/verify/certificates/abc123",
	}
}

func TestGeneratePDFPulangkanPDFSah(t *testing.T) {
	out, err := GeneratePDF(testData())
	if err != nil {
		t.Fatalf("GeneratePDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Errorf("output bukan PDF, 8 bait pertama: %q", out[:min(8, len(out))])
	}
	// Sijil dengan QR terbenam sepatutnya jauh melebihi seribu bait.
	// Ambang longgar sengaja — ini semakan kewarasan, bukan ujian saiz.
	if len(out) < 2000 {
		t.Errorf("PDF terlalu kecil (%d bait), QR mungkin tak terbenam", len(out))
	}
}

func TestGeneratePDFTolakNamaTakBolehDikodkan(t *testing.T) {
	d := testData()
	// Fon Helvetica terbina fpdf hanya meliputi cp1252. Tanpa semakan ini,
	// nama begini akan DITERBITKAN dengan aksara hilang senyap-senyap —
	// sijil rosak yang tiada siapa perasan sehingga penerima membukanya.
	d.RecipientName = "李小龍"

	if _, err := GeneratePDF(d); err == nil {
		t.Fatal("mahu ralat untuk nama tak boleh dikodkan, dapat nil")
	}
}

// Nama bukan satu-satunya medan yang dicetak. Tajuk, kategori dan siri
// juga melalui penterjemah cp1252, jadi aksara tak boleh dikodkan di mana-
// mana antaranya mesti ditolak — bukan diterbitkan sebagai "...".
func TestGeneratePDFTolakMedanLainTakBolehDikodkan(t *testing.T) {
	tests := []struct {
		medan string
		set   func(*Data)
	}{
		{"ActivityTitle", func(d *Data) { d.ActivityTitle = "锦标赛" }},
		{"CategoryName", func(d *Data) { d.CategoryName = "羽毛球" }},
		{"Serial", func(d *Data) { d.Serial = "MARC-2026-证书" }},
	}
	for _, tt := range tests {
		t.Run(tt.medan, func(t *testing.T) {
			d := testData()
			tt.set(&d)

			_, err := GeneratePDF(d)
			if err == nil {
				t.Fatalf("mahu ralat untuk %s tak boleh dikodkan, dapat nil", tt.medan)
			}
			// Ralat mesti menamakan medan yang salah — Task 9 memaparkannya
			// kepada pengurusan, yang perlu tahu apa hendak dibetulkan.
			if !strings.Contains(err.Error(), tt.medan) {
				t.Errorf("ralat tidak menamakan medan %s: %v", tt.medan, err)
			}
		})
	}
}

// Medan yang sah sepenuhnya mesti masih menjana PDF selepas semakan
// diperluas — semakan yang menolak segalanya juga lulus ujian di atas.
func TestGeneratePDFTerimaSemuaMedanSah(t *testing.T) {
	d := testData()
	d.ActivityTitle = "Kejohanan Böla Sepak Piala José 2026"
	d.CategoryName = "Bola Sepak"

	if _, err := GeneratePDF(d); err != nil {
		t.Fatalf("GeneratePDF dengan medan beraksen sah: %v", err)
	}
}

// fpdf tidak memotong teks yang melebihi sel — ia melimpah melepasi
// bingkai sijil. Tanpa clip, tajuk aktiviti yang panjang merosakkan
// susun atur secara senyap.
func TestClipPotongTeksTerlaluLebar(t *testing.T) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 16)

	panjang := strings.Repeat("Kejohanan Badminton Terbuka Kebangsaan ", 5)
	got := clip(pdf, panjang, contentW)
	if got == panjang {
		t.Fatal("teks panjang tidak dipotong langsung")
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("teks dipotong tanpa elipsis: %q", got)
	}
	if w := pdf.GetStringWidth(got); w > contentW {
		t.Errorf("teks dipotong masih melimpah: %.1fmm > %.1fmm", w, contentW)
	}

	pendek := "Kejohanan Badminton Terbuka 2026"
	if got := clip(pdf, pendek, contentW); got != pendek {
		t.Errorf("teks muat diubah: %q -> %q", pendek, got)
	}
}

func TestEncodableName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Ahmad bin Abdullah", true},
		{"Nurul Aisyah binti Zainal", true},
		{"José Álvarez", true}, // cp1252 meliputi aksara beraksen Latin
		{"李小龍", false},
		{"Ahmad 李", false},
		// cp1252 memetakan rune ini walaupun ia di atas 0xFF. Nama yang
		// disalin dari Word atau iOS membawa apostrof pintar; menolaknya
		// bermakna tiada sijil dikeluarkan untuk sebab yang salah.
		{"Nur’ain binti Ismail", true},
		{"Ali – Ketua Pasukan", true},
		// Titik sebenar bukan aksara rosak.
		{"Ahmad b. Abdullah", true},
	}
	for _, tt := range tests {
		if got := EncodableName(tt.name); got != tt.want {
			t.Errorf("EncodableName(%q) = %v, mahu %v", tt.name, got, tt.want)
		}
	}
}
