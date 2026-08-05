// Package onesignal hantar push notification melalui OneSignal REST API.
// Ini gantian server-side untuk apa yang device_tokens/OneSignal SDK
// Flutter dah setup di client — app cuma daftar subscription id, sekarang
// Go backend yang hantar notification kepada id tu.
package onesignal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultAPIURL = "https://onesignal.com/api/v1/notifications"

type Client struct {
	appID   string
	apiKey  string
	baseURL string
	http    *http.Client
}

func NewClient(appID, apiKey string) *Client {
	return &Client{
		appID:   appID,
		apiKey:  apiKey,
		baseURL: defaultAPIURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SetBaseURLForTest override endpoint OneSignal — untuk test paket lain
// (cth internal/push) yang perlu httptest.Server palsu tanpa akses field
// unexported.
func (c *Client) SetBaseURLForTest(url string) {
	c.baseURL = url
}

// Enabled sama ada credential OneSignal telah diisi. Caller patut skip
// senyap kalau tidak (padanan dengan initOneSignal() di Flutter yang
// lompat kalau ONESIGNAL_APP_ID kosong).
func (c *Client) Enabled() bool {
	return c.appID != "" && c.apiKey != ""
}

type notificationPayload struct {
	AppID            string            `json:"app_id"`
	IncludePlayerIDs []string          `json:"include_player_ids"`
	Headings         map[string]string `json:"headings"`
	Contents         map[string]string `json:"contents"`
}

// Send hantar push notification kepada senarai OneSignal player/
// subscription id (nilai `device_tokens.onesignal_id`).
func (c *Client) Send(ctx context.Context, playerIDs []string, title, message string) error {
	if !c.Enabled() || len(playerIDs) == 0 {
		return nil
	}

	payload := notificationPayload{
		AppID:            c.appID,
		IncludePlayerIDs: playerIDs,
		Headings:         map[string]string{"en": title},
		Contents:         map[string]string{"en": message},
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
	req.Header.Set("Authorization", "Basic "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("onesignal: unexpected status %d", resp.StatusCode)
	}

	return nil
}
