package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ToyyibPayGateway implements Gateway — dikhaskan untuk yuran ahli
// (belum wired ke mana-mana handler; skema dues dan gate itu sendiri
// bergantung 3 keputusan produk yang belum dibuat, lihat TODO.md bahagian
// Payment). Akaun BERASINGAN drpd Stripe (yang pegang donation).
//
// PENTING — sumber kajian, bukan dokumentasi rasmi terus:
// `toyyibpay.com/apireference/` sekat fetch langsung (403, kemungkinan
// bot-blocking) semasa kajian dibuat 2026-08-15. Butiran API di bawah
// disilang rujuk daripada dokumentasi komuniti + kod sumber terbuka pihak
// ketiga — SAHKAN semula terhadap sandbox (dev.toyyibpay.com) sebelum
// guna dalam produksi. Nota penuh + checklist: `marc_flutter/PAYMENT-
// TOYYIB.md`.
type ToyyibPayGateway struct {
	httpClient   *http.Client
	baseURL      string // https://toyyibpay.com (produksi) atau https://dev.toyyibpay.com (sandbox)
	secretKey    string
	categoryCode string
	// callbackURL/returnURL — route SEBENAR sejak 2026-08-15
	// (`/registration-payments/webhook/toyyibpay` dan `/registration-
	// payments/return/toyyibpay`, lihat `internal/http/router.go` dan
	// `RegistrationPaymentHandler`), dibina drpd PublicBaseURL dalam
	// `cmd/api/main.go`. Bukan placeholder lagi.
	callbackURL string
	returnURL   string
	configured  bool
}

func NewToyyibPayGateway(baseURL, secretKey, categoryCode, callbackURL, returnURL string) *ToyyibPayGateway {
	if secretKey == "" || categoryCode == "" {
		return &ToyyibPayGateway{configured: false}
	}
	if baseURL == "" {
		baseURL = "https://toyyibpay.com"
	}
	return &ToyyibPayGateway{
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		baseURL:      strings.TrimRight(baseURL, "/"),
		secretKey:    secretKey,
		categoryCode: categoryCode,
		callbackURL:  callbackURL,
		returnURL:    returnURL,
		configured:   true,
	}
}

func (t *ToyyibPayGateway) Name() string { return "toyyibpay" }

func (t *ToyyibPayGateway) Enabled() bool { return t.configured }

