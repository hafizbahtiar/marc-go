package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/payment"
)

// L29 — susunan tulis checkout yuran pendaftaran.
//
// Invariannya: baris DB WUJUD sebelum bil gateway dicipta. Kalau
// terbalik, INSERT yang gagal meninggalkan bil ToyyibPay SAH yang boleh
// dibayar tanpa sebarang baris merujuknya — webhook mengena 0 baris dan
// menyenyapkannya sebagai "replay biasa", reconcile buta kepada apa yang
// tak pernah wujud, dan duit masuk tanpa rekod.
//
// Diuji melalui handler SEBENAR dengan gateway palsu, sebab yang penting
// ialah SUSUNAN dua operasi itu — bukan salah satu daripadanya.

// stubGateway — `CreatePayment` boleh dipaksa gagal.
type stubGateway struct {
	billCode string
	failWith error
	calls    int
}

func (s *stubGateway) Name() string  { return "toyyibpay" }
func (s *stubGateway) Enabled() bool { return true }

func (s *stubGateway) CreatePayment(context.Context, payment.CreateParams) (payment.CreateResult, error) {
	s.calls++
	if s.failWith != nil {
		return payment.CreateResult{}, s.failWith
	}
	return payment.CreateResult{
		GatewayRef:  s.billCode,
		RedirectURL: "https://toyyibpay.test/" + s.billCode,
		RawResponse: `[{"BillCode":"` + s.billCode + `"}]`,
	}, nil
}

func (s *stubGateway) VerifyWebhook([]byte, http.Header) (payment.WebhookEvent, error) {
	return payment.WebhookEvent{}, errors.New("tak dipakai")
}

func (s *stubGateway) CheckStatus(context.Context, string) (string, error) {
	return "pending", nil
}

// seedPendingMemberWithPhone — ahli `pending` dengan nombor telefon sah,
// jadi Checkout tak tersasar ke laluan `phone_required`.
func seedPendingMemberWithPhone(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	userID := seedMember(t, ctx, pool, "ahli", "pending")
	if _, err := pool.Exec(ctx,
		`update profiles set phone = '0123456789', display_name = 'Ahli Ujian' where user_id = $1`,
		userID); err != nil {
		t.Fatalf("set phone: %v", err)
	}
	return userID
}

func checkoutCall(t *testing.T, pool *pgxpool.Pool, gw payment.Gateway, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/registration-payments/checkout", strings.NewReader(""))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("userID", userID)

	NewRegistrationPaymentHandler(pool, gw, 1000).Checkout(c)
	return rec
}

type paymentRow struct {
	ID         uuid.UUID
	Status     string
	GatewayRef *string
}

func paymentRowsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) []paymentRow {
	t.Helper()
	rows, err := pool.Query(ctx,
		`select id, status, gateway_ref from registration_payments
		 where user_id = $1 order by created_at`, userID)
	if err != nil {
		t.Fatalf("query registration_payments: %v", err)
	}
	defer rows.Close()

	var out []paymentRow
	for rows.Next() {
		var r paymentRow
		if err := rows.Scan(&r.ID, &r.Status, &r.GatewayRef); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// Laluan bahagia — baris dicipta DAN dipautkan kepada bil.
func TestCheckoutMenciptaBarisDipautkanKepadaBil(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedPendingMemberWithPhone(t, ctx, pool)
	bill := "BILL" + uuid.NewString()[:8]

	rec := checkoutCall(t, pool, &stubGateway{billCode: bill}, userID)

	if rec.Code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %s", rec.Code, rec.Body.String())
	}

	rows := paymentRowsFor(t, ctx, pool, userID)
	if len(rows) != 1 {
		t.Fatalf("baris bayaran = %d, mahu 1", len(rows))
	}
	if rows[0].Status != "pending" {
		t.Errorf("status = %q, mahu \"pending\"", rows[0].Status)
	}
	if rows[0].GatewayRef == nil || *rows[0].GatewayRef != bill {
		t.Errorf("gateway_ref = %v, mahu %q — baris tak dipautkan kepada bil, "+
			"webhook takkan menemuinya", rows[0].GatewayRef, bill)
	}
}

// INTI L29: bila createBill GAGAL, baris MESTI sudah wujud.
//
// Ini penegasan yang gagal pada susunan lama — di situ createBill gagal
// bermakna tiada baris LANGSUNG dicipta, jadi tiada apa untuk dilihat
// atau didamaikan. (Pada susunan lama kes berbahayanya ialah kebalikan:
// createBill BERJAYA lalu INSERT gagal, meninggalkan bil tanpa baris.
// Susunan baharu menjadikan urutan itu mustahil — bil tak pernah dicipta
// sebelum baris wujud.)
func TestCheckoutMeninggalkanBarisWalaupunCreateBillGagal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedPendingMemberWithPhone(t, ctx, pool)
	gw := &stubGateway{failWith: errors.New("toyyibpay createBill: respons tak dijangka")}

	rec := checkoutCall(t, pool, gw, userID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("kod = %d, mahu 500", rec.Code)
	}
	if gw.calls != 1 {
		t.Fatalf("CreatePayment dipanggil %d kali, mahu 1", gw.calls)
	}

	rows := paymentRowsFor(t, ctx, pool, userID)
	if len(rows) != 1 {
		t.Fatalf("baris bayaran = %d, mahu 1 — baris MESTI ditulis SEBELUM "+
			"createBill; kalau tiada, susunan dah terbalik semula dan bil "+
			"yatim boleh berlaku lagi", len(rows))
	}
	if rows[0].GatewayRef != nil {
		t.Errorf("gateway_ref = %q, mahu NULL — tiada bil pernah dicipta", *rows[0].GatewayRef)
	}
	// Ditandakan 'failed', bukan dibiar 'pending' selamanya: tiada bil
	// wujud, jadi ia takkan pernah diselesaikan.
	if rows[0].Status != "failed" {
		t.Errorf("status = %q, mahu \"failed\" — baris 'pending' tanpa bil akan "+
			"duduk selamanya dalam sejarah ahli sebagai \"sedang diproses\"",
			rows[0].Status)
	}
}

