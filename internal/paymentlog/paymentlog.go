// Package paymentlog rekod PERISTIWA bayaran merentas semua modul
// (donation Stripe, yuran pendaftaran, yuran aktiviti — jadual
// payment_logs, migration 20260815060000). BUKAN internal/audit (delta
// perubahan MEDAN pada entiti yang boleh disunting) — ni log peristiwa
// append-only untuk diagnosis + asas internal/paymentreconcile.
//
// Keputusan produk 2026-08-15, lepas beberapa insiden webhook ToyyibPay
// (`;` mentah, `%` tak sah, billcode tak dijumpai) yang cuma dapat
// didiagnosis betul-betul lepas SSH terus ke Railway + query DB manual —
// sepatutnya boleh nampak terus dari satu jadual log kalau ni dah wujud
// dari awal.
package paymentlog

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
)

// Modul — set TETAP kecil (padan CHECK constraint jadual) sebab dipakai
// untuk retention/filter, beza drpd Event/Status di bawah yang sengaja
// bebas-bentuk.
const (
	ModuleDonation        = "donation"
	ModuleRegistrationFee = "registration_fee"
	ModuleActivityFee     = "activity_fee"
)

// Event — BUKAN senarai tertutup (tiada CHECK di DB, lihat migration).
// Ini nilai yang dipakai konsisten oleh handler sedia ada; caller lain
// boleh guna nilai baharu tanpa migration kalau bentuk baharu timbul.
const (
	EventCheckoutCreated     = "checkout_created"
	EventCheckoutFailed      = "checkout_failed"
	EventWebhookReceived     = "webhook_received"
	EventWebhookVerifyFailed = "webhook_verify_failed"
	EventStatusUpdated       = "status_updated"
	EventReconcileCheck      = "reconcile_check"
	EventReconcileMismatch   = "reconcile_mismatch_fixed"
)

// Status — bebas-bentuk (lihat Event), tapi handler sedia ada konsisten
// guna nilai ni.
const (
	StatusOK        = "ok"
	StatusError     = "error"
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusMismatch  = "mismatch"
)

// Entry — satu peristiwa bayaran untuk direkod.
type Entry struct {
	Module     string
	Event      string
	Status     string
	Gateway    string
	GatewayRef string // pilihan — kosong kalau belum ada bil (cth checkout gagal sebelum createBill)

	AmountCents *int64
	UserID      *uuid.UUID
	RelatedID   *uuid.UUID // id baris donations/registration_payments/activity_registrations

	Message string
	// RawPayload — badan webhook mentah atau respons poll gateway.
	// PILIHAN, tapi SANGAT digalakkan pada event webhook_* — inilah
	// yang akan elakkan pusingan diagnosis manual macam 2026-08-15.
	RawPayload []byte
}

// Record tulis satu peristiwa. BEST-EFFORT SENGAJA (beza drpd
// internal/audit.Record yang MESTI gagalkan seluruh permintaan) — log
// ni untuk diagnosis/reconcile, bukan invarian perniagaan yang
// menggerbang kelulusan/akses. Kegagalan tulis log tak patut gagalkan
// laluan bayaran sebenar (lebih-lebih lagi webhook, yang MESTI pulang
// 200 tak kira apa supaya gateway tak retry storm). Kegagalan dilog ke
// stdout, bukan dibuang senyap.
func Record(ctx context.Context, q *sqlc.Queries, e Entry) {
	params := sqlc.CreatePaymentLogParams{
		Module:  e.Module,
		Event:   e.Event,
		Status:  e.Status,
		Gateway: e.Gateway,
	}
	if e.GatewayRef != "" {
		params.GatewayRef = pgtype.Text{String: e.GatewayRef, Valid: true}
	}
	if e.AmountCents != nil {
		params.AmountCents = pgtype.Int4{Int32: int32(*e.AmountCents), Valid: true}
	}
	if e.UserID != nil {
		params.UserID = pgtype.UUID{Bytes: *e.UserID, Valid: true}
	}
	if e.RelatedID != nil {
		params.RelatedID = pgtype.UUID{Bytes: *e.RelatedID, Valid: true}
	}
	if e.Message != "" {
		params.Message = pgtype.Text{String: e.Message, Valid: true}
	}
	if len(e.RawPayload) > 0 {
		// text, BUKAN jsonb (Opus verify 2026-08-15) — callback ToyyibPay
		// form-urlencoded, bukan JSON; lajur jsonb tolak INSERT senyap
		// (Record best-effort) untuk DUA modul yang jadi sebab ciri ni
		// dibina. Simpan sebagai teks mentah, terima apa-apa bentuk.
		params.RawPayload = pgtype.Text{String: string(e.RawPayload), Valid: true}
	}

	if err := q.CreatePaymentLog(ctx, params); err != nil {
		log.Printf("paymentlog: gagal tulis log (module=%s event=%s gateway_ref=%s): %v", e.Module, e.Event, e.GatewayRef, err)
	}
}
