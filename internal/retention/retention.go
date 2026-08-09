// Package retention kuatkuasakan berapa lama data disimpan.
//
// Tiga sapuan berasingan, sengaja dengan tempoh yang BERBEZA-beza kerana
// data yang berlainan mempunyai justifikasi simpanan yang berlainan:
//
//	Redaksi PII audit  — buang ip_address/user_agent (data peribadi),
//	                     catatan audit itu sendiri DIKEKALKAN
//	Padam audit        — buang catatan sepenuhnya bila dah tak berguna
//	Prune batu nisan   — kemas rekod perkemasan R2 yang dah selesai
//
// Perbezaan antara dua yang pertama ialah keseluruhan idea di sini.
// "Siapa naikkan pangkat siapa pada Mac lepas" berbaloi disimpan lama;
// "dari alamat IP mana" tidak. Menggabungkan keduanya memaksa pilihan
// palsu antara jejak audit yang berguna dan simpanan data peribadi yang
// minimum — sedangkan kedua-duanya boleh dicapai serentak.
package retention

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
)

// Policy — tempoh simpanan. Sifar bermakna sapuan itu dimatikan.
type Policy struct {
	// AuditPII — umur sebelum ip_address/user_agent diredaksi.
	AuditPII time.Duration
	// AuditRecord — umur sebelum catatan audit dipadam terus.
	AuditRecord time.Duration
	// UploadTombstone — umur sebelum baris deleted_uploads yang selesai
	// dibuang.
	UploadTombstone time.Duration
}

type Runner struct {
	queries  *sqlc.Queries
	policy   Policy
	interval time.Duration
}

func New(queries *sqlc.Queries, policy Policy, interval time.Duration) *Runner {
	return &Runner{queries: queries, policy: policy, interval: interval}
}

// Start jalankan sapuan simpanan dalam background sehingga ctx dibatalkan.
func (r *Runner) Start(ctx context.Context) {
	go func() {
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

// RunOnce laksana satu pusingan penuh. Diekspos untuk ujian.
func (r *Runner) RunOnce(ctx context.Context) {
	// Redaksi didahulukan: kalau padam catatan berjalan dulu dan gagal,
	// kita masih mahu PII yang dah tamat tempoh dibuang pusingan ni.
	if r.policy.AuditPII > 0 {
		n, err := r.queries.RedactAuditLogPIIBefore(ctx, cutoff(r.policy.AuditPII))
		switch {
		case err != nil:
			log.Printf("retention: redaksi PII audit gagal: %v", err)
		case n > 0:
			log.Printf("retention: %d catatan audit diredaksi (ip/user_agent dibuang)", n)
		}
	}

	if r.policy.AuditRecord > 0 {
		n, err := r.queries.DeleteAuditLogsBefore(ctx, cutoff(r.policy.AuditRecord))
		switch {
		case err != nil:
			log.Printf("retention: padam catatan audit gagal: %v", err)
		case n > 0:
			log.Printf("retention: %d catatan audit dipadam", n)
		}
	}

	if r.policy.UploadTombstone > 0 {
		n, err := r.queries.DeleteDoneDeletedUploadsBefore(ctx, cutoff(r.policy.UploadTombstone))
		switch {
		case err != nil:
			log.Printf("retention: prune batu nisan upload gagal: %v", err)
		case n > 0:
			log.Printf("retention: %d batu nisan upload dibuang", n)
		}
	}
}

func cutoff(age time.Duration) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().Add(-age), Valid: true}
}
