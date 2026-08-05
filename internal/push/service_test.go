package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"marc/internal/db/sqlc"
	"marc/internal/onesignal"
)

// fakeQuerier embed sqlc.Querier (nil) supaya cuma perlu override method
// yang dipakai NotifyUser — method lain akan panic kalau dipanggil, yang
// mana bermaksud test tersasar skop kalau itu berlaku.
type fakeQuerier struct {
	sqlc.Querier
	tokens []sqlc.DeviceToken
	err    error
}

func (f *fakeQuerier) ListDeviceTokensByUser(ctx context.Context, userID uuid.UUID) ([]sqlc.DeviceToken, error) {
	return f.tokens, f.err
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*onesignal.Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	c := onesignal.NewClient("app-id", "api-key")
	c.SetBaseURLForTest(server.URL)
	return c, server.Close
}

func TestNotifyUserSendsToAllTokens(t *testing.T) {
	var gotPlayerIDs []string
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			IncludePlayerIDs []string `json:"include_player_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotPlayerIDs = payload.IncludePlayerIDs
		w.WriteHeader(http.StatusOK)
	})
	defer closeFn()

	q := &fakeQuerier{tokens: []sqlc.DeviceToken{
		{OnesignalID: "player-1"},
		{OnesignalID: "player-2"},
	}}

	svc := NewService(q, client)
	if err := svc.NotifyUser(context.Background(), uuid.New(), "Tajuk", "Mesej"); err != nil {
		t.Fatalf("NotifyUser error: %v", err)
	}

	if len(gotPlayerIDs) != 2 {
		t.Fatalf("expected 2 player ids sent, got %d (%v)", len(gotPlayerIDs), gotPlayerIDs)
	}
}

func TestNotifyUserNoTokensNoop(t *testing.T) {
	called := false
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	defer closeFn()

	q := &fakeQuerier{tokens: nil}
	svc := NewService(q, client)

	if err := svc.NotifyUser(context.Background(), uuid.New(), "Tajuk", "Mesej"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if called {
		t.Fatal("expected no HTTP call when user has no device tokens")
	}
}

func TestNotifyUserDisabledClientSkipsQuery(t *testing.T) {
	client := onesignal.NewClient("", "") // disabled

	q := &fakeQuerier{err: errors.New("should not be called")}
	svc := NewService(q, client)

	if err := svc.NotifyUser(context.Background(), uuid.New(), "Tajuk", "Mesej"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
