package paymentreconcile

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/payment"
)

// Pakej ni menulis ganti status bayaran SECARA AUTOMATIK berdasarkan
// jawapan gateway, tanpa manusia dalam gelung. `RunOnce` sengaja
// diekspos "supaya boleh dipanggil terus dalam ujian" — dan ujian itu
// tak pernah ditulis (TODO.md L36). Ini menutupnya.
//
// Gateway ialah ANTARA MUKA (`payment.Gateway`), jadi ia dipalsukan di
// sini dan keputusan reconcile jadi deterministik. Hanya DB yang perlu
// nyata, kerana kekangan yang membentuk logik ni (`CHECK` pada
// `payment_status` yang TIADA 'failed', guard `status <> 'succeeded'`)
// hidup dalam skema:
//
//	RECONCILE_TEST_DB="postgres://localhost:5432/marc_reconcile_check?sslmode=disable" \
//	  go test ./internal/paymentreconcile/ -v
//
// NOTA keadaan dikongsi: setiap ujian menyemai barisnya sendiri dengan
// rujukan RAWAK, tapi kesemuanya berkongsi satu DB dan `RunOnce`
// memproses SETIAP baris yang layak. Jadi penegasan mesti dibuat pada
// baris/rujukan yang ujian itu sendiri cipta — BUKAN pada medan agregat
// `ReconcileSummary`, yang membawa kerja daripada ujian lain.

// fakeGateway — jawapan CheckStatus ditetapkan per-rujukan.
type fakeGateway struct {
	name string

	mu       sync.Mutex
	statuses map[string]string // gatewayRef → status
	errs     map[string]error  // gatewayRef → ralat
	calls    map[string]int    // gatewayRef → bilangan panggilan
}

func newFakeGateway(name string) *fakeGateway {
	return &fakeGateway{
		name:     name,
		statuses: map[string]string{},
		errs:     map[string]error{},
		calls:    map[string]int{},
	}
}

func (f *fakeGateway) Name() string  { return f.name }
func (f *fakeGateway) Enabled() bool { return true }

func (f *fakeGateway) CreatePayment(context.Context, payment.CreateParams) (payment.CreateResult, error) {
	return payment.CreateResult{}, errors.New("tak dipakai dalam ujian reconcile")
}

func (f *fakeGateway) VerifyWebhook([]byte, http.Header) (payment.WebhookEvent, error) {
	return payment.WebhookEvent{}, errors.New("tak dipakai dalam ujian reconcile")
}

func (f *fakeGateway) CheckStatus(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[ref]++
	if err, ok := f.errs[ref]; ok {
		return "", err
	}
	if s, ok := f.statuses[ref]; ok {
		return s, nil
	}
	// Lalai padan gelagat sebenar bil yang tak pernah dibayar.
	return "pending", nil
}

func (f *fakeGateway) setStatus(ref, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[ref] = status
}

func (f *fakeGateway) setErr(ref string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[ref] = err
}

func (f *fakeGateway) callCount(ref string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[ref]
}

func setup(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, *fakeGateway, *Reconciler, context.Context) {
	t.Helper()
	dbURL := os.Getenv("RECONCILE_TEST_DB")
	if dbURL == "" {
		t.Skip("set RECONCILE_TEST_DB kepada DB buangan")
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
	// Satu instance dikongsi tiga kunci — padan wiring cmd/api/main.go,
	// di mana "toyyibpay" dan "toyyibpay-activity" ialah dua instance
	// dengan kredential SAMA.
	gw := newFakeGateway("toyyibpay")
	gateways := map[string]payment.Gateway{
		"toyyibpay":          gw,
		"toyyibpay-activity": gw,
		"stripe":             gw,
	}
	return pool, q, gw, New(q, gateways, time.Hour), ctx
}

func seedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"rec-"+uuid.NewString()+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id)
		 values ($1, $2, (select id from roles where key = 'ahli'))`,
		userID, "RC/"+uuid.NewString()[:12]); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return userID
}

func seedRegistrationPayment(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, ref string, age time.Duration,
) uuid.UUID {
	t.Helper()
	userID := seedUser(t, ctx, pool)
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into registration_payments
		   (user_id, amount_cents, currency, gateway, gateway_ref, status, created_at)
		 values ($1, 1000, 'myr', 'toyyibpay', $2, 'pending', now() - $3::interval)
		 returning id`,
		userID, ref, age.String()).Scan(&id); err != nil {
		t.Fatalf("seed registration_payment: %v", err)
	}
	return id
}

func regPaymentStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx,
		`select status from registration_payments where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("baca status: %v", err)
	}
	return s
}

// ---- Yuran pendaftaran ----

func TestReconcileBetulkanYuranPendaftaran(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	refBerjaya := "BILL-ok-" + uuid.NewString()[:8]
	refGagal := "BILL-fail-" + uuid.NewString()[:8]
	refPending := "BILL-pend-" + uuid.NewString()[:8]

	idBerjaya := seedRegistrationPayment(t, ctx, pool, refBerjaya, minAge+time.Hour)
	idGagal := seedRegistrationPayment(t, ctx, pool, refGagal, minAge+time.Hour)
	idPending := seedRegistrationPayment(t, ctx, pool, refPending, minAge+time.Hour)

	gw.setStatus(refBerjaya, "succeeded")
	gw.setStatus(refGagal, "failed")
	gw.setStatus(refPending, "pending")

	summary := r.RunOnce(ctx)

	if got := regPaymentStatus(t, ctx, pool, idBerjaya); got != "succeeded" {
		t.Errorf("gateway kata succeeded tapi DB = %q — ahli yang DAH bayar "+
			"kekal tak diluluskan", got)
	}
	if got := regPaymentStatus(t, ctx, pool, idGagal); got != "failed" {
		t.Errorf("gateway kata failed tapi DB = %q", got)
	}
	if got := regPaymentStatus(t, ctx, pool, idPending); got != "pending" {
		t.Errorf("gateway kata pending tapi DB berubah jadi %q", got)
	}
	if summary.MismatchesFixed != 2 {
		t.Errorf("MismatchesFixed = %d, mahu 2 (succeeded + failed)", summary.MismatchesFixed)
	}
	if summary.Errors != 0 {
		t.Errorf("Errors = %d, mahu 0", summary.Errors)
	}
}

// Baris yang BELUM cukup umur (< minAge) tak boleh disentuh — pembayar
// mungkin masih di halaman bank.
func TestReconcileLangkauBarisTerlaluBaharu(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "BILL-baru-" + uuid.NewString()[:8]
	id := seedRegistrationPayment(t, ctx, pool, ref, minAge-5*time.Minute)
	gw.setStatus(ref, "succeeded")

	r.RunOnce(ctx)

	if got := regPaymentStatus(t, ctx, pool, id); got != "pending" {
		t.Errorf("baris berumur < minAge (%v) disemak dan diubah jadi %q", minAge, got)
	}
	if n := gw.callCount(ref); n != 0 {
		t.Errorf("gateway dipanggil %d kali untuk baris terlalu baharu", n)
	}
}

// umurPurba — umur MUTLAK, sengaja BUKAN diterbitkan daripada `maxAge`.
//
// Versi pertama ujian ni menyemai pada `maxAge + 24h` dan gagal
// menangkap apa-apa: menaikkan `maxAge` turut menaikkan umur benih, jadi
// baris itu kekal di luar tingkap tak kira apa nilai pemalarnya —
// penegasan yang tak boleh gagal. Disahkan melalui ujian mutasi
// 2026-08-22.
//
// Angka mutlak bermakna melonggarkan `maxAge` melebihi 30 hari
// menjadikan ujian ni gagal, yang memang niatnya.
const umurPurba = 30 * 24 * time.Hour

// L30 — tingkap ATAS. Baris yang lebih tua drpd maxAge mesti berhenti
// dipoll SEPENUHNYA, kalau tidak bebanan reconcile membesar secara
// monotonik sepanjang hayat sistem.
func TestReconcileBerhentiPollBarisLebihTuaDaripadaMaxAge(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	if maxAge >= umurPurba {
		t.Fatalf("maxAge (%v) dah mencapai umur benih ujian (%v) — ujian ni "+
			"tak lagi membuktikan apa-apa; naikkan umurPurba secara sedar "+
			"kalau tingkap memang sepatutnya seluas itu", maxAge, umurPurba)
	}

	ref := "BILL-purba-" + uuid.NewString()[:8]
	id := seedRegistrationPayment(t, ctx, pool, ref, umurPurba)
	gw.setStatus(ref, "succeeded")

	r.RunOnce(ctx)

	if n := gw.callCount(ref); n != 0 {
		t.Errorf("gateway dipanggil %d kali untuk baris berumur %v (maxAge = %v) — "+
			"setiap checkout terbiar sejak hari pertama akan dipoll "+
			"selama-lamanya", n, umurPurba, maxAge)
	}
	// Baris itu TIDAK hilang — ia cuma berhenti dipoll.
	if got := regPaymentStatus(t, ctx, pool, id); got != "pending" {
		t.Errorf("baris purba diubah jadi %q — ia sepatutnya dibiar utuh", got)
	}
}

// L29 — baris tanpa `gateway_ref` bermakna createBill tak pernah
// berjaya, jadi TIADA bil untuk ditanya pada gateway. Ia mesti dilangkau
// sepenuhnya: memanggil CheckStatus dengan ref kosong akan mengembalikan
// jawapan yang tak bermakna, dan lebih teruk lagi ia membakar kuota API
// gateway pada baris yang takkan pernah diselesaikan.
func TestReconcileLangkauBarisTanpaGatewayRef(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	userID := seedUser(t, ctx, pool)
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into registration_payments
		   (user_id, amount_cents, currency, gateway, status, created_at)
		 values ($1, 1000, 'myr', 'toyyibpay', 'pending', now() - $2::interval)
		 returning id`,
		userID, (minAge + time.Hour).String()).Scan(&id); err != nil {
		t.Fatalf("seed registration_payment tanpa ref: %v", err)
	}

	r.RunOnce(ctx)

	// Penegasan dibuat pada REF, bukan pada `summary.Checked`: ringkasan
	// mengira merentas ketiga-tiga modul dan ujian dalam pakej ni
	// berkongsi satu DB, jadi ia membawa baris daripada ujian lain.
	// `callCount("")` khusus kepada baris ni — CheckStatus hanya boleh
	// dipanggil dengan ref kosong kalau baris tanpa bil terlepas tapisan.
	if n := gw.callCount(""); n != 0 {
		t.Errorf("gateway dipanggil %d kali dengan ref KOSONG — baris yang "+
			"createBill-nya tak pernah berjaya sedang dipoll; ia takkan "+
			"pernah diselesaikan dan cuma membakar kuota API", n)
	}
	if got := regPaymentStatus(t, ctx, pool, id); got != "pending" {
		t.Errorf("baris tanpa ref diubah jadi %q", got)
	}
}

