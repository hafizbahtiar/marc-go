// Package paymentreconcile menyemak semula bayaran 'pending' TERUS pada
// gateway (bukan bergantung webhook semata-mata) merentas ketiga-tiga
// modul bayaran (donation Stripe, yuran pendaftaran ToyyibPay, yuran
// aktiviti ToyyibPay) dan membetulkan DB automatik bila jawapan gateway
// tak sepadan status tersimpan.
//
// Keputusan produk 2026-08-15, lepas beberapa insiden webhook ToyyibPay
// yang gagal senyap (`;` mentah, `%` tak sah, billcode tak dijumpai) —
// DB boleh tersasar drpd kebenaran gateway kalau webhook tak pernah
// tiba/gagal parse. Gateway ialah SUMBER KEBENARAN (keputusan produk):
// bila mismatch dijumpai, DB dikemas kini automatik untuk padan gateway,
// bukan sekadar dilog untuk semakan manual.
//
// Struktur ikut internal/activitysweep rapat (New/Start/RunOnce,
// ticker-based loop) — corak sama yang dah terbukti untuk kerja latar
// berkala + pencetus manual.
package paymentreconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
	"marc/internal/payment"
	"marc/internal/paymentlog"
)

// minAge — umur minimum baris 'pending' sebelum ia layak disemak semula
// pada gateway. Jauh lebih pendek drpd cutoff internal/activitysweep
// (45 minit/24 jam) SENGAJA: activitysweep membatalkan (tindakan
// MUSNAH, kena berhati-hati elak race dengan webhook lewat), reconcile
// ni pula MEMBETULKAN state ikut jawapan gateway sebenar (tindakan
// selamat, boleh diulang) — semak terlalu awal cuma bermakna panggilan
// API gateway lebih kerap untuk bayaran yang MEMANG masih pending
// (murah), bukan risiko keputusan salah macam pembatalan pra-matang.
const minAge = 15 * time.Minute

// ReconcileSummary — keputusan satu pusingan RunOnce, dipulangkan supaya
// pencetus manual (endpoint HTTP) boleh laporkan sesuatu yang berguna
// kepada caller, bukan sekadar "ok".
type ReconcileSummary struct {
	Checked         int `json:"checked"`
	MismatchesFixed int `json:"mismatches_fixed"`
	Errors          int `json:"errors"`
}

type Reconciler struct {
	queries  *sqlc.Queries
	gateways map[string]payment.Gateway
	interval time.Duration
}

func New(queries *sqlc.Queries, gateways map[string]payment.Gateway, interval time.Duration) *Reconciler {
	return &Reconciler{queries: queries, gateways: gateways, interval: interval}
}

// Start jalankan sapuan dalam background sehingga ctx dibatalkan.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		// Satu pusingan sebaik boot: kalau proses terbunuh dengan bayaran
		// tersasar, ia tak patut tunggu satu interval penuh.
		r.RunOnce(ctx)

		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.RunOnce(ctx)
			}
		}
	}()
}

// RunOnce semak semua bayaran 'pending' lapuk (>minAge) merentas
// ketiga-tiga modul terus pada gateway masing-masing, dan betulkan DB
// automatik bila jawapan gateway tak sepadan. Diekspos supaya boleh
// dipanggil terus dalam ujian, oleh loop latar, DAN oleh endpoint HTTP
// pencetus manual (POST /admin/payments/reconcile).
func (r *Reconciler) RunOnce(ctx context.Context) ReconcileSummary {
	summary := ReconcileSummary{}
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-minAge), Valid: true}

	r.reconcileRegistrationPayments(ctx, cutoff, &summary)
	r.reconcileActivityRegistrations(ctx, cutoff, &summary)
	r.reconcileDonations(ctx, cutoff, &summary)

	return summary
}

