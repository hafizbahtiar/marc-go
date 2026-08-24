// Package registrationsweep menandakan yuran pendaftaran ahli 'pending'
// yang lapuk sebagai 'failed' — padanan internal/activitysweep untuk
// modul yuran pendaftaran (registration_payments).
//
// Bila ahli mula checkout ToyyibPay tapi tak selesaikan bayaran, baris
// kekal 'pending' selama-lama (CheckStatus pulang "pending" selama-lamanya
// untuk bil unpaid). Gate bypass admin (`HasPendingRegistrationPayment`)
// tersekat sehingga baris ni diselesaikan. Dua lapisan:
//   - billExpiryDate (30 min default) pada createBill — bil inactive di
//     ToyyibPay
//   - sapuan DB ini — tandakan 'failed' selepas cutoff + semak gateway
//     supaya webhook lewat/bayaran lewat tak hilang senyap
package registrationsweep

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
	"marc/internal/payment"
	"marc/internal/paymentlog"
)

const batchSize = 100

type Sweeper struct {
	queries   *sqlc.Queries
	gateways  map[string]payment.Gateway
	interval  time.Duration
	staleAfter time.Duration
}

func New(queries *sqlc.Queries, gateways map[string]payment.Gateway, interval, staleAfter time.Duration) *Sweeper {
	return &Sweeper{
		queries:    queries,
		gateways:   gateways,
		interval:   interval,
		staleAfter: staleAfter,
	}
}

func (s *Sweeper) Start(ctx context.Context) {
	go func() {
		s.RunOnce(ctx)

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx)
			}
		}
	}()
}

// RunOnce sapu baris 'pending' lebih tua drpd staleAfter. Diekspos untuk
// ujian dan panggilan terus tanpa menunggu ticker.
func (s *Sweeper) RunOnce(ctx context.Context) {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-s.staleAfter), Valid: true}
	rows, err := s.queries.ListStalePendingRegistrationPayments(ctx, sqlc.ListStalePendingRegistrationPaymentsParams{
		CreatedAt: cutoff,
		Limit:     batchSize,
	})
	if err != nil {
		log.Printf("registrationsweep: senarai pending lapuk gagal: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	var expired int
	for _, row := range rows {
		if s.expireRow(ctx, row) {
			expired++
		}
	}
	if expired > 0 {
		log.Printf("registrationsweep: %d yuran pendaftaran pending ditandakan failed (lapuk, >%s)", expired, s.staleAfter)
	}
}

func (s *Sweeper) expireRow(ctx context.Context, row sqlc.RegistrationPayment) bool {
	amount := int64(row.AmountCents)
	userID := row.UserID
	relatedID := row.ID

	if row.GatewayRef.Valid && row.GatewayRef.String != "" {
		gw, ok := s.gateways[row.Gateway]
		if !ok || !gw.Enabled() {
			log.Printf("registrationsweep: gateway %q (ref=%s) tak tersedia, langkau", row.Gateway, row.GatewayRef.String)
			return false
		}

		status, err := gw.CheckStatus(ctx, row.GatewayRef.String)
		if err != nil {
			log.Printf("registrationsweep: CheckStatus gagal (ref=%s): %v", row.GatewayRef.String, err)
			return false
		}

		switch status {
		case "succeeded":
			if _, err := s.queries.UpdateRegistrationPaymentStatusByGatewayRef(ctx, sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
				Gateway: row.Gateway, GatewayRef: row.GatewayRef, Status: "succeeded",
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("registrationsweep: kemas kini succeeded gagal (ref=%s): %v", row.GatewayRef.String, err)
				return false
			}
			paymentlog.Record(ctx, s.queries, paymentlog.Entry{
				Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventReconcileMismatch,
				Status: "succeeded", Gateway: row.Gateway, GatewayRef: row.GatewayRef.String,
				AmountCents: &amount, UserID: &userID, RelatedID: &relatedID,
				Message: "sapuan: gateway succeeded, DB dikemas kini",
			})
			return false
		case "failed":
			if _, err := s.queries.UpdateRegistrationPaymentStatusByGatewayRef(ctx, sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
				Gateway: row.Gateway, GatewayRef: row.GatewayRef, Status: "failed",
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("registrationsweep: kemas kini failed gagal (ref=%s): %v", row.GatewayRef.String, err)
				return false
			}
			return false
		}
		// Masih "pending" di gateway — tandakan failed dalam DB (bil
		// patut dah tamat tempoh melalui billExpiryDate).
	} else {
		// Tiada ref — tiada bil sebenar; selamat tandakan failed terus.
		if err := s.queries.MarkRegistrationPaymentFailed(ctx, row.ID); err != nil {
			log.Printf("registrationsweep: MarkRegistrationPaymentFailed gagal (id=%s): %v", row.ID, err)
			return false
		}
		return true
	}

	if _, err := s.queries.ExpireRegistrationPayment(ctx, row.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		log.Printf("registrationsweep: ExpireRegistrationPayment gagal (id=%s): %v", row.ID, err)
		return false
	}

	paymentlog.Record(ctx, s.queries, paymentlog.Entry{
		Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventReconcileCheck,
		Status: "failed", Gateway: row.Gateway,
		GatewayRef: row.GatewayRef.String, AmountCents: &amount,
		UserID: &userID, RelatedID: &relatedID,
		Message: "sapuan: bil pending lapuk ditandakan failed",
	})
	return true
}
