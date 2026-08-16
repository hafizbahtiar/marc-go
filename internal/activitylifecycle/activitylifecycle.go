// Package activitylifecycle jalankan dua peralihan status/notifikasi
// aktiviti berasaskan MASA, yang sebelum ni tiada apa-apa kod tulis:
//
//  1. Peringatan H-1 — aktiviti akan bermula dlm ~24 jam dapat push
//     sekali sahaja kepada semua yang berdaftar.
//  2. Auto-complete — aktiviti 'published' yang dah tamat sepenuhnya
//     (ends_at < now()) beralih status ke 'completed' automatik.
//
// Struktur ikut internal/activitysweep rapat (New/Start/RunOnce,
// ticker-based loop, sapuan idempoten berasaskan guard kolum, BUKAN
// kunci teragih — deskripsi penuh sebab tu ada dlm activitysweep.go).
// Kedua-dua sapuan di sini kongsi pakej (bukan dua pakej berasingan)
// sebab kedua-duanya operasi "aktiviti-berasaskan-masa" berkos rendah;
// padanan retention.RunOnce yang jalankan beberapa sapuan bebas dlm
// SATU pakej.
package activitylifecycle

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
	"marc/internal/push"
)

type Runner struct {
	queries  *sqlc.Queries
	push     *push.Service
	interval time.Duration
}

func New(queries *sqlc.Queries, pushSvc *push.Service, interval time.Duration) *Runner {
	return &Runner{queries: queries, push: pushSvc, interval: interval}
}

// Start jalankan sapuan dalam background sehingga ctx dibatalkan.
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

func (r *Runner) RunOnce(ctx context.Context) {
	r.sendReminders(ctx)
	r.completeEndedActivities(ctx)
}

// completeEndedActivities — lihat komen CompleteEndedActivities (SQL).
// Peralihan status semata-mata, tiada notifikasi/push (transisi
// housekeeping dalaman, bukan salah satu 4 jenis push yang dijanjikan
// spec asal).
func (r *Runner) completeEndedActivities(ctx context.Context) {
	completed, err := r.queries.CompleteEndedActivities(ctx)
	if err != nil {
		log.Printf("activitylifecycle: auto-complete gagal: %v", err)
		return
	}
	if len(completed) > 0 {
		log.Printf("activitylifecycle: %d aktiviti beralih ke completed", len(completed))
	}
}

// sendReminders — lihat komen ListActivitiesNeedingReminder/
// MarkActivityReminderSent (SQL) utk mekanisme dedup merentas replika.
//
// Tandakan `reminder_sent_at` SEBELUM push dihantar (bukan selepas) —
// sengaja: kalau push gagal separuh jalan (cth OneSignal down), lebih
// selamat SATU aktiviti terlepas sebahagian penerima drpd N replika
// yang masing-masing cuba semula tanpa had dan banjiri ahli dgn push
// berganda. Padanan falsafah "reminder dihantar" TODO.md — sekali
// cuba, bukan jaminan-sampai.
func (r *Runner) sendReminders(ctx context.Context) {
	activities, err := r.queries.ListActivitiesNeedingReminder(ctx)
	if err != nil {
		log.Printf("activitylifecycle: senarai aktiviti perlu peringatan gagal: %v", err)
		return
	}

	for _, a := range activities {
		affected, err := r.queries.MarkActivityReminderSent(ctx, a.ID)
		if err != nil {
			log.Printf("activitylifecycle: tanda reminder_sent_at gagal (aktiviti=%s): %v", a.ID, err)
			continue
		}
		if affected == 0 {
			// Replika lain menang race (guard reminder_sent_at is null) —
			// bukan ralat, cuma elak hantar push berganda.
			continue
		}

		regs, err := r.queries.ListRegistrationsByActivity(ctx, a.ID)
		if err != nil {
			log.Printf("activitylifecycle: senarai pendaftaran gagal (aktiviti=%s): %v", a.ID, err)
			continue
		}
		if len(regs) == 0 {
			continue
		}

		title := "Peringatan Aktiviti"
		message := a.Title + " bermula tidak lama lagi. Jangan terlepas!"
		for _, reg := range regs {
			r.notifyOne(ctx, reg.UserID, a.ID, title, message)
		}
	}
}

// notifyOne — cipta baris notifications + hantar push, padanan pola
// notifyMembers (internal/http/handlers/activities.go) tapi TAK boleh
// guna terus fungsi tu (unexported, pakej berlainan; job berjadual ni
// juga tiada "pelaku HTTP" utk actor_id). `actor_id` diset kepada
// PENERIMA SENDIRI (bukan akaun "sistem" — tiada satu wujud dlm skema
// ni) — actor_id NOT NULL + ON DELETE CASCADE pada users, jadi nilai
// mesti sentiasa wujud; guna id penerima menjamin itu tanpa perlu
// akaun sistem baharu.
func (r *Runner) notifyOne(ctx context.Context, userID, activityID uuid.UUID, title, message string) {
	if _, err := r.queries.CreateNotification(ctx, sqlc.CreateNotificationParams{
		RecipientID: userID,
		ActorID:     userID,
		Type:        "activity_reminder",
		ActivityID:  pgtype.UUID{Bytes: activityID, Valid: true},
	}); err != nil {
		log.Printf("activitylifecycle: cipta notifikasi gagal (user=%s, aktiviti=%s): %v", userID, activityID, err)
	}
	if err := r.push.NotifyUser(ctx, userID, title, message); err != nil {
		log.Printf("activitylifecycle: hantar push gagal (user=%s, aktiviti=%s): %v", userID, activityID, err)
	}
}