// reconcileRegistrationPayments — yuran pendaftaran ahli sekali bayar
// (jadual registration_payments, CHECK status IN ('pending','succeeded',
// 'failed') — set nilai SAMA dengan Gateway.CheckStatus, jadi jawapan
// gateway boleh ditulis terus tanpa pemetaan).
func (r *Reconciler) reconcileRegistrationPayments(ctx context.Context, cutoff pgtype.Timestamptz, summary *ReconcileSummary) {
	rows, err := r.queries.ListPendingRegistrationPaymentsOlderThan(ctx, cutoff)
	if err != nil {
		log.Printf("paymentreconcile: senarai yuran pendaftaran pending gagal: %v", err)
		summary.Errors++
		return
	}

	for _, row := range rows {
		summary.Checked++

		gw, ok := r.gateways[row.Gateway]
		if !ok || !gw.Enabled() {
			log.Printf("paymentreconcile: gateway %q (yuran pendaftaran, ref=%s) tak berdaftar/tak enabled, langkau", row.Gateway, row.GatewayRef)
			summary.Errors++
			continue
		}

		amount := int64(row.AmountCents)
		userID := row.UserID
		relatedID := row.ID

		status, err := gw.CheckStatus(ctx, row.GatewayRef)
		if err != nil {
			log.Printf("paymentreconcile: CheckStatus gagal (yuran pendaftaran, gateway=%s, ref=%s): %v", row.Gateway, row.GatewayRef, err)
			summary.Errors++
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:      paymentlog.ModuleRegistrationFee,
				Event:       paymentlog.EventReconcileCheck,
				Status:      paymentlog.StatusError,
				Gateway:     row.Gateway,
				GatewayRef:  row.GatewayRef,
				AmountCents: &amount,
				UserID:      &userID,
				RelatedID:   &relatedID,
				Message:     err.Error(),
			})
			continue
		}

		if status == row.Status {
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:      paymentlog.ModuleRegistrationFee,
				Event:       paymentlog.EventReconcileCheck,
				Status:      status,
				Gateway:     row.Gateway,
				GatewayRef:  row.GatewayRef,
				AmountCents: &amount,
				UserID:      &userID,
				RelatedID:   &relatedID,
			})
			continue
		}

		msg := fmt.Sprintf("DB=%s, gateway=%s, dikemas kini", row.Status, status)
		paymentlog.Record(ctx, r.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleRegistrationFee,
			Event:       paymentlog.EventReconcileMismatch,
			Status:      paymentlog.StatusMismatch,
			Gateway:     row.Gateway,
			GatewayRef:  row.GatewayRef,
			AmountCents: &amount,
			UserID:      &userID,
			RelatedID:   &relatedID,
			Message:     msg,
		})

		if _, err := r.queries.UpdateRegistrationPaymentStatusByGatewayRef(ctx, sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
			Gateway:    row.Gateway,
			GatewayRef: row.GatewayRef,
			Status:     status,
		}); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("paymentreconcile: betulkan status yuran pendaftaran gagal (ref=%s): %v", row.GatewayRef, err)
				summary.Errors++
			}
			// pgx.ErrNoRows = proses lain (webhook) dah tolak baris ni ke
			// keadaan terminal SEBELUM UPDATE ni sempat — bukan "dibetulkan
			// reconcile", ELAK kira dua kali (Opus verify 2026-08-15).
			continue
		}
		summary.MismatchesFixed++
	}
}

