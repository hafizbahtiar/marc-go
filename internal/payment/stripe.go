package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// StripeGateway implements Gateway — akaun Stripe BERASINGAN drpd
// ToyyibPay (yuran ahli), khas untuk sokongan penyelenggaraan app.
type StripeGateway struct {
	client        *stripe.Client
	webhookSecret string
	configured    bool
}

func NewStripeGateway(secretKey, webhookSecret string) *StripeGateway {
	if secretKey == "" {
		return &StripeGateway{configured: false}
	}
	return &StripeGateway{
		client:        stripe.NewClient(secretKey),
		webhookSecret: webhookSecret,
		configured:    true,
	}
}

func (s *StripeGateway) Name() string { return "stripe" }

func (s *StripeGateway) Enabled() bool { return s.configured }

// CreatePayment cipta PaymentIntent dengan automatic_payment_methods
// enabled (Payment Element di Flutter tentukan cara bayar ikut apa yang
// aktif di dashboard Stripe). ClientSecret diisi, RedirectURL kosong —
// Stripe ialah gateway client-side confirm.
func (s *StripeGateway) CreatePayment(ctx context.Context, params CreateParams) (CreateResult, error) {
	if !s.configured {
		return CreateResult{}, ErrNotConfigured
	}

	piParams := &stripe.PaymentIntentCreateParams{
		Amount:   stripe.Int64(params.AmountCents),
		Currency: stripe.String(params.Currency),
		AutomaticPaymentMethods: &stripe.PaymentIntentCreateAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		Metadata: params.Metadata,
	}

	pi, err := s.client.V1PaymentIntents.Create(ctx, piParams)
	if err != nil {
		return CreateResult{}, err
	}

	// RawResponse: stripe-go punya client wrapper nyahsiri terus ke `pi`,
	// tak dedahkan byte HTTP mentah — siri SEMULA `pi` penuh ke JSON
	// sebagai anggaran. Bukan wayar sebenar (medan yang stripe-go tak
	// petakan takkan ada), tapi bawa SETIAP field yang Stripe pulangkan
	// yang kita ada akses padanya — cukup untuk diagnosis. Kegagalan
	// Marshal (patut mustahil untuk struct terjana) diabaikan senyap:
	// checkout MESTI tetap berjaya walau raw-capture gagal.
	//
	// ClientSecret DIKOSONGKAN pada salinan sebelum Marshal (Opus verify
	// 2026-08-15) — payment_logs.raw_payload simpan 90 hari, boleh dibaca
	// sesiapa dengan akses DB; client_secret bukan sekadar PII, ia
	// KELAYAKAN (benarkan retrieve/confirm/cancel PaymentIntent tu dari
	// client-side). CreateResult.ClientSecret di bawah (dihantar balik ke
	// Flutter) TAK terjejas — cuma salinan untuk log yang dikosongkan.
	logCopy := *pi
	logCopy.ClientSecret = ""
	rawJSON, _ := json.Marshal(logCopy)

	return CreateResult{
		GatewayRef:   pi.ID,
		ClientSecret: pi.ClientSecret,
		RawResponse:  string(rawJSON),
	}, nil
}

// CheckStatus — asas internal/paymentreconcile. Ambil PaymentIntent
// terus dari Stripe (bukan webhook/DB tempatan) dan normalisasi ke set
// nilai sama dengan WebhookEvent.Status + "pending". `canceled` dilayan
// "failed" (intent ditinggalkan, takkan selesai); `succeeded` "succeeded";
// selain tu (requires_payment_method/confirmation/action/processing)
// "pending" — pembayar belum selesai atau masih mid-flow.
func (s *StripeGateway) CheckStatus(ctx context.Context, paymentIntentID string) (string, error) {
	if !s.configured {
		return "", ErrNotConfigured
	}

	pi, err := s.client.V1PaymentIntents.Retrieve(ctx, paymentIntentID, nil)
	if err != nil {
		return "", fmt.Errorf("stripe retrieve payment intent: %w", err)
	}

	switch pi.Status {
	case stripe.PaymentIntentStatusSucceeded:
		return "succeeded", nil
	case stripe.PaymentIntentStatusCanceled:
		return "failed", nil
	default:
		return "pending", nil
	}
}

