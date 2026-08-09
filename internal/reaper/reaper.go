// Package reaper buang objek R2 yang tiada sesiapa rujuk lagi.
//
// Ada DUA punca sampah, dan dua-dua pernah bocor senyap-senyap:
//
//  1. Post dipadam. Post guna soft delete (deleted_at) untuk jejak audit,
//     jadi baris post_images kekal — tapi gambar itu sendiri takkan
//     dipaparkan lagi, jadi tiada sebab ia terus makan storan.
//  2. Karangan post ditinggalkan. Gambar naik ke R2 sebaik dipilih, jadi
//     sesiapa yang pilih gambar lalu tekan "back" tinggalkan objek yatim
//     yang tiada apa-apa pun merujuknya.
//
// Dua-dua diuruskan dengan cara sama: masukkan kunci ke dalam gilir
// `deleted_uploads`, kemudian saluran ni cuba padam sampai berjaya. Padam
// R2 boleh gagal; gilir bermakna kegagalan dicuba semula dan bukan
// dilupakan.
package reaper

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
	"marc/internal/storage"
)

const (
	// Berapa banyak objek dibersihkan setiap pusingan. Kecil sengaja —
	// pembersihan tak mendesak, dan ini menghadkan kesan kalau bucket
	// sedang bermasalah.
	batchSize = 50

	// Umur sebelum pending upload dikira ditinggalkan. Mesti JAUH lebih
	// panjang daripada masa mengarang post yang munasabah: menyapu terlalu
	// awal akan memadam gambar yang pengguna masih dalam proses hantar.
	abandonedAfter = 6 * time.Hour
)

type Reaper struct {
	queries  *sqlc.Queries
	r2       *storage.R2Client
	interval time.Duration
}

func New(queries *sqlc.Queries, r2 *storage.R2Client, interval time.Duration) *Reaper {
	return &Reaper{queries: queries, r2: r2, interval: interval}
}

// Start jalankan reaper dalam background sehingga ctx dibatalkan.
//
// Tiada penyelarasan antara replika sengaja: memadam objek R2 yang sama
// dua kali tak apa-apa (DeleteObject idempotent), jadi kunci teragih cuma
// akan menambah kerumitan tanpa faedah.
func (r *Reaper) Start(ctx context.Context) {
	if !r.r2.Enabled() {
		log.Printf("reaper: R2 tak dikonfigur — pembersihan storan dilangkau")
		return
	}

	go func() {
		// Satu pusingan sebaik boot: kalau proses terbunuh dengan kerja
		// tertunggak, ia tak patut menunggu satu interval penuh.
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

// RunOnce laksana satu pusingan pembersihan penuh. Diekspos supaya boleh
// dipanggil terus dalam ujian dan bukan menunggu ticker.
func (r *Reaper) RunOnce(ctx context.Context) {
	r.enqueueOrphanedPostImages(ctx)
	r.sweepAbandonedUploads(ctx)
	r.drainDeleteQueue(ctx)
}

// enqueueOrphanedPostImages tangkap gambar milik post yang dah dipadam
// tapi belum pernah digilir. Ini yang membersihkan post yang dipadam
// SEBELUM gilir ni wujud — tanpanya, sampah sedia ada kekal selamanya
// sebab tiada apa yang pernah merujuknya semula.
func (r *Reaper) enqueueOrphanedPostImages(ctx context.Context) {
	keys, err := r.queries.ListOrphanedPostImageKeys(ctx, batchSize)
	if err != nil {
		log.Printf("reaper: senarai gambar post yatim gagal: %v", err)
		return
	}
	for _, key := range keys {
		if err := r.queries.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
			R2Key:  key,
			Reason: "post_deleted",
		}); err != nil {
			log.Printf("reaper: gilir gambar yatim %s gagal: %v", key, err)
		}
	}
	if len(keys) > 0 {
		log.Printf("reaper: %d gambar post yang dipadam dimasukkan ke gilir", len(keys))
	}
}

// sweepAbandonedUploads pindahkan pending upload yang dah tua ke dalam
// gilir padam. Ia tak memadam dari R2 terus — biar drainDeleteQueue jadi
// satu-satunya tempat yang sentuh R2, supaya retry/backoff ada satu
// laluan sahaja.
func (r *Reaper) sweepAbandonedUploads(ctx context.Context) {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-abandonedAfter), Valid: true}
	stale, err := r.queries.ListStalePendingUploads(ctx, sqlc.ListStalePendingUploadsParams{
		CreatedAt: cutoff,
		Limit:     batchSize,
	})
	if err != nil {
		log.Printf("reaper: senarai pending upload tertunggak gagal: %v", err)
		return
	}

	for _, p := range stale {
		if err := r.queries.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
			R2Key:  p.R2Key,
			Reason: "upload_abandoned",
		}); err != nil {
			log.Printf("reaper: gilir %s gagal: %v", p.R2Key, err)
			continue
		}
		if err := r.queries.DeletePendingUploadByKey(ctx, p.R2Key); err != nil {
			log.Printf("reaper: buang baris pending %s gagal: %v", p.R2Key, err)
		}
	}

	if len(stale) > 0 {
		log.Printf("reaper: %d upload ditinggalkan dimasukkan ke gilir padam", len(stale))
	}
}

func (r *Reaper) drainDeleteQueue(ctx context.Context) {
	due, err := r.queries.ListDueDeletedUploads(ctx, batchSize)
	if err != nil {
		log.Printf("reaper: senarai gilir padam gagal: %v", err)
		return
	}

	var deleted int
	for _, item := range due {
		if err := r.r2.DeleteImage(ctx, item.R2Key); err != nil {
			log.Printf("reaper: padam R2 %s gagal (cubaan %d): %v", item.R2Key, item.Attempts+1, err)
			if merr := r.queries.MarkDeletedUploadFailed(ctx, sqlc.MarkDeletedUploadFailedParams{
				R2Key:     item.R2Key,
				LastError: pgtype.Text{String: truncate(err.Error(), 500), Valid: true},
			}); merr != nil {
				log.Printf("reaper: tanda gagal %s: %v", item.R2Key, merr)
			}
			continue
		}
		if err := r.queries.MarkDeletedUploadDone(ctx, item.R2Key); err != nil {
			// Objek dah tiada dalam R2 tapi batu nisan tak tertulis —
			// pusingan seterusnya cuma akan padam sekali lagi (idempotent),
			// jadi ini selamat untuk sekadar dilog.
			log.Printf("reaper: tanda siap %s gagal: %v", item.R2Key, err)
			continue
		}
		deleted++
	}

	if deleted > 0 {
		log.Printf("reaper: %d objek R2 dipadam", deleted)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
