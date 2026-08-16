// Package payment mendefinisikan kontrak sepunya (`Gateway`) untuk
// semua payment provider (Stage 12: Stripe donation sekarang; ToyyibPay
// yuran ahli + SociaBuzz donation akan datang). Handler HTTP bergantung
// interface ni SAHAJA — tambah gateway baru = implement `Gateway` +
// daftar dalam registry (lihat `cmd/api/main.go`), tiada perubahan pada
// handler/routing sedia ada. Loose coupling: swap/tambah provider tanpa
// sentuh kod yang dah stabil.
package payment

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrNotConfigured — gateway belum ada credential (env kosong). Handler
// pulangkan 503 graceful, bukan crash (padanan pattern R2Client).
var ErrNotConfigured = errors.New("payment: gateway belum dikonfigurasi")

// ErrIgnoredEvent — webhook event jenis yang gateway ni tak peduli
// (contoh Stripe hantar banyak event type, kita cuma nak
// succeeded/failed). BUKAN ralat sebenar — caller (handler) patut
// respond 200 OK, elak gateway retry berterusan.
var ErrIgnoredEvent = errors.New("payment: webhook event diabaikan")

// CreateParams parameter generik untuk mulakan bayaran — sama struktur
// tak kira gateway.
type CreateParams struct {
	AmountCents int64
	Currency    string // ISO 4217 lowercase, cth "myr"
	Metadata    map[string]string
}

// CreateResult keputusan mulakan bayaran. Union type ala pattern
// payment aggregator sebenar (Stripe sendiri guna corak sama untuk
// `next_action`) — HANYA SATU antara ClientSecret/RedirectURL diisi,
// ikut model gateway:
//   - ClientSecret: gateway client-side confirm (Stripe Payment Element)
//   - RedirectURL: gateway hosted-redirect page (ToyyibPay, SociaBuzz)
//
// Caller (handler HTTP, lepas tu client Flutter) tengok field mana
// terisi untuk tentukan flow — tak perlu tahu gateway spesifik apa.
type CreateResult struct {
	GatewayRef   string
	ClientSecret string
	RedirectURL  string

	// RawResponse — respons MENTAH gateway bagi panggilan create-payment
	// ni (badan HTTP createBill ToyyibPay, atau PaymentIntent Stripe
	// disiri semula ke JSON). PILIHAN — kosong kalau gateway tak isi.
	// Simetri dengan raw_payload webhook (internal/paymentlog): sebab
	// sama — diagnosis satu insiden checkout tak patut perlukan SSH ke
	// produksi. Medan RATA di sini (bukan interface method baharu)
	// sengaja kekalkan Gateway loose-coupled — caller (handler HTTP)
	// baca medan ni secara buta tanpa tahu bentuk dalaman gateway
	// mana pun, dan gateway yang tak nak isi tinggal biar kosong tanpa
	// langgar kontrak.
	RawResponse string
}

// WebhookEvent hasil generik lepas gateway verify + parse callback
// sendiri (signature/format berbeza setiap gateway — itu kekal dalam
// implementation masing-masing, BUKAN sebahagian interface ni).
type WebhookEvent struct {
	GatewayRef string
	Status     string // dinormalisasi ke "succeeded" | "failed"
	// PaidAt — masa SEBENAR gateway proses bayaran (bukan masa webhook
	// diproses server kita — webhook boleh lewat/retry, jadi guna ni
	// untuk apa-apa yang tercatat kekal, cth tarikh pada resit PDF).
	PaidAt time.Time
}

// Gateway kontrak sepunya semua payment provider.
type Gateway interface {
	// Name — pengecam pendek gateway (cth "stripe"), padanan column
	// `donations.gateway` di DB.
	Name() string

	// Enabled — false kalau credential belum diisi (env kosong).
	Enabled() bool

	// CreatePayment mulakan satu bayaran baru.
	CreatePayment(ctx context.Context, params CreateParams) (CreateResult, error)

	// VerifyWebhook sahkan + parse callback gateway. `payload` MESTI
	// byte MENTAH request body (sebelum sebarang JSON binding/parsing
	// lain — sesetengah gateway kira signature atas byte tepat).
	// Pulang ErrIgnoredEvent untuk event type yang tak relevan (bukan
	// ralat — caller patut respond 200 OK).
	VerifyWebhook(payload []byte, headers http.Header) (WebhookEvent, error)

	// CheckStatus tanya status SEBENAR satu bayaran TERUS dari gateway
	// (bukan daripada webhook/DB tempatan) — asas internal/paymentreconcile
	// (2026-08-15, lepas insiden webhook ToyyibPay yang gagal senyap
	// beberapa kali: DB boleh tersasar drpd kebenaran gateway kalau
	// webhook tak pernah tiba/gagal parse, dan reconcile ialah cara
	// pulih tanpa perlu perasan manual). Pulang "pending"/"succeeded"/
	// "failed" — sama set nilai dengan WebhookEvent.Status + "pending".
	CheckStatus(ctx context.Context, gatewayRef string) (status string, err error)
}
