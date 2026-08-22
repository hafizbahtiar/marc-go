package activitylifecycle

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/onesignal"
	"marc/internal/push"
)

// Dua sapuan berasaskan MASA yang berjalan tanpa manusia dalam gelung.
// Sebelum ni `no test files` (TODO.md L36).
//
// Yang paling perlu dilindungi ialah guard dedup (`reminder_sent_at is
// null`, `status = 'published'`): ia satu-satunya perkara yang
// menghalang N replika daripada menghantar N push kepada setiap
// pendaftar. Ia tak kelihatan dalam ujian satu-proses melainkan diuji
// secara eksplisit, kerana satu proses yang menjalankan sapuan sekali
// nampak betul sepenuhnya.
//
//	LIFECYCLE_TEST_DB="postgres://localhost:5432/marc_lifecycle_check?sslmode=disable" \
//	  go test ./internal/activitylifecycle/ -v
func setup(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, *Runner, context.Context) {
	t.Helper()
	dbURL := os.Getenv("LIFECYCLE_TEST_DB")
	if dbURL == "" {
		t.Skip("set LIFECYCLE_TEST_DB kepada DB buangan")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	q := sqlc.New(pool)
	// OneSignal tanpa kredential = `Enabled()` false, jadi `NotifyUser`
	// no-op senyap tanpa rangkaian. Itu tepat yang dimahukan: yang diuji
	// di sini ialah peralihan DB dan dedup, bukan penghantaran push.
	pushSvc := push.NewService(q, onesignal.NewClient("", ""))
	return pool, q, New(q, pushSvc, time.Hour), ctx
}

// seedActivity cipta aktiviti dengan tetingkap masa dan status yang
// diminta. `startsIn` boleh negatif (aktiviti sudah bermula/tamat).
func seedActivity(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, status string, startsIn, duration time.Duration,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activities (category_id, title, description, location_name,
		   location_address, starts_at, ends_at, registration_closes_at,
		   capacity, fee_cents, attendance_threshold_pct, status)
		 values ((select id from activity_categories limit 1), 'Ujian kitaran', '', 'Dewan',
		   '', now() + $1::interval, now() + $1::interval + $2::interval,
		   now() - interval '1 day', null, 0, 100, $3)
		 returning id`,
		startsIn.String(), duration.String(), status).Scan(&id); err != nil {
		t.Fatalf("seed activity (status=%s): %v", status, err)
	}
	return id
}

func seedRegistrant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, activityID uuid.UUID) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"lc-"+uuid.NewString()+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id)
		 values ($1, $2, (select id from roles where key = 'ahli'))`,
		userID, "LC/"+uuid.NewString()[:12]); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into activity_registrations
		   (activity_id, user_id, status, payment_status, checkin_token)
		 values ($1, $2, 'registered', 'not_required', $3)`,
		activityID, userID, uuid.NewString()); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	return userID
}

func statusOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `select status from activities where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("baca status: %v", err)
	}
	return s
}

func countNotifications(t *testing.T, ctx context.Context, pool *pgxpool.Pool, activityID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`select count(*) from notifications where activity_id = $1 and type = 'activity_reminder'`,
		activityID).Scan(&n); err != nil {
		t.Fatalf("kira notifikasi: %v", err)
	}
	return n
}

// ---- Auto-complete ----

func TestAutoCompleteAktivitiYangDahTamat(t *testing.T) {
	pool, _, r, ctx := setup(t)

	tamat := seedActivity(t, ctx, pool, "published", -48*time.Hour, 2*time.Hour)
	akanDatang := seedActivity(t, ctx, pool, "published", 48*time.Hour, 2*time.Hour)
	// Sedang BERJALAN — sudah bermula tapi belum tamat.
	sedangJalan := seedActivity(t, ctx, pool, "published", -1*time.Hour, 3*time.Hour)

	r.RunOnce(ctx)

	if got := statusOf(t, ctx, pool, tamat); got != "completed" {
		t.Errorf("aktiviti yang dah tamat: status = %q, mahu \"completed\"", got)
	}
	if got := statusOf(t, ctx, pool, akanDatang); got != "published" {
		t.Errorf("aktiviti AKAN DATANG ditanda %q", got)
	}
	if got := statusOf(t, ctx, pool, sedangJalan); got != "published" {
		t.Errorf("aktiviti yang SEDANG BERJALAN ditanda %q — kehadiran masih "+
			"boleh ditanda, ia belum selesai", got)
	}
}

