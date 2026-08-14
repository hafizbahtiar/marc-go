package certificate

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/go-pdf/fpdf"
	qrcode "github.com/skip2/go-qrcode"
)

// Ukuran A4 landskap dalam mm.
const (
	pageW    = 297.0
	pageH    = 210.0
	marginX  = 20.0
	contentW = pageW - 2*marginX
)

var (
	brandColor = [3]int{16, 94, 74}
	brandDark  = [3]int{9, 61, 48}
	inkColor   = [3]int{28, 30, 33}
	mutedColor = [3]int{110, 116, 122}
)

// Data — segala yang perlu untuk mencetak satu sijil. Semuanya sudah
// disnapshot oleh pemanggil; fungsi ini tidak membaca DB.
type Data struct {
	Serial        string
	RecipientName string
	ActivityTitle string
	CategoryName  string
	ActivityDate  time.Time
	VerifyURL     string
}

// Penterjemah cp1252 dikongsi untuk semakan pra-terbang.
//
// Ia penterjemah yang SAMA jenisnya dengan yang dipakai semasa mencetak,
// jadi semakan tidak boleh menyimpang daripada pengekod sebenar. Closure
// fpdf berkongsi satu bytes.Buffer dalaman (lihat repClosure dalam
// fpdf/util.go), jadi ia BUKAN selamat-goroutine — itu sebab mutex.
// Membinanya memakan ~260µs, jadi ia dibina sekali sahaja dan bukan pada
// setiap panggilan EncodableName.
var (
	cp1252Mu sync.Mutex
	cp1252   func(string) string
)

func translate(s string) string {
	cp1252Mu.Lock()
	defer cp1252Mu.Unlock()
	if cp1252 == nil {
		cp1252 = fpdf.New("P", "mm", "A4", "").UnicodeTranslatorFromDescriptor("")
	}
	return cp1252(s)
}

// unencodable — cari aksara pertama yang akan hilang bila dicetak.
//
// fpdf dengan fon terbina mengekod ke cp1252 dan menggantikan setiap rune
// yang tiada dalam peta itu dengan '.' secara SENYAP (repClosure,
// fpdf/util.go). Aktiviti bertajuk "锦标赛" akan diterbitkan sebagai "..."
// — sijil rosak yang tiada siapa perasan sehingga penerima membukanya.
//
// Kita menyemak dengan menterjemah dan membandingkan, bukan dengan
// meneka julat rune: cp1252 sebenarnya meliputi beberapa rune di atas
// 0xFF ('’' U+2019, '–' U+2013, '€' U+20AC, '•' U+2022), dan semakan
// julat akan menolak nama sah yang disalin dari Word atau iOS.
func unencodable(s string) (rune, bool) {
	out := translate(s)
	// repClosure menulis tepat satu bait bagi setiap rune. Kalau tidak,
	// penterjemah gagal dimuatkan dan mengembalikan teks asal — cetakan
	// pasti rosak, jadi tolak dan bukan luluskan secara senyap.
	if len(out) != len([]rune(s)) {
		return 0, true
	}
	i := 0
	for _, r := range s {
		if out[i] == '.' && r != '.' {
			return r, true
		}
		i++
	}
	return 0, false
}

// EncodableName — bolehkah nama ini dicetak tanpa kehilangan aksara?
func EncodableName(name string) bool {
	_, bad := unencodable(name)
	return !bad
}

func GeneratePDF(d Data) ([]byte, error) {
	// Setiap medan yang sampai kepada penterjemah disemak, bukan nama
	// sahaja — tajuk atau kategori yang rosak sama teruknya. VerifyURL
	// dikecualikan: ia masuk ke QR, tidak pernah dicetak sebagai teks.
	for _, f := range []struct{ nama, nilai string }{
		{"Serial", d.Serial},
		{"RecipientName", d.RecipientName},
		{"ActivityTitle", d.ActivityTitle},
		{"CategoryName", d.CategoryName},
	} {
		if r, bad := unencodable(f.nilai); bad {
			return nil, fmt.Errorf(
				"medan %s mengandungi aksara yang tidak boleh dicetak (%q): %q",
				f.nama, r, f.nilai)
		}
	}

	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(marginX, marginX, marginX)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetTitle("Sijil Penyertaan "+d.Serial, true)
	pdf.SetAuthor("MARC", true)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.AddPage()

	drawBorder(pdf)
	drawHeading(pdf, tr)
	drawRecipient(pdf, tr, d)
	if err := drawFooter(pdf, tr, d); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("hasilkan PDF: %w", err)
	}
	return buf.Bytes(), nil
}

