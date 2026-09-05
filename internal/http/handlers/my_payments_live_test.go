package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// L33 - `GET /me/payments` kini memulangkan derma.
//
// Sebelum ni ia memulangkan dua senarai sahaja, dan
// `GET /me/payments/donation/:id/receipt` mati secara praktikal: endpoint
// resit itu perlukan `donations.id`, dan tiada permukaan API yang pernah
// mendedahkan id itu kepada pemiliknya.

func seedDonation(
	t *testing.T, pool *pgxpool.Pool, userID *uuid.UUID, status string, amountCents int,
) uuid.UUID {
	t.Helper()
	var owner any
	if userID != nil {
		owner = *userID
	}
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		insert into donations (user_id, donor_name, donor_email, amount_cents,
		  currency, gateway, gateway_ref, status)
		values ($1, 'Penderma', 'derma@test.local', $2, 'myr', 'stripe', $3, $4)
		returning id`,
		owner, amountCents, "pi_"+uuid.NewString()[:12], status).Scan(&id); err != nil {
		t.Fatalf("seed donation: %v", err)
	}
	return id
}

func minePayments(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/me/payments", nil)
	c.Set("userID", userID)

	NewPaymentsHandler(pool, 100).Mine(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("nyahsiri respons: %v", err)
	}
	return body
}

func donationIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["donations"]
	if !ok {
		t.Fatal("respons TIADA kunci \"donations\" - endpoint resit derma " +
			"kekal tak boleh dicapai (L33)")
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("\"donations\" bukan array: %T", raw)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.(map[string]any)["id"].(string))
	}
	return out
}

func TestMinePayementsMemulangkanDermaSendiri(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	berjaya := seedDonation(t, pool, &userID, "succeeded", 5000)
	// Percubaan gagal MESTI turut muncul - sejarah patut menunjukkan
	// percubaan, bukan senyap menghilangkannya (padanan
	// ListMyRegistrationPayments).
	gagal := seedDonation(t, pool, &userID, "failed", 2000)

	got := donationIDs(t, minePayments(t, pool, userID))

	found := map[string]bool{}
	for _, id := range got {
		found[id] = true
	}
	if !found[berjaya.String()] {
		t.Error("derma 'succeeded' tiada dalam senarai - resitnya tak boleh dicapai")
	}
	if !found[gagal.String()] {
		t.Error("derma 'failed' tiada dalam senarai - sejarah menyembunyikan percubaan")
	}
}

// Pengasingan: senarai diskop `user_id`, jadi derma ahli LAIN tak boleh
// bocor. Kalau ia bocor, ahli boleh memanggil endpoint resit dengan id
// itu - dan resit membawa nama + emel penderma.
func TestMinePaymentsTidakBocorkanDermaAhliLain(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	saya := seedMember(t, ctx, pool, "ahli", "approved")
	oranglain := seedMember(t, ctx, pool, "ahli", "approved")
	dermaOrangLain := seedDonation(t, pool, &oranglain, "succeeded", 9900)

	for _, id := range donationIDs(t, minePayments(t, pool, saya)) {
		if id == dermaOrangLain.String() {
			t.Fatal("derma ahli LAIN muncul dalam /me/payments - pemanggil boleh " +
				"muat turun resit yang membawa nama dan emel penderma itu")
		}
	}
}

// Derma TANPA NAMA (`user_id` null) tak boleh muncul untuk sesiapa.
// Penderma itu tiada akaun untuk menuntutnya; emel resit semasa webhook
// ialah satu-satunya jejak mereka ada, mengikut reka bentuk.
func TestMinePaymentsTidakSertakanDermaTanpaNama(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	tanpaNama := seedDonation(t, pool, nil, "succeeded", 1500)

	for _, id := range donationIDs(t, minePayments(t, pool, userID)) {
		if id == tanpaNama.String() {
			t.Fatal("derma TANPA NAMA muncul dalam /me/payments seseorang")
		}
	}
}

// Ahli tanpa derma dapat array KOSONG, bukan `null` - klien memanggil
// `.map` atasnya (corak sama yang `coalesce(..., '{}')` lindungi dalam
// query kehadiran).
func TestMinePaymentsDermaKosongIalahArrayBukanNull(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	body := minePayments(t, pool, userID)

	raw, ok := body["donations"]
	if !ok {
		t.Fatal("kunci \"donations\" tiada")
	}
	if raw == nil {
		t.Fatal("\"donations\" ialah null, bukan [] - klien yang memanggil .map " +
			"atasnya akan terhempas")
	}
	if list := raw.([]any); len(list) != 0 {
		t.Fatalf("mahu senarai kosong, dapat %d entri", len(list))
	}
}

// Dua senarai sedia ada mesti kekal - L33 menambah, bukan mengganti.
func TestMinePaymentsMengekalkanDuaSenaraiSediaAda(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedMember(t, ctx, pool, "ahli", "approved")
	body := minePayments(t, pool, userID)

	for _, kunci := range []string{"registration_fee", "activity_fees", "donations"} {
		if _, ok := body[kunci]; !ok {
			t.Errorf("respons kehilangan kunci %q", kunci)
		}
	}
}

// Task 9 (staff-id verification plan) - `outstanding_registration_fee`
// pada GET /me/payments. Guna statusTestPool (bukan activityTestPool)
// dan createTestPendingProfile/createTestApprovedProfile
// (staff_id_query_live_test.go) sebab helper-helper itu seed staff_id
// secara eksplisit - seedMember (profile_status_live_test.go) tak isi
// staff_id dan akan gagal not-null constraint pada DB yang dah
// dimigrasi penuh.

func mePaymentsOutstandingFee(t *testing.T, body map[string]any) bool {
	t.Helper()
	raw, ok := body["outstanding_registration_fee"]
	if !ok {
		t.Fatal("respons TIADA kunci \"outstanding_registration_fee\"")
	}
	v, ok := raw.(bool)
	if !ok {
		t.Fatalf("\"outstanding_registration_fee\" bukan bool: %T", raw)
	}
	return v
}

// Baris lama PRA-MIGRASI (staff_id = user_id::text, placeholder yang
// migrasi isi) belum berada atas laluan staff langsung - mereka daftar
// bawah rejim yuran lama, jadi mereka MEMANG boleh terhutang.
func TestMinePaymentsOutstandingFeeTrueUntukBarisPlaceholderBelumBayar(t *testing.T) {
	pool, ctx := statusTestPool(t)

	var member uuid.UUID
	emailAddr := "outstanding-placeholder-" + uuid.NewString() + "@test.local"
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		emailAddr).Scan(&member); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, staff_id, role_id, status)
		 values ($1, $2, (select id from roles where key = 'ahli'), 'pending')`,
		member, member.String()); err != nil {
		t.Fatalf("seed profil placeholder: %v", err)
	}

	body := minePayments(t, pool, member)
	if !mePaymentsOutstandingFee(t, body) {
		t.Fatal("mahu true untuk baris placeholder yang belum disahkan & belum bayar")
	}
}

