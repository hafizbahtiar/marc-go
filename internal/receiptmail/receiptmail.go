// Package receiptmail hantar emel resit - yuran pendaftaran, yuran
// aktiviti, ATAU donation - selepas webhook gateway sahkan bayaran
// berjaya.
//
// SATU skeleton HTML + SATU fungsi `Send` dikongsi SEMUA jenis resit
// (bukan satu templat/fungsi berasingan setiap jenis macam sebelum ni -
// `donationReceiptHTML`/`sendReceiptEmail` dalam `donations.go` dan
// `feeReceiptHTML`/`Send` dalam package ni sendiri hampir 100% sama
// struktur HTML, cuma teks/label berbeza). Tambah jenis resit baharu
// kelak = tambah SATU entri `kindConfigs`, bukan tulis fungsi HTML
// baharu.
//
// SENGAJA package BERASINGAN drpd `internal/receipt` (jana PDF, tiada
// kebergantungan emel) dan drpd `internal/http/handlers` (tiga laluan
// webhook - registration_payment.go/activity_registration_payment.go/
// donations.go - tak perlu import satu sama lain atau salin-tampal
// logik emel tiga kali). Loose coupling dua hala:
//   - Package ni TAK TAHU langsung pasal `receipt.FeePayment`/
//     `receipt.Donation` (struct PDF yang berbeza bentuk - cth
//     GatewayChargeCents cuma wujud pada satu) - caller jana PDF
//     SENDIRI (guna generator yang sesuai dgn Kind dia) dan hantar
//     bait siap jadi (`Receipt.PDFBytes`), package ni cuma lampir +
//     hantar.
//   - Caller (webhook) hantar SATU struct [Receipt] - package ni tak
//     tahu/tak kisah ia dari pendaftaran ahli, aktiviti, atau
//     donation; `Kind` je yang tentukan copy/label yang dipakai.
package receiptmail

import (
	"context"
	"fmt"
	htmlpkg "html"
	"log"
	"strings"
	"time"

	"marc/internal/email"
	"marc/internal/receipt"
)

// Kind - jenis resit. String (bukan iota int) supaya log mesej terus
// boleh dibaca (`kind=activity_fee`, bukan `kind=1`) - padanan gaya
// `paymentlog.Module*` (internal/paymentlog) yang guna string sama
// corak, walaupun package ni sengaja TAK import paymentlog terus
// (elak kebergantungan yang tak diperlukan - Kind di sini pasal
// PILIH TEMPLAT emel, bukan audit trail).
type Kind string

const (
	KindRegistrationFee Kind = "registration_fee"
	KindActivityFee     Kind = "activity_fee"
	KindDonation        Kind = "donation"
)

// Receipt - parameter loosely-coupled untuk [Send]. Caller (webhook
// handler) isi struct ni drpd baris DB yang dia dah ada; package ni
// sendiri TAK PERNAH query DB atau jana PDF, cuma render HTML + hantar
// drpd nilai yang dihantar.
type Receipt struct {
	Kind      Kind
	To        string
	PayerName string

	// Purpose - teks penuh dipaparkan pada perenggan pengenalan emel
	// untuk kind FEE sahaja (cth "Yuran Pendaftaran Ahli" atau tajuk
	// aktiviti sebenar) - DIABAIKAN untuk KindDonation (perenggan
	// donation statik, tiada slot purpose - lihat kindConfigs).
	Purpose string

	AmountCents int64
	Currency    string
	GatewayRef  string

	// FallbackID - dipakai `receipt.Filename` HANYA bila GatewayRef
	// kosong (jaring keselamatan, bukan kes dijangka - [Send] dipanggil
	// lepas status 'succeeded'/'paid' sahaja). Donation
	// hantar id baris (UUID) di sini (padanan tingkah laku asal
	// `donations.go`); dua kind fee tinggalkan kosong (padanan asal
	// `receiptmail.Send` - GatewayRef fee SENTIASA diisi selepas
	// status 'succeeded', jadi fallback tak pernah kena guna).
	FallbackID string

	PaidAt time.Time

	// PDFBytes - caller jana PDF SENDIRI (`receipt.GenerateFeePDF` utk
	// kind fee, `receipt.GeneratePDF` utk KindDonation - dua struct
	// parameter tu bentuk BERBEZA, itu sebab package ni tak generate
	// PDF sendiri) dan hantar bait siap di sini. `nil` = jana PDF gagal
	// di caller (log di sana, bukan di sini) - emel tetap dihantar,
	// versi HTML sahaja tanpa lampiran.
	PDFBytes []byte
}