func drawBorder(pdf *fpdf.Fpdf) {
	pdf.SetFillColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.Rect(0, 0, pageW, 14, "F")
	pdf.SetFillColor(brandDark[0], brandDark[1], brandDark[2])
	pdf.Rect(0, 14, pageW, 1.6, "F")

	pdf.SetDrawColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.SetLineWidth(0.6)
	pdf.Rect(10, 22, pageW-20, pageH-32, "D")
}

func drawHeading(pdf *fpdf.Fpdf, tr func(string) string) {
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(marginX, 3)
	pdf.SetFont("Helvetica", "B", 15)
	pdf.CellFormat(contentW, 8, "MARC", "", 0, "L", false, 0, "")

	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.SetXY(marginX, 42)
	pdf.SetFont("Helvetica", "B", 30)
	pdf.CellFormat(contentW, 14, tr("SIJIL PENYERTAAN"), "", 2, "C", false, 0, "")

	pdf.SetFont("Helvetica", "", 11)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.CellFormat(contentW, 8, tr("Dengan ini disahkan bahawa"), "", 2, "C", false, 0, "")
}

func drawRecipient(pdf *fpdf.Fpdf, tr func(string) string, d Data) {
	pdf.SetY(78)
	pdf.SetTextColor(brandDark[0], brandDark[1], brandDark[2])
	pdf.SetFont("Helvetica", "B", 26)
	pdf.CellFormat(contentW, 14, tr(clip(pdf, d.RecipientName, contentW)), "", 2, "C", false, 0, "")

	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(contentW, 8, tr("telah menyertai"), "", 2, "C", false, 0, "")

	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(contentW, 10, tr(clip(pdf, d.ActivityTitle, contentW)), "", 2, "C", false, 0, "")

	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 11)
	meta := fmt.Sprintf("%s  •  %s", d.CategoryName, formatTarikh(d.ActivityDate))
	pdf.CellFormat(contentW, 7, tr(clip(pdf, meta, contentW)), "", 2, "C", false, 0, "")
}

func drawFooter(pdf *fpdf.Fpdf, tr func(string) string, d Data) error {
	png, err := qrcode.Encode(d.VerifyURL, qrcode.Medium, 256)
	if err != nil {
		return fmt.Errorf("jana QR: %w", err)
	}
	// RegisterImageReader membaca dari memori — tiada fail sementara.
	pdf.RegisterImageOptionsReader("qr", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(png))
	pdf.ImageOptions("qr", pageW-marginX-28, pageH-58, 28, 28, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetXY(marginX, pageH-44)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(contentW/2, 5, tr("No. Sijil  "+d.Serial), "", 2, "L", false, 0, "")
	pdf.CellFormat(contentW/2, 5, tr("Imbas QR untuk mengesahkan sijil ini"), "", 2, "L", false, 0, "")
	return nil
}

// clip potong teks yang lebih lebar drpd sel (fpdf tak clip sendiri —
// teks panjang akan melimpah melepasi bingkai sijil). Sama seperti
// internal/receipt.clip; mesti dipanggil SELEPAS SetFont kerana
// GetStringWidth bergantung pada fon semasa.
func clip(pdf *fpdf.Fpdf, s string, maxW float64) string {
	maxW -= 2 // padding dalaman sel
	if pdf.GetStringWidth(s) <= maxW {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if pdf.GetStringWidth(string(runes)+"...") <= maxW {
			return string(runes) + "..."
		}
	}
	return s
}

var namaBulan = [...]string{
	"Januari", "Februari", "Mac", "April", "Mei", "Jun",
	"Julai", "Ogos", "September", "Oktober", "November", "Disember",
}

func formatTarikh(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), namaBulan[int(t.Month())-1], t.Year())
}
