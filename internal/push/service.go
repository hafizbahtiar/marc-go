// Package push gabungkan device_tokens (Stage 4) dengan OneSignal client
// (internal/onesignal) supaya senang panggil "hantar notification kepada
// user ni" dari mana-mana bahagian app.
//
// Trigger diwire di Stage 10 (Posts) — NotifyUser dipanggil dari
// notifyOwner (internal/http/handlers/posts_common.go) bila like/comment
// pada post/comment sendiri.
package push

import (
	"context"

	"github.com/google/uuid"

	"marc/internal/db/sqlc"
	"marc/internal/onesignal"
)

type Service struct {
	queries sqlc.Querier
	client  *onesignal.Client
}

func NewService(queries sqlc.Querier, client *onesignal.Client) *Service {
	return &Service{queries: queries, client: client}
}

// NotifyUser hantar push notification ke semua peranti berdaftar untuk
// user tertentu. No-op senyap kalau OneSignal tak configured atau user
// tiada device token berdaftar.
func (s *Service) NotifyUser(ctx context.Context, userID uuid.UUID, title, message string) error {
	if !s.client.Enabled() {
		return nil
	}

	tokens, err := s.queries.ListDeviceTokensByUser(ctx, userID)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}

	playerIDs := make([]string, len(tokens))
	for i, t := range tokens {
		playerIDs[i] = t.OnesignalID
	}

	return s.client.Send(ctx, playerIDs, title, message)
}
