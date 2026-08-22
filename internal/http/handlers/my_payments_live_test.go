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

	"marc/internal/storage"
)

// L33 — `GET /me/payments` kini memulangkan derma.
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

	NewPaymentsHandler(pool, storage.NewR2Client("", "", "", "", "")).Mine(c)

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
		t.Fatal("respons TIADA kunci \"donations\" — endpoint resit derma " +
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
	// Percubaan gagal MESTI turut muncul — sejarah patut menunjukkan
	// percubaan, bukan senyap menghilangkannya (padanan
	// ListMyRegistrationPayments).
	gagal := seedDonation(t, pool, &userID, "failed", 2000)

	got := donationIDs(t, minePayments(t, pool, userID))

	found := map[string]bool{}
	for _, id := range got {
		found[id] = true
	}
	if !found[berjaya.String()] {
		t.Error("derma 'succeeded' tiada dalam senarai — resitnya tak boleh dicapai")
	}
	if !found[gagal.String()] {
		t.Error("derma 'failed' tiada dalam senarai — sejarah menyembunyikan percubaan")
	}
}

// Pengasingan: senarai diskop `user_id`, jadi derma ahli LAIN tak boleh
// bocor. Kalau ia bocor, ahli boleh memanggil endpoint resit dengan id
// itu — dan resit membawa nama + emel penderma.
func TestMinePaymentsTidakBocorkanDermaAhliLain(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	saya := seedMember(t, ctx, pool, "ahli", "approved")
	oranglain := seedMember(t, ctx, pool, "ahli", "approved")
	dermaOrangLain := seedDonation(t, pool, &oranglain, "succeeded", 9900)

	for _, id := range donationIDs(t, minePayments(t, pool, saya)) {
		if id == dermaOrangLain.String() {
			t.Fatal("derma ahli LAIN muncul dalam /me/payments — pemanggil boleh " +
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

// Ahli tanpa derma dapat array KOSONG, bukan `null` — klien memanggil
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
		t.Fatal("\"donations\" ialah null, bukan [] — klien yang memanggil .map " +
			"atasnya akan terhempas")
	}
	if list := raw.([]any); len(list) != 0 {
		t.Fatalf("mahu senarai kosong, dapat %d entri", len(list))
	}
}

// Dua senarai sedia ada mesti kekal — L33 menambah, bukan mengganti.
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