// kindCopy - teks/label yang BERBEZA setiap Kind. Semua medan lain
// (jalur header, panel jumlah, baris ref/tarikh) DIKONGSI SATU
// skeleton HTML (`htmlSkeleton`) - tambah Kind baharu = tambah SATU
// entri kindConfigs, tak perlu tulis HTML baharu.
type kindCopy struct {
	// label - digunakan DUA tempat: (1) `receipt.Filename(label, ...)`
	// bina nama fail lampiran ("Resit-{label}-MARC-{ref}.pdf"), (2)
	// subject emel rujuk label ni scr tersirat (subject sendiri
	// STATIK setiap kind, lihat medan `subject`).
	label   string
	subject string
	tagline string

	// intro - perenggan SELEPAS "Terima kasih, {nama}." - terima
	// (safeName, safePurpose) YANG SUDAH di-html.EscapeString, pulang
	// HTML siap sedia utk disisip terus (fungsi, bukan templat
	// Sprintf, sengaja - elak footgun bilangan `%s` tak padan bila
	// sesetengah kind tak perlukan purpose langsung, cth donation).
	intro func(safeName, safePurpose string) string

	amountLabel string
	footerHTML  string

	// defaultPayerName - fallback bila PayerName kosong. Beza sengaja
	// antara kind (padanan tingkah laku ASAL sebelum refactor ni):
	// fee kata "Ahli MARC", donation kata "Penyumbang" - donation
	// terima sumbangan anonymous (tiada akaun ahli), "Ahli MARC" utk
	// kes tu salah konteks.
	defaultPayerName string
}

const feeFooterHTML = `<p style="margin:0;font-size:12px;color:#6B6B6B;line-height:1.5;">
            Emel ini dihantar automatik oleh sistem MARC. Sila simpan resit
            PDF terlampir untuk rekod anda.
          </p>`

const donationFooterHTML = `<p style="margin:0 0 10px;font-size:12px;color:#6B6B6B;line-height:1.5;">
            Sumbangan ini diberikan secara peribadi kepada pembangun MARC.
            Ia <strong>bukan</strong> sumbangan kepada MAIWP atau mana-mana
            badan amal, dan tidak layak untuk pelepasan cukai.
          </p>
          <p style="margin:0;font-size:12px;color:#6B6B6B;line-height:1.5;">
            Emel ini dihantar automatik oleh sistem MARC. Sila simpan resit
            PDF terlampir untuk rekod anda.<br>
            &mdash; Hafiz, pembangun MARC
          </p>`

// feeIntro - perenggan sama utk KEDUA-DUA kind fee (pendaftaran dan
// aktiviti); cuma `Purpose` (activity title / "Yuran Pendaftaran
// Ahli") yang beza antara dua panggilan, bukan copy sekeliling dia.
func feeIntro(_, safePurpose string) string {
	return fmt.Sprintf(`Pembayaran anda untuk <strong>%s</strong> dah disahkan berjaya.`, safePurpose)
}

