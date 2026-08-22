package activitysweep

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
)

// Sapuan ni membatalkan pendaftaran — tindakan MUSNAH yang berjalan
// tanpa manusia dalam gelung. Sebelum ni `no test files` (TODO.md L36).
//
// Yang paling perlu dilindungi ialah DUA cutoff yang sengaja jauh
// berbeza (45 minit lwn 24 jam). Menyamakannya adalah "pembersihan" yang
// nampak munasabah sepenuhnya semasa membaca kod — dan ia akan
// membatalkan pendaftaran yang bilnya masih boleh dibayar, menghasilkan
// baris cancelled+paid yang memerlukan campur tangan manual. Ujian di
// sini menjadikan penyamaan itu gagal dengan kuat.
//
//	ACTIVITYSWEEP_TEST_DB="postgres://localhost:5432/marc_sweep_check?sslmode=disable" \
//	  go test ./internal/activitysweep/ -v
func setup(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, context.Context) {
	t.Helper()
	dbURL := os.Getenv("ACTIVITYSWEEP_TEST_DB")
	if dbURL == "" {
		t.Skip("set ACTIVITYSWEEP_TEST_DB kepada DB buangan")
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
	return pool, sqlc.New(pool), ctx
}

// seedRegistration cipta aktiviti berbayar + satu pendaftaran
// `payment_status='pending'`, ditua kepada `age`.
//
// `paymentRef` kosong = ahli tak pernah cuba checkout (tiada bil).
func seedRegistration(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, paymentRef string, age time.Duration,
) uuid.UUID {
	t.Helper()

	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"sweep-"+uuid.NewString()+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id)
		 values ($1, $2, (select id from roles where key = 'ahli'))`,
		userID, "SW/"+uuid.NewString()[:12]); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	var activityID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activities (category_id, title, description, location_name,
		   location_address, starts_at, ends_at, registration_closes_at,
		   capacity, fee_cents, attendance_threshold_pct, status)
		 values ((select id from activity_categories limit 1), 'Ujian sapuan', '', 'Dewan',
		   '', now() + interval '10 days', now() + interval '10 days' + interval '2 hours',
		   now() + interval '9 days', null, 1000, 100, 'published')
		 returning id`).Scan(&activityID); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	var ref any
	if paymentRef != "" {
		ref = paymentRef
	}

	var regID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activity_registrations
		   (activity_id, user_id, status, payment_status, payment_ref, checkin_token, registered_at)
		 values ($1, $2, 'registered', 'pending', $3, $4, now() - $5::interval)
		 returning id`,
		activityID, userID, ref, uuid.NewString(), age.String()).Scan(&regID); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	return regID
}

func statusOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, regID uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx,
		`select status from activity_registrations where id = $1`, regID).Scan(&status); err != nil {
		t.Fatalf("baca status: %v", err)
	}
	return status
}

// Cabang 1: tak pernah cuba checkout (`payment_ref is null`).
// Selamat dibatalkan cepat — tiada bil wujud, jadi tiada webhook boleh
// tiba untuk baris ni.
func TestBatalPendaftaranYangTakPernahCheckout(t *testing.T) {
	pool, q, ctx := setup(t)

	lapuk := seedRegistration(t, ctx, pool, "", unstartedAfter+10*time.Minute)
	baharu := seedRegistration(t, ctx, pool, "", unstartedAfter-10*time.Minute)

	New(q, time.Minute).RunOnce(ctx)

	if got := statusOf(t, ctx, pool, lapuk); got != "cancelled" {
		t.Errorf("pendaftaran lapuk tanpa bil: status = %q, mahu \"cancelled\" — "+
			"slot kapasiti kekal terikat selamanya", got)
	}
	if got := statusOf(t, ctx, pool, baharu); got != "registered" {
		t.Errorf("pendaftaran BAHARU dibatalkan (status = %q) — ahli yang "+
			"sedang di halaman bayaran akan hilang slotnya", got)
	}
}

// Cabang 2: bil ToyyibPay SUDAH dicipta. Cutoff di sini sengaja JAUH
// lebih panjang (24 jam lwn 45 minit) — FPX/bank boleh ambil berjam-jam,
// dan membatalkan awal bermakna webhook yang tiba kemudian menanda
// `payment_status='paid'` atas baris `status='cancelled'`: ahli sudah
// BAYAR tetapi slotnya hilang.
//
// Ujian ni gagal kalau kedua-dua cutoff disamakan.
func TestBilBelumDibayarGunaCutoffJauhLebihPanjang(t *testing.T) {
	pool, q, ctx := setup(t)

	// Lebih tua drpd cutoff "tak pernah checkout", TAPI lebih muda drpd
	// cutoff bil. MESTI selamat.
	dalamTetingkap := seedRegistration(t, ctx, pool, "BILL-"+uuid.NewString()[:8], unstartedAfter+time.Hour)
	// Melepasi cutoff bil sebenar.
	lapukBenar := seedRegistration(t, ctx, pool, "BILL-"+uuid.NewString()[:8], unpaidBillAfter+time.Hour)

	New(q, time.Minute).RunOnce(ctx)

	if got := statusOf(t, ctx, pool, dalamTetingkap); got != "registered" {
		t.Errorf("bil berumur %v dibatalkan (status = %q) — cutoff bil nampak "+
			"dah disamakan dgn cutoff tak-pernah-checkout (%v). Ahli yang "+
			"bayar lewat akan hilang slot walau dah bayar",
			unstartedAfter+time.Hour, got, unstartedAfter)
	}
	if got := statusOf(t, ctx, pool, lapukBenar); got != "cancelled" {
		t.Errorf("bil berumur > %v tidak dibatalkan (status = %q) — slot "+
			"terikat selamanya", unpaidBillAfter, got)
	}
}

// Guard `status <> 'cancelled'` pada kedua-dua query: tanpa ia, baris
// yang SUDAH dibatalkan kena UPDATE semula setiap 15 minit selama-lamanya
// — menulis ganti `cancelled_at` (merosakkan jejak "bila SEBENAR ia
// dibatalkan") dan mengembungkan kiraan yang dilog.
func TestBarisYangSudahDibatalkanTidakDisentuhSemula(t *testing.T) {
	pool, q, ctx := setup(t)

	regID := seedRegistration(t, ctx, pool, "", unstartedAfter+time.Hour)

	New(q, time.Minute).RunOnce(ctx)

	var pertama time.Time
	if err := pool.QueryRow(ctx,
		`select cancelled_at from activity_registrations where id = $1`, regID).Scan(&pertama); err != nil {
		t.Fatalf("baca cancelled_at: %v", err)
	}

	// Pusingan kedua tak boleh menyentuhnya lagi.
	New(q, time.Minute).RunOnce(ctx)

	var kedua time.Time
	if err := pool.QueryRow(ctx,
		`select cancelled_at from activity_registrations where id = $1`, regID).Scan(&kedua); err != nil {
		t.Fatalf("baca cancelled_at: %v", err)
	}

	if !pertama.Equal(kedua) {
		t.Errorf("cancelled_at ditulis ganti pada pusingan kedua (%v → %v) — "+
			"guard `status <> 'cancelled'` hilang, jejak masa pembatalan rosak",
			pertama, kedua)
	}
}

// Sapuan ni HANYA untuk aktiviti berbayar yang belum dibayar. Pendaftaran
// percuma (`payment_status='not_required'`) dan yang SUDAH dibayar
// ('paid') tak boleh disentuh walau berapa lama pun umurnya.
func TestTidakSentuhPendaftaranPercumaAtauYangSudahDibayar(t *testing.T) {
	pool, q, ctx := setup(t)

	for _, status := range []string{"not_required", "paid"} {
		t.Run(status, func(t *testing.T) {
			regID := seedRegistration(t, ctx, pool, "BILL-"+uuid.NewString()[:8], unpaidBillAfter+48*time.Hour)
			if _, err := pool.Exec(ctx,
				`update activity_registrations set payment_status = $1 where id = $2`,
				status, regID); err != nil {
				t.Fatalf("set payment_status: %v", err)
			}

			New(q, time.Minute).RunOnce(ctx)

			if got := statusOf(t, ctx, pool, regID); got != "registered" {
				t.Errorf("pendaftaran payment_status=%q dibatalkan (status = %q)",
					status, got)
			}
		})
	}
}
