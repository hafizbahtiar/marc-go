package handlers

import (
	"strings"
	"testing"
)

func TestVerificationEmailHTMLAdaPautanDanJenama(t *testing.T) {
	html := verificationEmailHTML(
		"ahli@example.com",
		"https://marc.hafizbahtiar.com/sahkan-emel?token=abc",
	)

	need := []string{
		"#2F6B4F",
		"Sahkan emel anda",
		"https://marc.hafizbahtiar.com/sahkan-emel?token=abc",
		"ahli@example.com",
		"1 jam",
		"MARC",
	}
	for _, s := range need {
		if !strings.Contains(html, s) {
			t.Errorf("html tak mengandungi %q", s)
		}
	}
}

func TestVerificationEmailHTMLEscapeEmel(t *testing.T) {
	html := verificationEmailHTML(
		`<script>alert(1)</script>@x.com`,
		"https://marc.hafizbahtiar.com/sahkan-emel?token=safe",
	)
	if strings.Contains(html, "<script>") {
		t.Fatal("emel pengguna tak di-escape — injection dalam HTML emel")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("emel pengguna patut muncul sebagai entiti HTML")
	}
}
