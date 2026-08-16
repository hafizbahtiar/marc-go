// Package email hantar email melalui Resend REST API. Dipakai untuk
// email verification (gantian "custom SMTP" yang disebut komen lama di
// Flutter, sebelum ni cuma log.Printf token ke server).
package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultAPIURL = "https://api.resend.com/emails"

type Client struct {
	apiKey  string
	from    string
	baseURL string
	http    *http.Client
}

func NewClient(apiKey, from string) *Client {
	return &Client{
		apiKey:  apiKey,
		from:    from,
		baseURL: defaultAPIURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SetBaseURLForTest override endpoint Resend — untuk test tanpa panggil
// API sebenar.
func (c *Client) SetBaseURLForTest(url string) {
	c.baseURL = url
}

// Enabled sama ada credential Resend telah diisi. Caller patut skip
// senyap kalau tidak (padanan pattern `internal/onesignal`).
func (c *Client) Enabled() bool {
	return c.apiKey != "" && c.from != ""
}

// Attachment — fail dilampirkan pada emel (cth PDF resit donation).
// Resend terima kandungan sebagai base64 dlm body JSON (bukan
// multipart) — `resendAttachment.Content` bawah handle encoding tu.
type Attachment struct {
	Filename string
	Content  []byte
}

type resendAttachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"` // base64
}

type sendPayload struct {
	From        string             `json:"from"`
	To          []string           `json:"to"`
	Subject     string             `json:"subject"`
	HTML        string             `json:"html"`
	Attachments []resendAttachment `json:"attachments,omitempty"`
}

// Send hantar satu email HTML kepada satu penerima.
func (c *Client) Send(ctx context.Context, to, subject, html string) error {
	return c.SendWithAttachments(ctx, to, subject, html, nil)
}

// SendWithAttachments — sama macam Send, tapi boleh lampir fail (cth
// PDF resit). Attachments kosong/nil = sama perilaku dgn Send biasa.
func (c *Client) SendWithAttachments(ctx context.Context, to, subject, html string, attachments []Attachment) error {
	if !c.Enabled() {
		return nil
	}

	payload := sendPayload{From: c.from, To: []string{to}, Subject: subject, HTML: html}
	for _, a := range attachments {
		payload.Attachments = append(payload.Attachments, resendAttachment{
			Filename: a.Filename,
			Content:  base64.StdEncoding.EncodeToString(a.Content),
		})
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend: unexpected status %d", resp.StatusCode)
	}

	return nil
}