// Guard `status = 'published'` — draf dan yang dibatalkan tak boleh
// dinaikkan ke 'completed' hanya kerana tarikhnya berlalu. Aktiviti yang
// DIBATALKAN khususnya: menandanya 'completed' akan menjadikannya layak
// untuk penerbitan sijil.
func TestAutoCompleteTidakSentuhDrafAtauYangDibatalkan(t *testing.T) {
	pool, _, r, ctx := setup(t)

	for _, status := range []string{"draft", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			id := seedActivity(t, ctx, pool, status, -48*time.Hour, 2*time.Hour)

			r.RunOnce(ctx)

			if got := statusOf(t, ctx, pool, id); got != status {
				t.Errorf("aktiviti %q ditukar jadi %q", status, got)
			}
		})
	}
}

// ---- Peringatan H-1 ----

func TestPeringatanDihantarSekaliSahaja(t *testing.T) {
	pool, _, r, ctx := setup(t)

	// Dalam tetingkap H-1: bermula dalam ~12 jam.
	id := seedActivity(t, ctx, pool, "published", 12*time.Hour, 2*time.Hour)
	seedRegistrant(t, ctx, pool, id)
	seedRegistrant(t, ctx, pool, id)

	r.RunOnce(ctx)

	if got := countNotifications(t, ctx, pool, id); got != 2 {
		t.Fatalf("notifikasi selepas pusingan pertama = %d, mahu 2 (satu setiap pendaftar)", got)
	}

	// Pusingan kedua = ticker seterusnya dalam proses yang SAMA.
	// `ListActivitiesNeedingReminder` yang menahannya di sini (ia menapis
	// `reminder_sent_at is null`), BUKAN guard `affected == 0` dalam Go.
	// Lihat TestMarkReminderGuardMenangRaceReplika untuk guard itu.
	r.RunOnce(ctx)

	if got := countNotifications(t, ctx, pool, id); got != 2 {
		t.Errorf("notifikasi selepas pusingan KEDUA = %d, mahu kekal 2 — "+
			"tapisan `reminder_sent_at is null` hilang, setiap ticker akan "+
			"membanjiri pendaftar dgn push berulang", got)
	}
}

// Guard cross-replika, diuji TERUS pada querynya.
//
// `RunOnce` yang dijalankan dua kali dalam satu proses TIDAK menguji ini:
// pusingan kedua tak pernah melihat aktiviti itu langsung, kerana
// `ListActivitiesNeedingReminder` sudah menapisnya. Yang menahan race
// SEBENAR ialah `where reminder_sent_at is null` pada UPDATE — bila DUA
// replika menyenaraikan baris yang sama SEBELUM salah satu sempat
// menandanya.
//
// Disahkan melalui ujian mutasi 2026-08-22: membuang guard `affected == 0`
// dalam Go TIDAK menggagalkan ujian di atas, kerana lapisan senarai
// menutupnya dahulu. Guard sebenar hidup dalam SQL, jadi di situ ia diuji.
func TestMarkReminderGuardMenangRaceReplika(t *testing.T) {
	pool, q, _, ctx := setup(t)

	id := seedActivity(t, ctx, pool, "published", 12*time.Hour, 2*time.Hour)

	// Replika A menang.
	pertama, err := q.MarkActivityReminderSent(ctx, id)
	if err != nil {
		t.Fatalf("MarkActivityReminderSent (pertama): %v", err)
	}
	if pertama != 1 {
		t.Fatalf("baris terjejas (pertama) = %d, mahu 1", pertama)
	}

	// Replika B menyenaraikan baris yang SAMA sebelum A menandanya, lalu
	// cuba menandanya juga. Mesti mengena SIFAR baris.
	kedua, err := q.MarkActivityReminderSent(ctx, id)
	if err != nil {
		t.Fatalf("MarkActivityReminderSent (kedua): %v", err)
	}
	if kedua != 0 {
		t.Errorf("baris terjejas (kedua) = %d, mahu 0 — guard "+
			"`reminder_sent_at is null` hilang daripada UPDATE, jadi N replika "+
			"akan menghantar N push kepada setiap pendaftar", kedua)
	}
}

