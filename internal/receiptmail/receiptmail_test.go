package receiptmail

import (
	"strings"
	"testing"
	"time"
)

func TestFormatRinggit(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{1000, "myr", "RM10.00"},
		{1000, "", "RM10.00"},
		{2500, "usd", "USD25.00"},
		// `activities.currency` default UPPERCASE ('MYR', beza drpd
		// `donations`/`registration_payments` yang lowercase 'myr') -
		// tanpa lower-case dulu sebelum banding, ni tersalah anggap
		// mata wang ASING dan cetak "MYR35.00" bukan "RM35.00" (Opus
		// verify 2026-08-24, dijumpai pada versi SEBELUM refactor ni).
		{3500, "MYR", "RM35.00"},
	}
	for _, c := range cases {
		if got := formatRinggit(c.cents, c.currency); got != c.want {
			t.Errorf("formatRinggit(%d, %q) = %q, mahu %q", c.cents, c.currency, got, c.want)
		}
	}
}

// Nama/tajuk aktiviti (Purpose)/ref datang drpd input pengguna/gateway -
// tanpa escape, jadi HTML injection vector dlm emel yang kita hantar.
func TestRenderHTMLEscapeXSS(t *testing.T) {
	html := renderHTML(
		kindConfigs[KindRegistrationFee],
		`<script>alert(1)</script>`,
		`Kejohanan "Best" <b>2026</b>`,
		"RM10.00",
		`<img src=x onerror=alert(1)>`,
		time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
	)
	if strings.Contains(html, "<script>") {
		t.Fatal("nama tak di-escape - <script> tag mentah lolos ke HTML emel")
	}
	if strings.Contains(html, `<b>2026</b>`) {
		t.Fatal("purpose tak di-escape - tag HTML mentah lolos ke HTML emel")
	}
	if strings.Contains(html, "<img src=x") {
		t.Fatal("gateway_ref tak di-escape - tag HTML mentah lolos ke HTML emel")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("nama sepatutnya muncul dlm bentuk di-escape")
	}
}

// Merger templat (fee + donation -> satu skeleton) senang tercemar
// silang tanpa disedari - kunci copy PALING sensitif setiap kind: nota
// MAIWP (disclaimer undang-undang) WAJIB pada donation, WAJIB TIADA
// pada fee (yuran ialah bayaran rasmi kelab, bukan sumbangan peribadi).
func TestRenderHTMLKindCopyTidakBercampur(t *testing.T) {
	donationHTML := renderHTML(kindConfigs[KindDonation], "Ali", "", "RM10.00", "ref1", time.Now())
	if !strings.Contains(donationHTML, "MAIWP") {
		t.Error("resit donation sepatutnya bawa disclaimer MAIWP")
	}
	if !strings.Contains(donationHTML, "Jumlah Sokongan") {
		t.Error("resit donation sepatutnya label \"Jumlah Sokongan\"")
	}
	if strings.Contains(donationHTML, "Jumlah Dibayar") {
		t.Error("resit donation TAK patut bawa label fee \"Jumlah Dibayar\"")
	}

	for _, k := range []Kind{KindRegistrationFee, KindActivityFee} {
		feeHTML := renderHTML(kindConfigs[k], "Ali", "Yuran Pendaftaran Ahli", "RM10.00", "ref1", time.Now())
		if strings.Contains(feeHTML, "MAIWP") {
			t.Errorf("resit fee (%s) TAK patut bawa disclaimer MAIWP - itu konteks donation sahaja", k)
		}
		if !strings.Contains(feeHTML, "Jumlah Dibayar") {
			t.Errorf("resit fee (%s) sepatutnya label \"Jumlah Dibayar\"", k)
		}
		if strings.Contains(feeHTML, "Jumlah Sokongan") {
			t.Errorf("resit fee (%s) TAK patut bawa label donation \"Jumlah Sokongan\"", k)
		}
	}
}

func TestSendKindTakDikenaliTakPanic(t *testing.T) {
	// Kind rekaan (bukan salah satu KindRegistrationFee/KindActivityFee/
	// KindDonation) - kindConfigs lookup gagal (`ok == false`). Send
	// mesti log + return, BUKAN panic/pulang ralat ke caller.
	Send(t.Context(), nil, Receipt{Kind: Kind("tidak-wujud"), To: "a@b.com"})
}
