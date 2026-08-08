// Package receipt jana PDF resit donation — dilampirkan pada emel resit
// (lihat internal/http/handlers/donations.go `sendReceiptEmail`).
package receipt

import (
	"bytes"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
)

// Warna jenama MARC (padanan AppColors.accent di marc_flutter/lib/app/theme.dart).
var (
	brandColor = [3]int{47, 107, 79}   // #2F6B4F
	inkColor   = [3]int{28, 27, 25}    // #1C1B19
	mutedColor = [3]int{107, 107, 107} // #6B6B6B
	lineColor  = [3]int{228, 225, 218} // #E4E1DA
)

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

// GeneratePDF bina resit satu muka surat ringkas: header jenama, jumlah
// besar, jadual butiran, footer nota cukai/terima kasih.
func GeneratePDF(d Donation) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.AddPage()

	// Header jenama.
	pdf.SetFont("Helvetica", "B", 22)
	pdf.SetTextColor(brandColor[0], brandColor[1], brandColor[2])
	pdf.CellFormat(0, 12, "MARC", "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 11)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.CellFormat(0, 6, "Resit Sumbangan Rasmi", "", 1, "L", false, 0, "")
	pdf.Ln(4)

	pdf.SetDrawColor(lineColor[0], lineColor[1], lineColor[2])
	y := pdf.GetY()
	pdf.Line(20, y, 190, y)
	pdf.Ln(10)

	// Jumlah — besar & menonjol.
	pdf.SetFont("Helvetica", "B", 28)
	pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
	pdf.CellFormat(0, 14, formatAmount(d.AmountCents, d.Currency), "", 1, "L", false, 0, "")
	pdf.Ln(6)

	// Jadual butiran.
	rows := [][2]string{
		{"No. Rujukan", d.GatewayRef},
		{"Tarikh", d.PaidAt.Format("2 January 2006, 3:04 PM")},
		{"Nama Penderma", nonEmpty(d.DonorName, "-")},
		{"Emel", nonEmpty(d.DonorEmail, "-")},
	}
	if d.MemberID != "" {
		rows = append(rows, [2]string{"No. Ahli MARC", d.MemberID})
	}

	pdf.SetFont("Helvetica", "", 11)
	for _, row := range rows {
		pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
		pdf.CellFormat(45, 9, row[0], "", 0, "L", false, 0, "")
		pdf.SetTextColor(inkColor[0], inkColor[1], inkColor[2])
		pdf.CellFormat(0, 9, row[1], "", 1, "L", false, 0, "")
	}

	pdf.Ln(10)
	y = pdf.GetY()
	pdf.Line(20, y, 190, y)
	pdf.Ln(8)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(mutedColor[0], mutedColor[1], mutedColor[2])
	pdf.MultiCell(0, 5,
		"Terima kasih atas sumbangan anda kepada MARC. Resit ini dijana "+
			"secara automatik dan sah tanpa tandatangan. Sila simpan untuk "+
			"rekod peribadi anda.",
		"", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func formatAmount(cents int64, currency string) string {
	symbol := "RM"
	if currency != "" && currency != "myr" {
		symbol = currency
	}
	return fmt.Sprintf("%s%.2f", symbol, float64(cents)/100)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