// Ralat gateway dikira dalam ringkasan dan TIDAK mengubah DB — jawapan
// yang tak diketahui bukan alasan menulis apa-apa.
func TestReconcileRalatGatewayTidakUbahDB(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "BILL-err-" + uuid.NewString()[:8]
	id := seedRegistrationPayment(t, ctx, pool, ref, minAge+time.Hour)
	gw.setErr(ref, errors.New("gateway tak dapat dihubungi"))

	summary := r.RunOnce(ctx)

	if got := regPaymentStatus(t, ctx, pool, id); got != "pending" {
		t.Errorf("status ditulis (%q) walaupun CheckStatus gagal", got)
	}
	if summary.Errors == 0 {
		t.Error("Errors = 0 walaupun CheckStatus gagal — kegagalan gateway " +
			"jadi tak kelihatan dalam ringkasan pencetus manual")
	}
	if summary.MismatchesFixed != 0 {
		t.Errorf("MismatchesFixed = %d walaupun tiada apa dibetulkan", summary.MismatchesFixed)
	}
}

// ---- Yuran aktiviti ----

func seedActivityRegistration(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, ref string, age time.Duration, status string,
) uuid.UUID {
	t.Helper()
	userID := seedUser(t, ctx, pool)

	var activityID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activities (category_id, title, description, location_name,
		   location_address, starts_at, ends_at, registration_closes_at,
		   capacity, fee_cents, attendance_threshold_pct, status)
		 values ((select id from activity_categories limit 1), 'Ujian reconcile', '', 'Dewan',
		   '', now() + interval '10 days', now() + interval '10 days' + interval '2 hours',
		   now() + interval '9 days', null, 1000, 100, 'published')
		 returning id`).Scan(&activityID); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	var regID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into activity_registrations
		   (activity_id, user_id, status, payment_status, payment_ref, checkin_token, registered_at)
		 values ($1, $2, $3, 'pending', $4, $5, now() - $6::interval)
		 returning id`,
		activityID, userID, status, ref, uuid.NewString(), age.String()).Scan(&regID); err != nil {
		t.Fatalf("seed activity_registration: %v", err)
	}
	return regID
}

func activityPaymentStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx,
		`select payment_status from activity_registrations where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("baca payment_status: %v", err)
	}
	return s
}

func TestReconcileTandaYuranAktivitiSebagaiPaid(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "ACT-ok-" + uuid.NewString()[:8]
	regID := seedActivityRegistration(t, ctx, pool, ref, minAge+time.Hour, "registered")
	gw.setStatus(ref, "succeeded")

	r.RunOnce(ctx)

	if got := activityPaymentStatus(t, ctx, pool, regID); got != "paid" {
		t.Errorf("payment_status = %q, mahu \"paid\" — ahli yang dah bayar "+
			"takkan layak dapat sijil", got)
	}
}

// `activity_registrations.payment_status` CHECK tiada 'failed'. Menulis
// "failed" ke sana akan melanggar kekangan — jadi gateway yang kata gagal
// mesti meninggalkan baris pada 'pending' (untuk activitysweep bersihkan)
// dan bukan cuba menulis.
func TestReconcileTidakTulisFailedKeYuranAktiviti(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "ACT-fail-" + uuid.NewString()[:8]
	regID := seedActivityRegistration(t, ctx, pool, ref, minAge+time.Hour, "registered")
	gw.setStatus(ref, "failed")

	summary := r.RunOnce(ctx)

	if got := activityPaymentStatus(t, ctx, pool, regID); got != "pending" {
		t.Errorf("payment_status = %q — 'failed' bukan nilai sah bagi lajur ni, "+
			"CHECK constraint akan ditolak", got)
	}
	if summary.Errors != 0 {
		t.Errorf("Errors = %d — gateway 'failed' ialah keputusan yang DIJANGKA "+
			"di sini, bukan kegagalan", summary.Errors)
	}
}

// L30 — pendaftaran yang SUDAH dibatalkan sapuan mengekalkan
// `payment_status='pending'` dengan sengaja, jadi tanpa guard
// `status <> 'cancelled'` ia dipoll selama-lamanya walaupun sudah mati.
func TestReconcileLangkauPendaftaranYangSudahDibatalkan(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "ACT-cancel-" + uuid.NewString()[:8]
	seedActivityRegistration(t, ctx, pool, ref, minAge+time.Hour, "cancelled")
	gw.setStatus(ref, "succeeded")

	r.RunOnce(ctx)

	if n := gw.callCount(ref); n != 0 {
		t.Errorf("gateway dipanggil %d kali untuk pendaftaran DIBATALKAN — "+
			"baris mati kekal dipoll selamanya", n)
	}
}

// ---- Derma ----

func TestReconcileBetulkanDerma(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "pi_" + uuid.NewString()[:12]
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into donations
		   (donor_name, donor_email, amount_cents, currency, gateway, gateway_ref, status, created_at)
		 values ('Penderma', 'derma@test.local', 5000, 'myr', 'stripe', $1, 'pending', now() - $2::interval)
		 returning id`,
		ref, (minAge + time.Hour).String()).Scan(&id); err != nil {
		t.Fatalf("seed donation: %v", err)
	}
	gw.setStatus(ref, "succeeded")

	r.RunOnce(ctx)

	var status string
	if err := pool.QueryRow(ctx,
		`select status from donations where id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("baca status: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("status derma = %q, mahu \"succeeded\"", status)
	}
}

// ---- Sifat merentas modul ----

// Reconcile mesti selamat dijalankan berulang — pencetus manual
// (POST /admin/payments/reconcile) berkongsi laluan yang SAMA dengan
// sapuan berjadual, jadi pengurus yang menekan butang dua kali tak boleh
// menghasilkan keputusan berbeza.
func TestRunOnceIdempoten(t *testing.T) {
	pool, _, gw, r, ctx := setup(t)

	ref := "BILL-idem-" + uuid.NewString()[:8]
	id := seedRegistrationPayment(t, ctx, pool, ref, minAge+time.Hour)
	gw.setStatus(ref, "succeeded")

	pertama := r.RunOnce(ctx)
	kedua := r.RunOnce(ctx)

	if got := regPaymentStatus(t, ctx, pool, id); got != "succeeded" {
		t.Errorf("status = %q selepas dua pusingan", got)
	}
	if pertama.MismatchesFixed != 1 {
		t.Errorf("pusingan pertama MismatchesFixed = %d, mahu 1", pertama.MismatchesFixed)
	}
	// Pusingan kedua tak patut mengira apa-apa: baris dah 'succeeded',
	// jadi ia tak lagi padan `status = 'pending'`.
	if kedua.MismatchesFixed != 0 {
		t.Errorf("pusingan kedua MismatchesFixed = %d, mahu 0 — pembetulan "+
			"dikira dua kali", kedua.MismatchesFixed)
	}
}

// Gateway yang tak berdaftar mesti dilangkau dengan ralat yang dikira,
// bukan panik nil-pointer dalam goroutine latar (yang bermakna proses
// mati, bukan satu sapuan gagal).
func TestReconcileGatewayTidakBerdaftarDilangkauDenganSelamat(t *testing.T) {
	pool, q, _, _, ctx := setup(t)

	ref := "BILL-nogw-" + uuid.NewString()[:8]
	seedRegistrationPayment(t, ctx, pool, ref, minAge+time.Hour)

	// Registry KOSONG — "toyyibpay" tiada.
	kosong := New(q, map[string]payment.Gateway{}, time.Hour)

	summary := kosong.RunOnce(ctx)

	if summary.Errors == 0 {
		t.Error("Errors = 0 walaupun gateway tak berdaftar — salah konfigurasi " +
			"jadi tak kelihatan")
	}
	if summary.MismatchesFixed != 0 {
		t.Errorf("MismatchesFixed = %d tanpa gateway", summary.MismatchesFixed)
	}
}
