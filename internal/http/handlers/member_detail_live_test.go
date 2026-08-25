package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/email"
)

// callMemberDetail - padanan callMembers/callSetStatus (profile_status_live_test.go),
// tapi utk GetMemberDetail (satu ahli, bukan senarai/tindakan).
func callMemberDetail(t *testing.T, pool *pgxpool.Pool, callerID, targetID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &ProfileHandler{pool: pool, queries: sqlc.New(pool), emailClient: email.NewClient("", "")}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/members/"+targetID.String(), nil)
	c.Params = gin.Params{{Key: "id", Value: targetID.String()}}
	c.Set("userID", callerID)
	h.GetMemberDetail(c)
	return rec
}

// Ahli biasa yang tengok profil ahli lain (dlm skop visibleRankCeiling)
// cuma dapat Tier 1 - Tier 2/3 mesti null dlm JSON, bukan diabaikan/kosong.
func TestGetMemberDetailAhliBiasaTier1Sahaja(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	viewer := seedMember(t, ctx, pool, "ahli", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	rec := callMemberDetail(t, pool, viewer, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Tier 1 mesti hadir.
	if body["user_id"] != target.String() {
		t.Errorf("user_id = %v, mahu %s", body["user_id"], target)
	}
	if body["role_key"] != "ahli" {
		t.Errorf("role_key = %v, mahu ahli", body["role_key"])
	}

	// Tier 2/3 mesti null - BUKAN string kosong, BUKAN diabaikan drpd JSON.
	tier23Fields := []string{
		"email", "phone", "registration_payment_status",
		"emergency_contact_name", "emergency_contact_phone", "health_notes",
		"telegram_linked", "telegram_username", "addresses",
	}
	for _, f := range tier23Fields {
		v, present := body[f]
		if !present {
			t.Errorf("medan %q tak hadir langsung dlm JSON - patut hadir dgn nilai null", f)
			continue
		}
		if v != nil {
			t.Errorf("medan %q bocor kpd ahli biasa: %v", f, v)
		}
	}
}

// Manager (management, BUKAN superadmin) dapat Tier 1 + Tier 2, tapi
// Tier 3 tetap null - dua peringkat ni berasingan (keputusan produk
// eksplisit: admin BUKAN superadmin).
func TestGetMemberDetailManagerTier1Dan2(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	rec := callMemberDetail(t, pool, manager, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["email"] == nil {
		t.Error("manager patut nampak emel ahli")
	}
	// registration_payment_status boleh jadi null (ahli tak pernah cuba
	// bayar) - itu keadaan sah, bukan kegagalan tapisan. Cuma pastikan
	// medan tu HADIR (bukan hilang terus dari JSON).
	if _, present := body["registration_payment_status"]; !present {
		t.Error("registration_payment_status tak hadir dlm JSON utk manager")
	}

	tier3Fields := []string{
		"emergency_contact_name", "emergency_contact_phone", "health_notes",
		"telegram_linked", "telegram_username", "addresses",
	}
	for _, f := range tier3Fields {
		if v := body[f]; v != nil {
			t.Errorf("medan Tier 3 %q bocor kpd manager (bukan superadmin): %v", f, v)
		}
	}
}

// Superadmin dapat SEMUA tier, termasuk senarai alamat penuh.
func TestGetMemberDetailSuperadminSemuaTier(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)
	q := sqlc.New(pool)

	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	if _, err := pool.Exec(ctx,
		`update profiles set emergency_contact_name = 'Waris Ujian',
		 emergency_contact_phone = '+60123456789', health_notes = 'Asma'
		 where user_id = $1`, target); err != nil {
		t.Fatalf("seed tier3: %v", err)
	}
	if _, err := q.CreateAddress(ctx, sqlc.CreateAddressParams{
		UserID:      target,
		IsDefault:   true,
		AddressType: "landed",
		City:        "Petaling Jaya",
		Postcode:    "46000",
		State:       "Selangor",
	}); err != nil {
		t.Fatalf("seed alamat: %v", err)
	}

	rec := callMemberDetail(t, pool, superadmin, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["email"] == nil {
		t.Error("superadmin patut nampak emel")
	}
	if body["emergency_contact_name"] != "Waris Ujian" {
		t.Errorf("emergency_contact_name = %v", body["emergency_contact_name"])
	}
	if body["health_notes"] != "Asma" {
		t.Errorf("health_notes = %v", body["health_notes"])
	}
	addrs, ok := body["addresses"].([]any)
	if !ok {
		t.Fatalf("addresses bukan array: %v", body["addresses"])
	}
	if len(addrs) != 1 {
		t.Fatalf("mahu 1 alamat, dapat %d", len(addrs))
	}
}

// Viewer di atas siling keterlihatan (mis. ahli tengok superadmin) dapat
// 404 generik - BUKAN 403, elak dedah kewujudan baris rank tinggi. Body
// mesti kosong (bukan Tier 1 separa) sebelum 404 dipulangkan.
func TestGetMemberDetailAtasSilingDapat404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	viewer := seedMember(t, ctx, pool, "ahli", "approved")
	target := seedMember(t, ctx, pool, "superadmin", "approved")

	rec := callMemberDetail(t, pool, viewer, target)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, mahu 404 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := body["role_key"]; present {
		t.Error("respons 404 membocorkan medan profil target")
	}
}

// user_id yang tak wujud langsung mesti 404, bukan 500.
func TestGetMemberDetailTidakWujud404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	viewer := seedMember(t, ctx, pool, "manager", "approved")
	rec := callMemberDetail(t, pool, viewer, uuid.New())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, mahu 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Ahli biasa yang tengok profil ahli 'pending' (dlm skop visibleRankCeiling,
// tapi belum diluluskan) mesti dapat 404 generik - endpoint ni direktori
// ahli, bukan barisan kelulusan (padanan peraturan Members).
func TestGetMemberDetailAhliBiasaTargetPending404(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	viewer := seedMember(t, ctx, pool, "ahli", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")

	rec := callMemberDetail(t, pool, viewer, target)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, mahu 404 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := body["role_key"]; present {
		t.Error("respons 404 membocorkan medan profil target")
	}
}

// Management (mis. manager) tetap boleh lihat profil ahli 'pending' -
// keperluan barisan kelulusan Stage 11, jangan sampai kawalan baru ni
// tersekat kepada pengurusan sendiri.
func TestGetMemberDetailManagerTargetPendingOK(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	manager := seedMember(t, ctx, pool, "manager", "approved")
	target := seedMember(t, ctx, pool, "ahli", "pending")

	rec := callMemberDetail(t, pool, manager, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "pending" {
		t.Errorf("status = %v, mahu pending", body["status"])
	}
	if body["email"] == nil {
		t.Error("manager patut tetap nampak emel ahli 'pending' (barisan kelulusan)")
	}
}

// Supervisor (management? BUKAN - authz.CategoryManagement termasuk
// supervisor/manager/superadmin) - semak eksplisit supervisor turut dapat
// Tier 2 (kategori management), padanan formula caller.RoleCategory.
func TestGetMemberDetailSupervisorTier2(t *testing.T) {
	pool, ctx := statusTestPool(t)
	wipeMembers(t, ctx, pool)

	supervisor := seedMember(t, ctx, pool, "supervisor", "approved")
	target := seedMember(t, ctx, pool, "ahli", "approved")

	rec := callMemberDetail(t, pool, supervisor, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["email"] == nil {
		t.Error("supervisor (kategori management) patut nampak emel")
	}
	if body["health_notes"] != nil {
		t.Error("supervisor (bukan superadmin) tak patut nampak nota kesihatan")
	}
}