// kindConfigs - SATU tempat tambah Kind baharu. `map` (bukan slice/
// switch) supaya lookup di [Send] pulang "Kind tak dikenali" dgn jelas
// (`ok == false`) drpd senyap jatuh ke kes lalai yang salah.
var kindConfigs = map[Kind]kindCopy{
	KindRegistrationFee: {
		label:            "Pendaftaran",
		subject:          "Resit Yuran Pendaftaran MARC",
		tagline:          "Bukti pembayaran yuran kelab",
		intro:            feeIntro,
		amountLabel:      "Jumlah Dibayar",
		footerHTML:       feeFooterHTML,
		defaultPayerName: "Ahli MARC",
	},
	KindActivityFee: {
		label:            "Aktiviti",
		subject:          "Resit Yuran Aktiviti MARC",
		tagline:          "Bukti pembayaran yuran kelab",
		intro:            feeIntro,
		amountLabel:      "Jumlah Dibayar",
		footerHTML:       feeFooterHTML,
		defaultPayerName: "Ahli MARC",
	},
	KindDonation: {
		label:   "Sokongan",
		subject: "Terima kasih kerana menyokong MARC",
		tagline: "Resit sokongan penyelenggaraan",
		intro: func(_, _ string) string {
			// Perenggan STATIK - donation TIADA slot purpose (beza
			// drpd fee) sebab ia bukan bayaran utk satu perkara
			// spesifik, ia sokongan am kpd penyelenggaraan app.
			return `Sokongan anda untuk MARC dah selamat diterima. Duit ni pergi
            terus kepada saya untuk menampung kos hosting, domain dan masa
            penyelenggaraan supaya app ni kekal berjalan dan percuma untuk
            semua ahli.`
		},
		amountLabel:      "Jumlah Sokongan",
		footerHTML:       donationFooterHTML,
		defaultPayerName: "Penyumbang",
	},
}

// htmlSkeleton - SATU jalur/panel/susun atur dikongsi SEMUA kind.
// Inline style sengaja (bukan `<style>`/class) - ramai email client
// (Gmail, Outlook) buang `<style>` block atau CSS luaran. Slot (ikut
// turutan `%s`): tagline, safeName (tajuk salam), introHTML, amountLabel,
// amount, safeRef, tarikh, footerHTML.
const htmlSkeleton = `<!doctype html>
<html>
<body style="margin:0;padding:0;background-color:#FAF9F6;font-family:Helvetica,Arial,sans-serif;color:#1C1B19;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#FAF9F6;padding:32px 16px;">
    <tr><td align="center">
      <table role="presentation" width="100%%" style="max-width:480px;background-color:#FFFFFF;border-radius:12px;overflow:hidden;">
        <tr><td style="background-color:#2F6B4F;padding:24px 32px;">
          <span style="font-size:20px;font-weight:700;color:#FFFFFF;letter-spacing:0.5px;">MARC</span>
          <div style="margin-top:4px;font-size:12px;color:#D7E5DC;">%s</div>
        </td></tr>
        <tr><td style="padding:32px;">
          <p style="margin:0 0 16px;font-size:15px;">Terima kasih, %s.</p>
          <p style="margin:0 0 16px;font-size:15px;line-height:1.5;">
            %s
          </p>
          <p style="margin:0 0 24px;font-size:15px;line-height:1.5;">
            Resit (PDF) dilampirkan bersama emel ini.
          </p>
          <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#FAF9F6;border-radius:10px;margin-bottom:24px;">
            <tr><td style="padding:20px 24px;">
              <p style="margin:0 0 4px;font-size:12px;color:#6B6B6B;text-transform:uppercase;letter-spacing:0.5px;">%s</p>
              <p style="margin:0;font-size:28px;font-weight:700;color:#1C1B19;">%s</p>
            </td></tr>
          </table>
          <p style="margin:0 0 4px;font-size:12px;color:#6B6B6B;">No. Rujukan</p>
          <p style="margin:0 0 16px;font-size:14px;color:#1C1B19;">%s</p>
          <p style="margin:0 0 4px;font-size:12px;color:#6B6B6B;">Tarikh</p>
          <p style="margin:0;font-size:14px;color:#1C1B19;">%s</p>
        </td></tr>
        <tr><td style="padding:20px 32px;border-top:1px solid #E4E1DA;">
          %s
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`

// renderHTML - `html.EscapeString` pada SEMUA nilai user-supplied yang
// landing dlm HTML (nama, purpose, DAN ref - ref pun kini di-escape
// walau ia gateway-controlled/tak pernah bawa teks penyerang serang
// pada hari ni, sebab ia dah lalui pengesahan server-side ToyyibPay/
// Stripe sebelum sampai sini; escape defensif konsisten dgn dua medan
// sebelah dia, bukan sebab ada laluan eksploit dijumpai). Tanpa escape
// pada mana-mana nilai ni, jadi HTML injection vector dlm emel yang
// kita hantar.
func renderHTML(k kindCopy, name, purpose, amount, ref string, paidAt time.Time) string {
	safeName := htmlpkg.EscapeString(name)
	safePurpose := htmlpkg.EscapeString(purpose)
	safeRef := htmlpkg.EscapeString(ref)
	introHTML := k.intro(safeName, safePurpose)
	return fmt.Sprintf(htmlSkeleton, k.tagline, safeName, introHTML, k.amountLabel, amount, safeRef, receipt.FormatDateTime(paidAt), k.footerHTML)
}