// Tetingkap H-1 ialah `starts_at > now() and starts_at <= now() + 24h`.
// Sempadan bawah (`starts_at > now()`) penting: tanpanya, aktiviti yang
// SUDAH bermula — atau yang sudah lama berlalu, kalau sapuan tak jalan
// sekian lama — akan mencetuskan peringatan "bermula tidak lama lagi".
func TestPeringatanHanyaDalamTetingkapH1(t *testing.T) {
	pool, _, r, ctx := setup(t)

	terlaluAwal := seedActivity(t, ctx, pool, "published", 72*time.Hour, 2*time.Hour)
	sudahBermula := seedActivity(t, ctx, pool, "published", -2*time.Hour, 6*time.Hour)
	seedRegistrant(t, ctx, pool, terlaluAwal)
	seedRegistrant(t, ctx, pool, sudahBermula)

	r.RunOnce(ctx)

	if got := countNotifications(t, ctx, pool, terlaluAwal); got != 0 {
		t.Errorf("aktiviti 72 jam lagi dapat %d peringatan — terlalu awal", got)
	}
	if got := countNotifications(t, ctx, pool, sudahBermula); got != 0 {
		t.Errorf("aktiviti yang SUDAH bermula dapat %d peringatan "+
			"(\"bermula tidak lama lagi\")", got)
	}
}

// Draf tak boleh menghantar peringatan — ahli tak sepatutnya tahu ia
// wujud pun.
func TestPeringatanTidakDihantarUntukDraf(t *testing.T) {
	pool, _, r, ctx := setup(t)

	id := seedActivity(t, ctx, pool, "draft", 12*time.Hour, 2*time.Hour)
	seedRegistrant(t, ctx, pool, id)

	r.RunOnce(ctx)

	if got := countNotifications(t, ctx, pool, id); got != 0 {
		t.Errorf("aktiviti DRAF menghantar %d peringatan — ahli tak sepatutnya "+
			"tahu ia wujud", got)
	}
}

// Pendaftaran yang DIBATALKAN tak boleh menerima peringatan.
// `ListRegistrationsByActivity` yang menapisnya — ujian ni mengunci
// kebergantungan itu supaya menukar query tu tak senyap memulakan
// penghantaran push kepada orang yang sudah batal.
func TestPeringatanTidakDihantarKepadaYangSudahBatal(t *testing.T) {
	pool, _, r, ctx := setup(t)

	id := seedActivity(t, ctx, pool, "published", 12*time.Hour, 2*time.Hour)
	userID := seedRegistrant(t, ctx, pool, id)
	if _, err := pool.Exec(ctx,
		`update activity_registrations set status = 'cancelled', cancelled_at = now()
		 where activity_id = $1 and user_id = $2`, id, userID); err != nil {
		t.Fatalf("batalkan pendaftaran: %v", err)
	}

	r.RunOnce(ctx)

	if got := countNotifications(t, ctx, pool, id); got != 0 {
		t.Errorf("pendaftaran DIBATALKAN dapat %d peringatan", got)
	}
}
