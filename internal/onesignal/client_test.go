package onesignal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	var gotAuth string
	var gotPayload notificationPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient("app-id-123", "api-key-456")
	c.baseURL = server.URL

	if !c.Enabled() {
		t.Fatal("expected client to be enabled with app id + api key set")
	}

	err := c.Send(context.Background(), []string{"player-1", "player-2"}, "Tajuk", "Mesej")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if gotAuth != "Basic api-key-456" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Basic api-key-456")
	}
	if gotPayload.AppID != "app-id-123" {
		t.Errorf("app_id = %q, want %q", gotPayload.AppID, "app-id-123")
	}
	if len(gotPayload.IncludePlayerIDs) != 2 {
		t.Errorf("include_player_ids length = %d, want 2", len(gotPayload.IncludePlayerIDs))
	}
	if gotPayload.Headings["en"] != "Tajuk" || gotPayload.Contents["en"] != "Mesej" {
		t.Errorf("headings/contents mismatch: %+v", gotPayload)
	}
}

func TestSendDisabledNoop(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	c := NewClient("", "") // takde credential — patut no-op senyap
	c.baseURL = server.URL

	if c.Enabled() {
		t.Fatal("expected client to be disabled with empty credentials")
	}

	if err := c.Send(context.Background(), []string{"player-1"}, "Tajuk", "Mesej"); err != nil {
		t.Fatalf("expected no error on no-op send, got %v", err)
	}
	if called {
		t.Fatal("expected no HTTP request when client disabled")
	}
}

func TestSendEmptyPlayerIDsNoop(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	c := NewClient("app-id", "api-key")
	c.baseURL = server.URL

	if err := c.Send(context.Background(), []string{}, "Tajuk", "Mesej"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if called {
		t.Fatal("expected no HTTP request when playerIDs empty")
	}
}

func TestSendNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	c := NewClient("app-id", "api-key")
	c.baseURL = server.URL

	if err := c.Send(context.Background(), []string{"player-1"}, "Tajuk", "Mesej"); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}