// reconcileActivityRegistrations — yuran aktiviti (jadual
// activity_registrations, CHECK payment_status IN ('not_required',
// 'pending','paid','refunded') — TIADA 'failed', padanan keputusan yang
// sama dihormati webhook handler sedia ada
// (ActivityRegistrationPaymentHandler.Webhook): gateway "failed" TAK
// ditulis ke DB, baris kekal 'pending' untuk internal/activitysweep
// bersihkan kelak, tapi tetap dilog sebagai isyarat diagnostik berguna.
//
// Jadual ni TIADA lajur `gateway` (beza drpd registration_payments/
// donations) — satu gateway ToyyibPay sahaja untuk modul ni, tapi
// instance kredential yang BETUL untuk dipanggil ialah kunci peta
// "toyyibpay-activity" (lihat cmd/api/main.go: instance kredential SAMA
// dengan "toyyibpay" tapi callbackURL/returnURL berbeza — ni PADANAN
// wiring ActivityRegistrationPaymentHandler, bukan tekaan).
func (r *Reconciler) reconcileActivityRegistrations(ctx context.Context, cutoff pgtype.Timestamptz, summary *ReconcileSummary) {
	rows, err := r.queries.ListPendingActivityRegistrationsOlderThan(ctx, cutoff)
	if err != nil {
		log.Printf("paymentreconcile: senarai yuran aktiviti pending gagal: %v", err)
		summary.Errors++
		return
	}

	gw, ok := r.gateways["toyyibpay-activity"]
	if !ok || !gw.Enabled() {
		if len(rows) > 0 {
			log.Printf("paymentreconcile: gateway toyyibpay-activity tak berdaftar/tak enabled, langkau %d baris yuran aktiviti pending", len(rows))
			summary.Errors++
		}
		return
	}

	for _, row := range rows {
		summary.Checked++

		paymentRef := row.PaymentRef.String
		userID := row.UserID
		relatedID := row.ID

		status, err := gw.CheckStatus(ctx, paymentRef)
		if err != nil {
			log.Printf("paymentreconcile: CheckStatus gagal (yuran aktiviti, ref=%s): %v", paymentRef, err)
			summary.Errors++
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:     paymentlog.ModuleActivityFee,
				Event:      paymentlog.EventReconcileCheck,
				Status:     paymentlog.StatusError,
				Gateway:    gw.Name(),
				GatewayRef: paymentRef,
				UserID:     &userID,
				RelatedID:  &relatedID,
				Message:    err.Error(),
			})
			continue
		}

		// row.PaymentStatus sentiasa "pending" di sini (itu kriteria
		// query) — bandingkan terus dengan status gateway "pending".
		if status == "pending" {
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:     paymentlog.ModuleActivityFee,
				Event:      paymentlog.EventReconcileCheck,
				Status:     paymentlog.StatusPending,
				Gateway:    gw.Name(),
				GatewayRef: paymentRef,
				UserID:     &userID,
				RelatedID:  &relatedID,
			})
			continue
		}

		if status == "failed" {
			// TIADA 'failed' dalam CHECK constraint payment_status — tiada
			// tulisan DB di sini (padanan webhook handler), tapi tetap
			// dilog sebagai isyarat diagnostik.
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:     paymentlog.ModuleActivityFee,
				Event:      paymentlog.EventReconcileMismatch,
				Status:     paymentlog.StatusFailed,
				Gateway:    gw.Name(),
				GatewayRef: paymentRef,
				UserID:     &userID,
				RelatedID:  &relatedID,
				Message:    fmt.Sprintf("DB=%s, gateway=failed, TIADA tulisan DB (payment_status tiada nilai 'failed'), tinggal untuk activitysweep bersihkan", row.PaymentStatus),
			})
			continue
		}

		// status == "succeeded" -> "paid".
		msg := fmt.Sprintf("DB=%s, gateway=succeeded, dikemas kini ke paid", row.PaymentStatus)
		paymentlog.Record(ctx, r.queries, paymentlog.Entry{
			Module:     paymentlog.ModuleActivityFee,
			Event:      paymentlog.EventReconcileMismatch,
			Status:     paymentlog.StatusMismatch,
			Gateway:    gw.Name(),
			GatewayRef: paymentRef,
			UserID:     &userID,
			RelatedID:  &relatedID,
			Message:    msg,
		})

		updated, err := r.queries.UpdateRegistrationPaymentStatusByPaymentRef(ctx, sqlc.UpdateRegistrationPaymentStatusByPaymentRefParams{
			PaymentRef:    row.PaymentRef,
			PaymentStatus: "paid",
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("paymentreconcile: betulkan payment_status yuran aktiviti gagal (ref=%s): %v", paymentRef, err)
				summary.Errors++
			}
			// pgx.ErrNoRows = baris dah 'paid' (proses lain menang) — bukan
			// dibetulkan reconcile ni, elak kira dua kali.
			continue
		} else if updated.Status == "cancelled" {
			// Race sweep-vs-reconcile, padanan kes sama dalam webhook
			// handler (activity_registration_payment.go) — baris ni dah
			// dibatal (CancelStaleUnpaidBills) SEBELUM reconcile sempat
			// membetulkannya. ERROR (bukan sekadar log) supaya nampak
			// dalam pemantauan produksi — perlukan semakan manual.
			log.Printf("ERROR paymentreconcile: ahli BAYAR (ref=%s, registration=%s) tapi pendaftaran SUDAH DIBATAL oleh sapuan — perlukan semakan manual (slot mungkin dah diambil orang lain)", paymentRef, updated.ID)
		}
		summary.MismatchesFixed++
	}
}

