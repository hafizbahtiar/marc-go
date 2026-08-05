package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	var gotAuth string
	var gotPayload sendPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient("re_apikey123", "MARC <noreply@marc.example>")
	c.SetBaseURLForTest(server.URL)

	if !c.Enabled() {
		t.Fatal("expected client to be enabled with apiKey + from set")
	}

	err := c.Send(context.Background(), "user@example.com", "Sahkan Email", "<p>Kod anda</p>")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if gotAuth != "Bearer re_apikey123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer re_apikey123")
	}
	if gotPayload.From != "MARC <noreply@marc.example>" {
		t.Errorf("from = %q", gotPayload.From)
	}
	if len(gotPayload.To) != 1 || gotPayload.To[0] != "user@example.com" {
		t.Errorf("to = %v", gotPayload.To)
	}
	if gotPayload.Subject != "Sahkan Email" || gotPayload.HTML != "<p>Kod anda</p>" {
		t.Errorf("subject/html mismatch: %+v", gotPayload)
	}
}

func TestSendDisabledNoop(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	c := NewClient("", "") // takde credential — patut no-op senyap
	c.SetBaseURLForTest(server.URL)

	if c.Enabled() {
		t.Fatal("expected client to be disabled with empty credentials")
	}
	if err := c.Send(context.Background(), "user@example.com", "Subjek", "<p>x</p>"); err != nil {
		t.Fatalf("expected no error on no-op send, got %v", err)
	}
	if called {
		t.Fatal("expected no HTTP request when client disabled")
	}
}

func TestSendNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewClient("bad-key", "noreply@marc.example")
	c.SetBaseURLForTest(server.URL)

	if err := c.Send(context.Background(), "user@example.com", "Subjek", "<p>x</p>"); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}