// Send jana emel HTML (+ lampir PDF kalau `r.PDFBytes` bukan nil),
// hantar. TAK PERNAH gagal secara boleh nampak kepada caller (webhook
// mesti tetap pulang 200 ke gateway tak kira emel berjaya/gagal) -
// kegagalan cuma di-log.
//
// `emailClient.SendWithAttachments` sendiri no-op senyap kalau
// credential Resend belum diisi (`Client.Enabled()`), jadi caller tak
// perlu semak konfigurasi sebelum panggil [Send].
func Send(ctx context.Context, emailClient *email.Client, r Receipt) {
	if r.To == "" {
		// Sepatutnya tak berlaku - caller (webhook) MESTI dah sahkan
		// profil/penerima wujud sebelum panggil Send. Jangan panic,
		// log sahaja.
		log.Printf("resit emel %s: tiada alamat penerima (ref=%s)", r.Kind, r.GatewayRef)
		return
	}

	cfg, ok := kindConfigs[r.Kind]
	if !ok {
		// Sepatutnya tak berlaku - Kind cuma set oleh kod dalam repo
		// ni sendiri (bukan input luaran). Fail-loud dlm log (bukan
		// panic) supaya Kind baharu yang terlepas tambah entri
		// kindConfigs cepat ketara, bukan senyap hantar emel kosong.
		log.Printf("resit emel: Kind %q tak dikenali dlm kindConfigs - emel TAK dihantar (ref=%s)", r.Kind, r.GatewayRef)
		return
	}

	displayName := r.PayerName
	if displayName == "" {
		displayName = cfg.defaultPayerName
	}

	body := renderHTML(cfg, displayName, r.Purpose, formatRinggit(r.AmountCents, r.Currency), r.GatewayRef, r.PaidAt)

	var attachments []email.Attachment
	if r.PDFBytes != nil {
		attachments = append(attachments, email.Attachment{
			Filename: receipt.Filename(cfg.label, r.GatewayRef, r.FallbackID),
			Content:  r.PDFBytes,
		})
	}

	if err := emailClient.SendWithAttachments(ctx, r.To, cfg.subject, body, attachments); err != nil {
		log.Printf("gagal hantar resit emel %s (ref=%s): %v", r.Kind, r.GatewayRef, err)
	}
}

// formatRinggit - SATU salinan dikongsi (dulu ada DUA: satu private
// dlm donations.go, satu private dlm package ni - bersatu di sini
// sebab kedua-dua caller kini funnel lalui [Send]).
//
// `strings.ToLower(currency)` WAJIB sebelum banding - `activities.
// currency` default UPPERCASE ('MYR', lihat migration
// 20260810100100_create_activities.sql), manakala `donations.currency`/
// `registration_payments.currency` default lowercase ('myr'). Tanpa
// lower-case dulu, banding `currency != "myr"` silap anggap "MYR"
// (huruf besar) sbg mata wang ASING dan cetak "MYR35.00" bukan
// "RM35.00" - emel resit yuran AKTIVITI akan papar jumlah berbeza drpd
// lampiran PDF-nya sendiri (`receipt.formatAmount` dah betul buat
// lower-case dulu; bug ni cuma di formatter emel). Ditemui Opus verify
// 2026-08-24 pada versi SEBELUM refactor ni - dibaiki serentak sekali
// gus bersatu jadi satu fungsi.
func formatRinggit(cents int64, currency string) string {
	symbol := "RM"
	if c := strings.ToLower(currency); c != "" && c != "myr" {
		symbol = strings.ToUpper(currency)
	}
	return fmt.Sprintf("%s%.2f", symbol, float64(cents)/100)
}