// VerifyWebhook sahkan header `Stripe-Signature` lawan raw body, pulang
// event succeeded/failed dinormalisasi. Event Stripe lain (banyak jenis
// — charge.*, customer.*, dll, sebab satu webhook endpoint boleh
// terima semua event akaun) pulang ErrIgnoredEvent.
func (s *StripeGateway) VerifyWebhook(payload []byte, headers http.Header) (WebhookEvent, error) {
	if !s.configured {
		return WebhookEvent{}, ErrNotConfigured
	}

	// WAJIB: tanpa guard ni, `STRIPE_WEBHOOK_SECRET` kosong bermakna
	// signature dikira HMAC dengan KUNCI KOSONG — sesiapa sahaja boleh
	// hasilkan header `Stripe-Signature` yang sah dan tandakan mana-mana
	// donation sebagai "succeeded". `webhook.ConstructEvent` stripe-go
	// TIDAK tolak secret kosong dengan sendirinya.
	if s.webhookSecret == "" {
		return WebhookEvent{}, ErrNotConfigured
	}

	// IgnoreAPIVersionMismatch: WAJIB. `webhook.ConstructEvent` bukan
	// setakat sahkan signature — ia JUGA tolak event yang API version-nya
	// lain "release train" drpd yang stripe-go dikompil dengan
	// (cth akaun hantar `2025-10-29.clover`, stripe-go v82 jangka
	// `2025-08-27.basil`), dan pulangkan ralat yang nampak sama macam
	// signature gagal. Webhook endpoint Stripe TAK boleh tukar
	// `api_version` selepas dicipta, jadi guard tu akan kekal gagal
	// selamanya walaupun signature 100% sah.
	//
	// Selamat diabaikan di sini sebab kita cuma baca `event.Type` dan
	// `pi.ID` — dua-dua stabil merentas semua versi API Stripe. Signature
	// masih disahkan sepenuhnya; hanya semakan versi yang dilangkau.
	event, err := webhook.ConstructEventWithOptions(
		payload,
		headers.Get("Stripe-Signature"),
		s.webhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
	)
	if err != nil {
		return WebhookEvent{}, err
	}

	var status string
	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		status = "succeeded"
	case stripe.EventTypePaymentIntentPaymentFailed:
		status = "failed"
	default:
		return WebhookEvent{}, ErrIgnoredEvent
	}

	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		return WebhookEvent{}, err
	}

	// L27a: Ideally PaidAt should come from the Charge's `Created` (actual
	// payment completion time), not PaymentIntent.Created (intent-creation
	// time) — these diverge for delayed payment methods like DuitNow QR/FPX.
	// `pi.LatestCharge` (`*stripe.Charge`, has its own `Created int64`) looks
	// like the fix, BUT Stripe does NOT expand `latest_charge` in webhook
	// payloads by default — it arrives as a bare charge ID string. stripe-go's
	// `Charge.UnmarshalJSON` (charge.go) handles that case by only setting
	// `c.ID` and leaving `Created` at its zero value, so reading
	// `pi.LatestCharge.Created` here would silently produce `1970-01-01`
	// instead of a wrong-but-plausible timestamp — worse than the current bug.
	// Getting the real charge time would require either configuring webhook
	// endpoint expansions for `latest_charge` (Stripe dashboard/API setting,
	// not present in this codebase) or an extra `charge.Get` API call inside
	// webhook processing — both out of scope here. Keeping `pi.Created` until
	// one of those is deliberately added.
	return WebhookEvent{GatewayRef: pi.ID, Status: status, PaidAt: time.Unix(pi.Created, 0)}, nil
}
