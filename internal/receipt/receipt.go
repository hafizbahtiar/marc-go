// Package receipt jana PDF resit donation — dilampirkan pada emel resit
// (lihat internal/http/handlers/donations.go `sendReceiptEmail`).
package receipt

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// Warna jenama MARC (padanan AppSemanticColors/ColorScheme di
// marc_flutter/lib/app/theme.dart).
var (
	brandColor = [3]int{47, 107, 79}   // #2F6B4F — hijau jenama
	brandDark  = [3]int{35, 82, 60}    // #23523C — jalur bawah header
	tintColor  = [3]int{238, 244, 240} // #EEF4F0 — panel jumlah
	inkColor   = [3]int{28, 27, 25}    // #1C1B19
	mutedColor = [3]int{107, 107, 107} // #6B6B6B
	lineColor  = [3]int{228, 225, 218} // #E4E1DA
	zebraColor = [3]int{250, 249, 246} // #FAF9F6 — baris jadual berselang
)

// Geometri muka surat A4 (mm).
const (
	pageW     = 210.0
	marginX   = 18.0
	contentW  = pageW - 2*marginX // 174
	headerH   = 40.0
	labelColW = 52.0

	// PENTING: sumbangan ni pergi kepada pembangun MARC secara peribadi,
	// BUKAN kepada MAIWP. Resit tak boleh nampak macam resit rasmi
	// organisasi — nota ni yang jelaskan bezanya, jangan buang.
	footerNote = "Sumbangan ini diberikan secara peribadi kepada pembangun " +
		"aplikasi MARC bagi menampung kos hosting, domain dan penyelenggaraan. " +
		"Ia BUKAN sumbangan kepada MAIWP atau mana-mana badan amal, dan TIDAK " +
		"layak untuk pelepasan cukai. Resit ini dijana secara automatik dan sah " +
		"tanpa tandatangan — sila simpan untuk rekod peribadi anda."
)

// malaysiaTZ — resit sentiasa dipaparkan dalam waktu Malaysia tanpa
// mengira zon masa server. FixedZone (bukan LoadLocation) supaya tak
// bergantung pada tzdata dalam imej container yang nipis.
var malaysiaTZ = time.FixedZone("MYT", 8*60*60)

// Donation — subset field donation yang perlu untuk resit. Diasingkan
// drpd sqlc.Donation supaya package ni tak perlu import sqlc/pgtype.
type Donation struct {
	MemberID    string // "" kalau anonymous/guest
	DonorName   string
	DonorEmail  string
	AmountCents int64
	Currency    string
	GatewayRef  string
	PaidAt      time.Time
}

// GeneratePDF bina resit satu muka surat: jalur header berjenama,
// blok penderma, panel jumlah (angka + perkataan), jadual butiran, dan
// footer nota.
func GeneratePDF(d Donation) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginX, marginX, marginX)
	pdf.SetAutoPageBreak(true, 20)
	pdf.SetTitle("Resit Sokongan MARC "+d.GatewayRef, true)
	pdf.SetAuthor("Hafiz — Pembangun MARC", true)

	// Font teras fpdf guna cp1252, bukan UTF-8. Tanpa penterjemah ni nama
	// penderma berdiakritik ("Aisyah Zulkifli" vs "Aïsyah") jadi sampah.
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	pdf.AddPage()
	drawHeader(pdf, tr, d)
	drawDonorBlock(pdf, tr, d)
	drawAmountPanel(pdf, tr, d)
	drawDetailsTable(pdf, tr, d)
	drawFooter(pdf, tr)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// drawHeader — jalur penuh lebar warna jenama: wordmark di kiri, jenis