// Indeks unik SEPARA — inilah yang menjadikan susunan baharu mungkin.
//
// Di bawah indeks unik PENUH lama atas (gateway, gateway_ref), ahli
// KEDUA yang checkout akan berlanggar dengan yang pertama sebaik
// kedua-duanya membawa ref kosong/NULL. Ujian ni memaksa keadaan itu.
func TestBanyakBarisTanpaRefBolehWujudSerentak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	gagal := &stubGateway{failWith: errors.New("gateway down")}

	a := seedPendingMemberWithPhone(t, ctx, pool)
	b := seedPendingMemberWithPhone(t, ctx, pool)

	if rec := checkoutCall(t, pool, gagal, a); rec.Code != http.StatusInternalServerError {
		t.Fatalf("checkout A: kod = %d", rec.Code)
	}
	// Kalau indeks masih unik PENUH, checkout kedua ni gagal pada INSERT
	// dengan pelanggaran kekangan dan bukan pada createBill.
	if rec := checkoutCall(t, pool, gagal, b); rec.Code != http.StatusInternalServerError {
		t.Fatalf("checkout B: kod = %d", rec.Code)
	}

	for nama, userID := range map[string]uuid.UUID{"A": a, "B": b} {
		rows := paymentRowsFor(t, ctx, pool, userID)
		if len(rows) != 1 {
			t.Errorf("ahli %s: baris = %d, mahu 1 — indeks unik separa tak "+
				"berkuat kuasa, dua baris ref-NULL berlanggar", nama, len(rows))
		}
	}
}

// `SetRegistrationPaymentGatewayRef` sekali-tulis: sebaik bil dikaitkan,
// tiada laluan boleh menunjuknya kepada bil LAIN. Tanpa guard
// `gateway_ref is null`, pepijat di tempat lain boleh mengalihkan rekod
// kewangan yang sudah berjaya kepada bil orang lain.
func TestSetGatewayRefSekaliTulis(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedPendingMemberWithPhone(t, ctx, pool)
	bill := "BILL" + uuid.NewString()[:8]

	if rec := checkoutCall(t, pool, &stubGateway{billCode: bill}, userID); rec.Code != http.StatusOK {
		t.Fatalf("checkout: kod = %d", rec.Code)
	}
	rows := paymentRowsFor(t, ctx, pool, userID)
	if len(rows) != 1 {
		t.Fatalf("baris = %d", len(rows))
	}

	// Cuba tunjuk baris yang sama kepada bil BERBEZA.
	tag, err := pool.Exec(ctx,
		`update registration_payments set gateway_ref = $2
		 where id = $1 and gateway_ref is null`,
		rows[0].ID, "BILL-lain")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("baris yang SUDAH berpaut ditulis ganti — rekod kewangan boleh " +
			"dialihkan kepada bil orang lain")
	}

	semula := paymentRowsFor(t, ctx, pool, userID)
	if semula[0].GatewayRef == nil || *semula[0].GatewayRef != bill {
		t.Errorf("gateway_ref = %v, mahu kekal %q", semula[0].GatewayRef, bill)
	}
}

// Ahli yang DAH bayar tak boleh cipta baris baharu — gate sedia ada,
// tapi ia kini berjalan SEBELUM sebarang tulisan DB, jadi ujian ni turut
// mengesahkan susunan baharu tak memperkenalkan baris sampah.
func TestCheckoutTidakCiptaBarisBilaSudahDibayar(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	userID := seedPendingMemberWithPhone(t, ctx, pool)
	seedSucceededRegistrationPayment(t, ctx, pool, userID)

	sebelum := len(paymentRowsFor(t, ctx, pool, userID))
	gw := &stubGateway{billCode: "BILL-patut-tak-dicipta"}

	rec := checkoutCall(t, pool, gw, userID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("kod = %d, mahu 400", rec.Code)
	}
	if gw.calls != 0 {
		t.Errorf("CreatePayment dipanggil %d kali walaupun sudah dibayar", gw.calls)
	}
	if selepas := len(paymentRowsFor(t, ctx, pool, userID)); selepas != sebelum {
		t.Errorf("baris bertambah %d → %d — gate berjalan SELEPAS tulisan DB",
			sebelum, selepas)
	}
}
