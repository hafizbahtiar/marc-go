package payment

import (
	"bytes"
	"mime/multipart"
	"testing"
)

func TestExtractBillCodeUrlEncoded(t *testing.T) {
	code := extractBillCode(
		[]byte("status=1&billcode=abc123&order_id=xyz"),
		"application/x-www-form-urlencoded",
	)
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123", code)
	}
}

// Disahkan LIVE 2026-08-15: callback ToyyibPay sebenar bawa `;` mentah
// dalam salah satu medan (msg/reason), yang buat url.ParseQuery tolak
// KESELURUHAN body sebelum ni. Padanan kes tepat yang bakar staging.
func TestExtractBillCodeSemicolonMentahDalamMedanLain(t *testing.T) {
	code := extractBillCode(
		[]byte("billcode=abc123&status=1&reason=Some; text with semicolon&order_id=xyz"),
		"application/x-www-form-urlencoded",
	)
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123 (payload ada ';' mentah)", code)
	}
}

func TestExtractBillCodeKunciBesarKecilCampur(t *testing.T) {
	cases := []string{
		"BillCode=abc123&status=1",
		"BILLCODE=abc123&status=1",
		"billCode=abc123&status=1",
	}
	for _, payload := range cases {
		code := extractBillCode([]byte(payload), "application/x-www-form-urlencoded")
		if code != "abc123" {
			t.Errorf("payload %q: billcode = %q, mahu abc123", payload, code)
		}
	}
}

func TestExtractBillCodeMultipart(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("billcode", "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("status", "1"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	code := extractBillCode(buf.Bytes(), w.FormDataContentType())
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123 (multipart)", code)
	}
}

func TestExtractBillCodeJSON(t *testing.T) {
	code := extractBillCode([]byte(`{"billcode":"abc123","status":"1"}`), "application/json")
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123 (JSON)", code)
	}
}

func TestExtractBillCodeJSONKunciBesar(t *testing.T) {
	code := extractBillCode([]byte(`{"BillCode":"abc123"}`), "application/json")
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123 (JSON, BillCode)", code)
	}
}

func TestExtractBillCodeTiadaBillcodeLangsung(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		contentType string
	}{
		{"form kosong", "status=1&order_id=xyz", "application/x-www-form-urlencoded"},
		{"JSON tiada billcode", `{"status":"1"}`, "application/json"},
		{"payload kosong", "", "application/x-www-form-urlencoded"},
	}
	for _, tc := range cases {
		code := extractBillCode([]byte(tc.payload), tc.contentType)
		if code != "" {
			t.Errorf("%s: billcode = %q, mahu kosong", tc.name, code)
		}
	}
}