// dokumen + no. rujukan di kanan.
func drawHeader(pdf *fpdf.Fpdf, tr func(string) string, d Donation) {
	pdf.SetFillColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.Rect(0, 0, pageW, headerH, "F")
	pdf.SetFillColor(brandDark[0], brandDark[1], brandDark[2])
	pdf.Rect(0, headerH, pageW, 1.6, "F")

	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(marginX, 11)
	pdf.SetFont("Helvetica", "B", 24)
	pdf.CellFormat(contentW/2, 10, "MARC", "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(contentW/2, 5, tr("Sokongan penyelenggaraan aplikasi"), "", 0, "L", false, 0, "")

	pdf.SetXY(pageW/2, 12)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(pageW/2-marginX, 7, "RESIT SOKONGAN", "", 2, "R", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(pageW/2-marginX, 5, tr("No. Rujukan  "+fallback(d.GatewayRef, "-")), "", 0, "R", false, 0, "")

	pdf.SetY(headerH + 12)
}

// drawDonorBlock — "Diterima daripada" di kiri, tarikh + status di kanan.
func drawDonorBlock(pdf *fpdf.Fpdf, tr func(string) string, d Donation) {
	top := pdf.GetY()
	colW := contentW / 2

	label(pdf, "DITERIMA DARIPADA", colW)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.CellFormat(colW, 7, tr(clip(pdf, fallback(d.DonorName, "Penyumbang Awanama"), colW)), "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.CellFormat(colW, 5.5, tr(clip(pdf, fallback(d.DonorEmail, "-"), colW)), "", 2, "L", false, 0, "")
	bottom := pdf.GetY()

	pdf.SetXY(marginX+colW, top)
	label(pdf, "TARIKH", colW)
	pdf.SetX(marginX + colW)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.CellFormat(colW, 7, tr(formatDateTime(d.PaidAt)), "", 2, "L", false, 0, "")

	// Lencana status — sumbangan hanya diresitkan selepas 'succeeded',
	// jadi ia sentiasa BERJAYA di sini.
	pdf.SetX(marginX + colW)
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetFillColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.SetTextColor(255, 255, 255)
	pdf.CellFormat(28, 6, "BERJAYA", "", 1, "C", true, 0, "")

	if pdf.GetY() > bottom {
		bottom = pdf.GetY()
	}
	pdf.SetY(bottom + 8)
}

// drawAmountPanel — panel bertona: jumlah dalam angka + dalam perkataan
// (amalan standard resit di Malaysia).
func drawAmountPanel(pdf *fpdf.Fpdf, tr func(string) string, d Donation) {
	words := amountInWords(d.AmountCents, d.Currency)
	panelH := 24.0
	if words != "" {
		panelH = 32
	}

	top := pdf.GetY()
	pdf.SetFillColor(tintColor[0], tintColor[1], tintColor[2])
	pdf.Rect(marginX, top, contentW, panelH, "F")
	pdf.SetFillColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.Rect(marginX, top, 2, panelH, "F") // aksen tepi kiri

	pdf.SetXY(marginX+8, top+5)
	label(pdf, "JUMLAH SOKONGAN", contentW-16)
	pdf.SetX(marginX + 8)
	pdf.SetFont("Helvetica", "B", 24)
	pdf.SetTextColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.CellFormat(contentW-16, 11, formatAmount(d.AmountCents, d.Currency), "", 2, "L", false, 0, "")

	if words != "" {
		pdf.SetX(marginX + 8)
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
		pdf.MultiCell(contentW-16, 4.5, tr(words), "", "L", false)
	}

	pdf.SetY(top + panelH + 10)
}

// drawDetailsTable — jadual dua lajur berselang warna, dengan baris
// kepala berjenama.
func drawDetailsTable(pdf *fpdf.Fpdf, tr func(string) string, d Donation) {
	// Nama/emel/tarikh sengaja TAK diulang di sini — dah ada dalam blok
	// penderma di atas. Jadual ni khusus butiran transaksi.
	rows := [][2]string{
		{"No. Rujukan Transaksi", fallback(d.GatewayRef, "-")},
		{"No. Ahli MARC", fallback(d.MemberID, "Tiada (penyumbang awam)")},
		{"Penerima", "Hafiz - Pembangun MARC"},
		{"Tujuan", "Penyelenggaraan aplikasi"},
		{"Kaedah Pembayaran", "Dalam talian"},
		{"Mata Wang", strings.ToUpper(fallback(d.Currency, "MYR"))},
		{"Status Pembayaran", "Berjaya"},
	}

	label(pdf, "BUTIRAN TRANSAKSI", contentW)
	pdf.Ln(1)

	const rowH = 8.5
	pdf.SetDrawColor(lineColor[0], lineColor[1], lineColor[2])
	pdf.SetLineWidth(0.2)

	for i, row := range rows {
		if i%2 == 1 {
			pdf.SetFillColor(zebraColor[0], zebraColor[1], zebraColor[2])
			pdf.Rect(marginX, pdf.GetY(), contentW, rowH, "F")
		}
		pdf.SetX(marginX)
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
		pdf.CellFormat(labelColW, rowH, tr(row[0]), "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "B", 10)
		pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
		valueW := contentW - labelColW
		pdf.CellFormat(valueW, rowH, tr(clip(pdf, row[1], valueW)), "", 1, "L", false, 0, "")

		y := pdf.GetY()
		pdf.Line(marginX, y, marginX+contentW, y)
	}

	pdf.Ln(10)
}

func drawFooter(pdf *fpdf.Fpdf, tr func(string) string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.CellFormat(contentW, 6, tr("Terima kasih kerana menyokong MARC."), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.MultiCell(contentW, 4.5, tr(footerNote), "", "L", false)

	pdf.Ln(4)
	y := pdf.GetY()
	pdf.SetDrawColor(lineColor[0], lineColor[1], lineColor[2])
	pdf.Line(marginX, y, marginX+contentW, y)
	pdf.Ln(3)
	pdf.SetFont("Helvetica", "", 8)
	pdf.CellFormat(contentW, 4, tr("Dijana pada "+formatDateTime(time.Now())+" | MARC"), "", 1, "L", false, 0, "")
}

// label — kapsyen kecil huruf besar yang berulang di setiap seksyen.
// Kursor turun ke baris bawah tapi kekal pada x asal (ln=2).
func label(pdf *fpdf.Fpdf, text string, w float64) {
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.CellFormat(w, 5, text, "", 2, "L", false, 0, "")
}

// clip potong teks yang lebih lebar drpd sel (fpdf tak clip sendiri —
// teks panjang akan melimpah menindih lajur sebelah).
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

func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(malaysiaTZ).Format("2 Jan 2006, 3:04 PM") + " (MYT)"
}

// formatAmount papar jumlah dengan pemisah ribuan — "RM1,500.00", bukan
// "RM1500.00".
func formatAmount(cents int64, currency string) string {
	prefix := "RM"
	if c := strings.ToLower(currency); c != "" && c != "myr" {
		prefix = strings.ToUpper(currency) + " "
	}
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return fmt.Sprintf("%s%s%s.%02d", neg, prefix, groupThousands(cents/100), cents%100)
}

func groupThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// amountInWords — jumlah dieja dalam Bahasa Melayu, macam resit rasmi
// ("Ringgit Malaysia Satu Ribu Lima Ratus sahaja"). Kosong untuk mata
// wang selain MYR — ejaan Melayu tak sesuai untuk USD/SGD.
func amountInWords(cents int64, currency string) string {
	if c := strings.ToLower(currency); c != "" && c != "myr" {
		return ""
	}
	if cents <= 0 {
		return ""
	}

	ringgit, sen := cents/100, cents%100
	var parts []string
	if ringgit > 0 {
		parts = append(parts, malayNumber(ringgit))
	}
	if sen > 0 {
		if len(parts) > 0 {
			parts = append(parts, "dan")
		}
		parts = append(parts, malayNumber(sen), "sen")
	}
	return "Ringgit Malaysia " + titleCase(strings.Join(parts, " ")) + " sahaja"
}

var malayUnits = [...]string{"kosong", "satu", "dua", "tiga", "empat", "lima", "enam", "tujuh", "lapan", "sembilan"}

func malayNumber(n int64) string {
	switch {
	case n < 10:
		return malayUnits[n]
	case n == 10:
		return "sepuluh"
	case n == 11:
		return "sebelas"
	case n < 20:
		return malayUnits[n-10] + " belas"
	case n < 100:
		return join(malayUnits[n/10]+" puluh", n%10)
	case n < 200:
		return join("seratus", n%100)
	case n < 1000:
		return join(malayUnits[n/100]+" ratus", n%100)
	case n < 2000:
		return join("seribu", n%1000)
	case n < 1_000_000:
		return join(malayNumber(n/1000)+" ribu", n%1000)
	case n < 1_000_000_000:
		return join(malayNumber(n/1_000_000)+" juta", n%1_000_000)
	default:
		return join(malayNumber(n/1_000_000_000)+" bilion", n%1_000_000_000)
	}
}

func join(head string, rest int64) string {
	if rest == 0 {
		return head
	}
	return head + " " + malayNumber(rest)
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
