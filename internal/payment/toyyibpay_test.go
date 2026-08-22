package payment

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// Opus verify 2026-08-15 jumpa kelas bug SAMA lepas fix ';' di atas:
// url.ParseQuery JUGA tolak escape peratus tak sah (`%` mentah bukan
// diikuti dua heksadesimal) — sangat munasabah dalam medan teks bebas
// macam "100% off". ParseQuery pulang `values` SEBAHAGIAN + `err`
// serentak bila ini berlaku; billcode (medan lain, tak terjejas) mesti
// tetap dibaca daripada `values` walau `err != nil`.
func TestExtractBillCodePeratusTakSahDalamMedanLain(t *testing.T) {
	code := extractBillCode(
		[]byte("billcode=abc123&status=1&reason=100% off&order_id=xyz"),
		"application/x-www-form-urlencoded",
	)
	if code != "abc123" {
		t.Fatalf("billcode = %q, mahu abc123 (payload ada '%%' tak sah)", code)
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

// Disahkan LIVE 2026-08-22 pada bil produksi fmo34a9m: getBillTransactions
// pulang ARRAY — transaksi FPX berjaya (status "1") di HADAPAN, diikuti
// beberapa baris pending-alt ("4"). Ambil elemen terakhir semata-mata
// buat webhook/reconcile abaikan bayaran yang dah lepas.
func TestConfirmStatusUtamakanTransaksiBerjayaWalauBukanTerakhir(t *testing.T) {
	gw := gatewayWithTransactions(t, "\t\n\t\t"+`[{"billpaymentStatus":"1","billpaymentInvoiceNo":"TP1"},{"billpaymentStatus":"4","billpaymentInvoiceNo":"TP2"},{"billpaymentStatus":"4","billpaymentInvoiceNo":"TP3"}]`)

	event, err := gw.confirmStatus(context.Background(), "fmo34a9m")
	if err != nil {
		t.Fatalf("confirmStatus: %v (mahu succeeded, bukan abaikan status 4 terakhir)", err)
	}
	if event.Status != "succeeded" {
		t.Fatalf("status = %q, mahu succeeded", event.Status)
	}
	if event.GatewayRef != "fmo34a9m" {
		t.Fatalf("GatewayRef = %q, mahu fmo34a9m", event.GatewayRef)
	}
}

func TestConfirmStatusSemuaPendingAltDiabaikan(t *testing.T) {
	gw := gatewayWithTransactions(t, `[{"billpaymentStatus":"4"},{"billpaymentStatus":"4"}]`)

	_, err := gw.confirmStatus(context.Background(), "m4h10b26")
	if !errors.Is(err, ErrIgnoredEvent) {
		t.Fatalf("err = %v, mahu ErrIgnoredEvent", err)
	}
}

func TestConfirmStatusGagalKalauTiadaBerjaya(t *testing.T) {
	gw := gatewayWithTransactions(t, `[{"billpaymentStatus":"4"},{"billpaymentStatus":"3"}]`)

	event, err := gw.confirmStatus(context.Background(), "gagal")
	if err != nil {
		t.Fatalf("confirmStatus: %v, mahu failed", err)
	}
	if event.Status != "failed" {
		t.Fatalf("status = %q, mahu failed", event.Status)
	}
}

func gatewayWithTransactions(t *testing.T, body string) *ToyyibPayGateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewToyyibPayGateway(srv.URL, "secret", "cat", "https://cb.example", "https://ret.example")
}