// Ahli pending BAHARU (staff_id sebenar diisi semasa daftar) belum
// disahkan, tapi satu-satunya jalan dia boleh diluluskan ialah melalui
// pengesahan staff - yang mengecualikan dia. Jadi dia TAK PERNAH
// terhutang, dan banner "bayar yuran" tak patut muncul (Opus verify
// 2026-09-03; v1 pulangkan true di sini untuk hampir SEMUA ahli baharu).
func TestMinePaymentsOutstandingFeeFalseUntukAhliPendingBerstaffID(t *testing.T) {
	pool, ctx := statusTestPool(t)
	member := createTestPendingProfile(t, ctx, pool)

	body := minePayments(t, pool, member)
	if mePaymentsOutstandingFee(t, body) {
		t.Fatal("mahu false untuk ahli pending yang dah ada nombor staff sebenar")
	}
}

func TestMinePaymentsOutstandingFeeFalseSelepasDisahkanStaff(t *testing.T) {
	pool, ctx := statusTestPool(t)
	manager := createTestApprovedProfile(t, ctx, pool, "manager")
	member := createTestPendingProfile(t, ctx, pool)
	verifyStaffIDDirect(t, ctx, pool, member, manager)

	body := minePayments(t, pool, member)
	if mePaymentsOutstandingFee(t, body) {
		t.Fatal("mahu false selepas staff ID disahkan (pengecualian automatik, Task 8)")
	}
}