// CreatePayment cipta bil (createBill) dan pulang URL hosted-redirect —
// ToyyibPay BUKAN client-secret macam Stripe, pembayar navigasi KELUAR
// app ke halaman ToyyibPay sendiri. `RedirectURL` (bukan `ClientSecret`)
// yang diisi, padanan medan yang dah sedia di `CreateResult` khas untuk
// gateway berbentuk ni (lihat payment.go).
func (t *ToyyibPayGateway) CreatePayment(ctx context.Context, params CreateParams) (CreateResult, error) {
	if !t.configured {
		return CreateResult{}, ErrNotConfigured
	}

	// billName ≤30 aksara, billDescription ≤100 — had dari dokumentasi
	// komuniti (bukan disahkan rasmi), dipotong defensif di sini supaya
	// caller tak perlu tahu had ni.
	desc := params.Metadata["description"]
	if desc == "" {
		desc = "Yuran keahlian MARC"
	}
	if len(desc) > 100 {
		desc = desc[:100]
	}

	form := url.Values{}
	form.Set("userSecretKey", t.secretKey)
	form.Set("categoryCode", t.categoryCode)
	form.Set("billName", "Yuran MARC")
	form.Set("billDescription", desc)
	form.Set("billPriceSetting", "1") // 1 = jumlah tetap (bukan pembayar isi sendiri)
	form.Set("billPayorInfo", "1")
	form.Set("billAmount", strconv.FormatInt(params.AmountCents, 10)) // dalam SEN, padanan terus amount_cents — tiada penukaran unit
	form.Set("billPaymentChannel", "2")                               // 0=FPX, 1=kad, 2=dua-dua
	form.Set("billReturnUrl", t.returnURL)
	form.Set("billCallbackUrl", t.callbackURL)
	if ref := params.Metadata["reference"]; ref != "" {
		form.Set("billExternalReferenceNo", ref)
	}
	// billTo WAJIB (disahkan terhadap sandbox 2026-08-15 — createBill
	// pulang `{"status":"error","msg":"billTo parameter is empty"}` bila
	// tiada). Dokumentasi komuniti tak sebut ini wajib. billEmail/
	// billPhone belum disahkan wajib/tidak, tapi hantar kalau ada supaya
	// ToyyibPay boleh emel resit sendiri kalau `billContentEmail` diguna
	// kelak.
	billTo := params.Metadata["billTo"]
	if billTo == "" {
		billTo = "Ahli MARC"
	}
	form.Set("billTo", billTo)
	if email := params.Metadata["billEmail"]; email != "" {
		form.Set("billEmail", email)
	}
	if phone := params.Metadata["billPhone"]; phone != "" {
		form.Set("billPhone", phone)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/index.php/api/createBill", strings.NewReader(form.Encode()))
	if err != nil {
		return CreateResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return CreateResult{}, fmt.Errorf("toyyibpay createBill: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CreateResult{}, fmt.Errorf("toyyibpay createBill: baca respons: %w", err)
	}

	// Respons createBill (dokumentasi komuniti): array JSON bawa satu
	// objek, cth `[{"BillCode":"abc123"}]`. BELUM disahkan rasmi — kalau
	// bentuk sebenar berbeza, ralat di sini akan tunjuk raw body untuk
	// mudah diagnos semasa uji sandbox.
	var bills []struct {
		BillCode string `json:"BillCode"`
		Msg      string `json:"msg"`
	}
	if err := json.Unmarshal(body, &bills); err != nil || len(bills) == 0 || bills[0].BillCode == "" {
		snippet := string(body)
		// 500, bukan 300 — padan `maxPaymentLogMessage`
		// (paymentlog_helpers.go) supaya snippet ni tak dipotong dua kali
		// buat sia-sia (Opus verify 2026-08-15: 300 di sini + 500 di
		// handler bermakna 300 yang sebenarnya berkuat kuasa, buang
		// maklumat berguna kalau ToyyibPay pulang muka HTML ralat/WAF).
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		return CreateResult{}, fmt.Errorf("toyyibpay createBill: respons tak dijangka: %s", snippet)
	}

	billCode := bills[0].BillCode
	return CreateResult{
		GatewayRef:  billCode,
		RedirectURL: t.baseURL + "/" + billCode,
		RawResponse: string(body),
	}, nil
}

// VerifyWebhook — ToyyibPay TIADA sah kriptografi callback yang boleh
// dipercayai sepenuhnya. Kajian 2026-08-15 jumpa medan `hash` disebut
// dalam sesetengah sumber (nampak khusus DuitNow QR), tapi DUA sumber
// sekunder bagi FORMULA BERCANGGAH untuk kira hash tu, dan tiada
// pengesahan ia hadir pada SEMUA jenis callback (FPX/kad biasa). Body
// callback jadi TAK BOLEH dipercayai untuk credit terus.
//
// Reka bentuk (penyimpangan sengaja drpd pola Stripe — lihat
// PAYMENT-TOYYIB.md): callback body cuma diambil `billcode`, lepas tu
// status SEBENAR disahkan dengan poll `getBillTransactions` guna
// `userSecretKey` (kredential sisi pelayan sahaja, penghantar callback
// tak boleh palsukan). Ini bermakna `VerifyWebhook` untuk ToyyibPay buat
// panggilan rangkaian keluar sebelum pulang — TIDAK pure/local macam
// `StripeGateway.VerifyWebhook` (sah HMAC tempatan sahaja).
func (t *ToyyibPayGateway) VerifyWebhook(payload []byte, headers http.Header) (WebhookEvent, error) {
	if !t.configured {
		return WebhookEvent{}, ErrNotConfigured
	}

	billCode := extractBillCode(payload, headers.Get("Content-Type"))
	if billCode == "" {
		// Payload MENTAH dalam ralat (bukan cuma "billcode kosong") —
		// dokumentasi rasmi ToyyibPay tak boleh dibaca (403, lihat
		// PAYMENT-TOYYIB.md), dan bentuk callback sebenar dah SILAP
		// diandaikan dua kali berturut-turut semasa kajian (`;` mentah
		// tolak url.ParseQuery keseluruhan; sebelum itu andaian medan
		// wajib pun silap). Kali ni kegagalan bawa BUKTI PENUH terus
		// dalam log — tak payah round-trip staging-fix-staging lagi
		// untuk cari punca. Payload boleh bawa PII pembayar (nama/emel)
		// — dipotong 1000 aksara dan cuma masuk LOG SERVER, tak pernah
		// sampai ke client.
		snippet := string(payload)
		if len(snippet) > 1000 {
			snippet = snippet[:1000]
		}
		return WebhookEvent{}, fmt.Errorf(
			"toyyibpay callback: billcode tidak dijumpai (content-type=%q, payload mentah=%q)",
			headers.Get("Content-Type"), snippet,
		)
	}

	// Interface VerifyWebhook tiada parameter context (lihat payment.go) —
	// httpClient.Timeout (15s) yang mengawal had masa panggilan ni.
	return t.confirmStatus(context.Background(), billCode)
}

// extractBillCode cuba BERURUTAN pelbagai bentuk body callback yang
// munasabah — bukan andaikan SATU bentuk sahaja. Sebab: ToyyibPay tak
// dokumenkan bentuk callback tepat secara rasmi (apireference/ 403,
// lihat PAYMENT-TOYYIB.md), dan setiap andaian tunggal sebelum ni
// (medan wajib, bentuk "No data found!", `;` sebagai pemisah sah) dah
// silap sekali bila diuji live. Susunan cubaan: form (url-encoded ATAU
// multipart, `;` literal diselamatkan dulu), lepas tu JSON — dan
// PADANAN KUNCI CASE-INSENSITIVE pada dua-dua (respons createBill ToyyibPay
// sendiri guna "BillCode" huruf besar, callback tak disahkan sama).
func extractBillCode(payload []byte, contentType string) string {
	// `;` mentah (disahkan wujud live di salah satu medan lain, cth
	// msg/reason) buat url.ParseQuery tolak KESELURUHAN body sebelum
	// sempat baca billcode — escape dulu, tak ubah struktur pasangan
	// key=value yang dipisah `&`.
	sanitized := strings.ReplaceAll(string(payload), ";", "%3B")
	// Nilai ABAIKAN ralat (bukan cuma `err == nil`) — Opus verify
	// 2026-08-15 dedah `url.ParseQuery` JUGA tolak escape peratus tak
	// sah (cth `reason=100% off`, `%` mentah bukan diikuti hex, sangat
	// munasabah dalam medan teks bebas ToyyibPay). `url.ParseQuery`
	// PULANG `values` SEBAHAGIAN sekali gus `err` bila ini berlaku —
	// billcode selalunya dah berjaya dibaca walaupun medan LAIN
	// (yang kita tak guna pun) gagal decode. Buang gate `err == nil`
	// tu sama kelas bug dengan `;` yang bakar staging sebelum ni: satu
	// aksara dalam SATU medan yang kita tak baca pun tak patut jatuhkan
	// keseluruhan parse.
	values, _ := url.ParseQuery(sanitized)
	if code := billCodeFromValues(values); code != "" {
		return code
	}

	// multipart/form-data — sesetengah backend PHP hantar callback
	// begini, bukan x-www-form-urlencoded. url.ParseQuery di atas tak
	// akan error (ia baca teks bebas macam pasangan tak bermakna) tapi
	// juga takkan jumpa billcode — cuba eksplisit kalau Content-Type
	// memang kata multipart.
	if mediaType, params, err := mime.ParseMediaType(contentType); err == nil && strings.HasPrefix(mediaType, "multipart/") {
		if boundary, ok := params["boundary"]; ok {
			mr := multipart.NewReader(bytes.NewReader(payload), boundary)
			if form, err := mr.ReadForm(1 << 20); err == nil {
				if code := billCodeFromValues(url.Values(form.Value)); code != "" {
					return code
				}
			}
		}
	}

	// JSON — kalau bukan dua-dua bentuk form di atas.
	var jsonBody map[string]any
	if err := json.Unmarshal(payload, &jsonBody); err == nil {
		for key, val := range jsonBody {
			if !strings.EqualFold(key, "billcode") {
				continue
			}
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}

	return ""
}

// billCodeFromValues padan kunci "billcode" case-insensitive.
func billCodeFromValues(values url.Values) string {
	for key, vals := range values {
		if strings.EqualFold(key, "billcode") && len(vals) > 0 && vals[0] != "" {
			return vals[0]
		}
	}
	return ""
}

// CheckStatus — asas internal/paymentreconcile. Guna semula confirmStatus
// (poll getBillTransactions yang sama dipakai VerifyWebhook), TIADA
// logik baharu — reconcile dan webhook confirm MESTI baca dari laluan
// sama supaya jawapan dua-dua konsisten.
func (t *ToyyibPayGateway) CheckStatus(ctx context.Context, billCode string) (string, error) {
	event, err := t.confirmStatus(ctx, billCode)
	if errors.Is(err, ErrIgnoredEvent) {
		// Tiada transaksi lagi = pembayar belum selesai/belum cuba bayar
		// — pending, bukan ralat reconcile.
		return "pending", nil
	}
	if err != nil {
		return "", err
	}
	return event.Status, nil
}

// confirmStatus poll getBillTransactions — sumber kebenaran SEBENAR,
// bukan body callback. Dipanggil oleh VerifyWebhook; nama berasingan
// supaya niatnya jelas dibaca semula (bukan sekadar "verify", tapi
// "sahkan dengan tanya semula server ToyyibPay").
func (t *ToyyibPayGateway) confirmStatus(ctx context.Context, billCode string) (WebhookEvent, error) {
	form := url.Values{}
	form.Set("userSecretKey", t.secretKey)
	form.Set("billCode", billCode)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/index.php/api/getBillTransactions", strings.NewReader(form.Encode()))
	if err != nil {
		return WebhookEvent{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("toyyibpay getBillTransactions: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("toyyibpay getBillTransactions: baca respons: %w", err)
	}

	// Tiada transaksi lagi untuk bil ni: ToyyibPay pulang teks BIASA
	// "No data found!" (BUKAN array JSON kosong `[]`) — disahkan terhadap
	// sandbox 2026-08-15. Perlu semakan eksplisit sebelum json.Unmarshal,
	// atau ia gagal parse dan disalah anggap sebagai ralat sebenar.
	if strings.TrimSpace(string(body)) == `No data found!` {
		return WebhookEvent{}, ErrIgnoredEvent
	}

	// Bentuk respons bila ADA transaksi (disahkan produksi 2026-08-22,
	// bil fmo34a9m): array JSON, medan `billpaymentStatus` (1=berjaya,
	// 2=pending, 3=gagal, 4=pending-alt). Satu bil boleh ada BEBERAPA
	// baris — jangan andaikan indeks terakhir = keputusan akhir.
	var txns []struct {
		BillpaymentStatus    string `json:"billpaymentStatus"`
		BillpaymentInvoiceNo string `json:"billpaymentInvoiceNo"`
	}
	if err := json.Unmarshal(body, &txns); err != nil {
		snippet := string(body)
		// 500, bukan 300 — padan `maxPaymentLogMessage`
		// (paymentlog_helpers.go) supaya snippet ni tak dipotong dua kali
		// buat sia-sia (Opus verify 2026-08-15: 300 di sini + 500 di
		// handler bermakna 300 yang sebenarnya berkuat kuasa, buang
		// maklumat berguna kalau ToyyibPay pulang muka HTML ralat/WAF).
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		return WebhookEvent{}, fmt.Errorf("toyyibpay getBillTransactions: respons tak dijangka: %s", snippet)
	}
	if len(txns) == 0 {
		// Tiada transaksi lagi untuk bil ni (pending, atau callback tiba
		// sebelum ToyyibPay sendiri catat transaksi) — bukan ralat,
		// caller patut abaikan macam event tak relevan.
		return WebhookEvent{}, ErrIgnoredEvent
	}

	// Imbas SEMUA transaksi, bukan elemen terakhir. Disahkan LIVE
	// 2026-08-22 (bil fmo34a9m): array ada status "1" (FPX berjaya) di
	// HADAPAN, diikuti beberapa "4" (pending-alt). Ambil terakhir
	// semata-mata buat webhook/reconcile abaikan duit yang dah masuk.
	// Mana-mana "1" menang; "3" cuma kalau tiada yang berjaya.
	sawFailed := false
	for _, txn := range txns {
		switch txn.BillpaymentStatus {
		case "1":
			return WebhookEvent{
				GatewayRef: billCode,
				Status:     "succeeded",
				PaidAt:     time.Now(),
			}, nil
		case "3":
			sawFailed = true
		}
	}
	if sawFailed {
		return WebhookEvent{
			GatewayRef: billCode,
			Status:     "failed",
			PaidAt:     time.Now(),
		}, nil
	}
	return WebhookEvent{}, ErrIgnoredEvent
}