// reconcileDonations — donation Stripe (jadual donations, CHECK status
// IN ('pending','succeeded','failed') — set nilai SAMA dengan
// Gateway.CheckStatus).
func (r *Reconciler) reconcileDonations(ctx context.Context, cutoff pgtype.Timestamptz, summary *ReconcileSummary) {
	rows, err := r.queries.ListPendingDonationsOlderThan(ctx, cutoff)
	if err != nil {
		log.Printf("paymentreconcile: senarai donation pending gagal: %v", err)
		summary.Errors++
		return
	}

	for _, row := range rows {
		summary.Checked++

		gw, ok := r.gateways[row.Gateway]
		if !ok || !gw.Enabled() {
			log.Printf("paymentreconcile: gateway %q (donation, ref=%s) tak berdaftar/tak enabled, langkau", row.Gateway, row.GatewayRef)
			summary.Errors++
			continue
		}

		amount := int64(row.AmountCents)
		relatedID := row.ID
		var userIDPtr *uuid.UUID
		if row.UserID.Valid {
			id := uuid.UUID(row.UserID.Bytes)
			userIDPtr = &id
		}

		status, err := gw.CheckStatus(ctx, row.GatewayRef)
		if err != nil {
			log.Printf("paymentreconcile: CheckStatus gagal (donation, gateway=%s, ref=%s): %v", row.Gateway, row.GatewayRef, err)
			summary.Errors++
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:      paymentlog.ModuleDonation,
				Event:       paymentlog.EventReconcileCheck,
				Status:      paymentlog.StatusError,
				Gateway:     row.Gateway,
				GatewayRef:  row.GatewayRef,
				AmountCents: &amount,
				UserID:      userIDPtr,
				RelatedID:   &relatedID,
				Message:     err.Error(),
			})
			continue
		}

		if status == row.Status {
			paymentlog.Record(ctx, r.queries, paymentlog.Entry{
				Module:      paymentlog.ModuleDonation,
				Event:       paymentlog.EventReconcileCheck,
				Status:      status,
				Gateway:     row.Gateway,
				GatewayRef:  row.GatewayRef,
				AmountCents: &amount,
				UserID:      userIDPtr,
				RelatedID:   &relatedID,
			})
			continue
		}

		msg := fmt.Sprintf("DB=%s, gateway=%s, dikemas kini", row.Status, status)
		paymentlog.Record(ctx, r.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleDonation,
			Event:       paymentlog.EventReconcileMismatch,
			Status:      paymentlog.StatusMismatch,
			Gateway:     row.Gateway,
			GatewayRef:  row.GatewayRef,
			AmountCents: &amount,
			UserID:      userIDPtr,
			RelatedID:   &relatedID,
			Message:     msg,
		})

		if _, err := r.queries.UpdateDonationStatusByGatewayRef(ctx, sqlc.UpdateDonationStatusByGatewayRefParams{
			Gateway:    row.Gateway,
			GatewayRef: row.GatewayRef,
			Status:     status,
		}); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("paymentreconcile: betulkan status donation gagal (ref=%s): %v", row.GatewayRef, err)
				summary.Errors++
			}
			// pgx.ErrNoRows = webhook Stripe dah tolak baris ni ke keadaan
			// terminal SEBELUM UPDATE ni sempat — bukan dibetulkan reconcile
			// ni, elak kira dua kali.
			continue
		}
		summary.MismatchesFixed++
	}
}
