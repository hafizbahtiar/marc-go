package payment

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// StripeGateway implements Gateway — akaun Stripe BERASINGAN drpd
// ToyyibPay (yuran ahli MAIWP), khas untuk "Donate to us".
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

	return CreateResult{GatewayRef: pi.ID, ClientSecret: pi.ClientSecret}, nil
}

// VerifyWebhook sahkan header `Stripe-Signature` lawan raw body, pulang
// event succeeded/failed dinormalisasi. Event Stripe lain (banyak jenis
// — charge.*, customer.*, dll, sebab satu webhook endpoint boleh
// terima semua event akaun) pulang ErrIgnoredEvent.
func (s *StripeGateway) VerifyWebhook(payload []byte, headers http.Header) (WebhookEvent, error) {
	if !s.configured {
		return WebhookEvent{}, ErrNotConfigured
	}

	event, err := webhook.ConstructEvent(payload, headers.Get("Stripe-Signature"), s.webhookSecret)
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

	return WebhookEvent{GatewayRef: pi.ID, Status: status}, nil
}
