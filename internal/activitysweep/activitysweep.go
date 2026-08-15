// Package activitysweep membatalkan pendaftaran aktiviti berbayar yang
// ditinggalkan separuh jalan — ahli mula checkout (payment_status='pending')
// tapi tak pernah selesaikan bayaran.
//
// Kenapa package berasingan drpd internal/reaper dan internal/retention
// (dua titik data sedia ada untuk keputusan ni):
//   - internal/reaper — khusus storan R2 (padam objek yatim/ditinggalkan).
//     Sapuan ni tak sentuh R2 langsung, jadi menambah di sini akan
//     mengelirukan skop package tu.
//   - internal/retention — polisi simpanan (buang/redaksi data LAMA yang
//     dah tak berguna). Sapuan ni bukan pembersihan data lapuk — ia
//     membetulkan STATE PERNIAGAAN (bebaskan slot kapasiti yang tersilap
//     dipegang), lebih dekat dengan konsep "reap stale reservation" macam
//     sweepAbandonedUploads (reaper) drpd "delete old audit rows"
//     (retention).
//
// Struktur ikut reaper.go rapat (New/Start/RunOnce, ticker-based loop) —
// satu concern, satu jadual, tiada dependency luar (tiada R2, tiada storan)
// yang buat "activitysweep" package sendiri lebih bersih drpd cuba muat ia
// dalam salah satu package sedia ada.
package activitysweep

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
)

// unstartedAfter — umur pendaftaran yang TAK PERNAH cuba checkout
// (tiada bil ToyyibPay wujud) sebelum dikira ditinggalkan. Selamat
// pendek — tiada webhook akan datang untuk baris begini.
const unstartedAfter = 45 * time.Minute

// unpaidBillAfter — umur pendaftaran yang DAH cuba checkout (bil
// ToyyibPay wujud) sebelum dikira ditinggalkan. Sengaja JAUH lebih
// panjang drpd unstartedAfter (Opus verify 2026-08-15 dedah: cutoff
// pendek di sini boleh cetus race dengan webhook lewat — ahli bayar
// SELEPAS baris dibatal, jadi "hilang" senyap. Lihat komen penuh pada
// query CancelStaleUnpaidBills). Risiko kapasiti terikat lebih lama
// diterima sebagai kos yang lebih rendah drpd risiko kehilangan
// bayaran ahli.
const unpaidBillAfter = 24 * time.Hour

type Sweeper struct {
	queries  *sqlc.Queries
	interval time.Duration
}

func New(queries *sqlc.Queries, interval time.Duration) *Sweeper {
	return &Sweeper{queries: queries, interval: interval}
}

// Start jalankan sapuan dalam background sehingga ctx dibatalkan.
func (s *Sweeper) Start(ctx context.Context) {
	go func() {
		// Satu pusingan sebaik boot: kalau proses terbunuh dengan
		// pendaftaran tertunggak, ia tak patut tunggu satu interval penuh.
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

// RunOnce batalkan semua pendaftaran aktiviti pending-payment yang dah
// lapuk. Diekspos supaya boleh dipanggil terus dalam ujian dan bukan
// menunggu ticker.
func (s *Sweeper) RunOnce(ctx context.Context) {
	unstartedCutoff := pgtype.Timestamptz{Time: time.Now().Add(-unstartedAfter), Valid: true}
	unstarted, err := s.queries.CancelStaleUnstartedPayments(ctx, unstartedCutoff)
	if err != nil {
		log.Printf("activitysweep: batal pendaftaran belum-checkout lapuk gagal: %v", err)
	} else if len(unstarted) > 0 {
		log.Printf("activitysweep: %d pendaftaran aktiviti belum-checkout dibatalkan (lapuk, >%s)", len(unstarted), unstartedAfter)
	}

	billCutoff := pgtype.Timestamptz{Time: time.Now().Add(-unpaidBillAfter), Valid: true}
	unpaidBills, err := s.queries.CancelStaleUnpaidBills(ctx, billCutoff)
	if err != nil {
		log.Printf("activitysweep: batal pendaftaran bil-belum-dibayar lapuk gagal: %v", err)
	} else if len(unpaidBills) > 0 {
		log.Printf("activitysweep: %d pendaftaran aktiviti bil-belum-dibayar dibatalkan (lapuk, >%s)", len(unpaidBills), unpaidBillAfter)
	}
}
