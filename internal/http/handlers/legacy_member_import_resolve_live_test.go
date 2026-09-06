package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const legacyCSVHeader = `Bil.,Status,Kategori,Jawatan Kelab,Kod Kelab,No. ID.,/,Tahun Daftar,-,Bil. Ahli,No. Ahli,Nama,Telefon,Emel,Bahagian,Alamat,Jawatan,Nama Waris,TelefonWaris,Kesihatan,Saiz Baju,Jenis Baju,Lengan,Catatan`

// legacyCSVRow membina satu baris CSV yang SAH pada semua medan lain,
// supaya setiap ujian mengasingkan satu jenis konflik sahaja.
func legacyCSVRow(bil int, staffID, memberID, email, department, year string) string {
	return fmt.Sprintf(
		"%d,Aktif,A,Ahli,MARC,%s,/,%s,-,%d,%s,Ahli %d,0123456789,%s,%s,Alamat,Pegawai,Waris,0198887777,Tiada,M,T,Pendek,",
		bil, staffID, year, bil, memberID, bil, email, department)
}

func seedBKP(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`insert into departments (code, name) values ('BKP', 'Bahagian Ujian')
		 on conflict (code) do nothing`); err != nil {
		t.Fatalf("seed department: %v", err)
	}
}

func legacyRowsOf(t *testing.T, h *LegacyMemberImportHandler, actor uuid.UUID, batchID string) []map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/legacy-member-import/"+batchID, nil)
	c.Params = gin.Params{{Key: "id", Value: batchID}}
	c.Set("userID", actor)
	h.GetBatch(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetBatch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return payload.Rows
}

func conflictCodes(row map[string]any) []string {
	raw, _ := row["conflicts"].([]any)
	codes := make([]string, 0, len(raw))
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			codes = append(codes, fmt.Sprint(entry["code"]))
		}
	}
	return codes
}

func hasCode(row map[string]any, code string) bool {
	for _, got := range conflictCodes(row) {
		if got == code {
			return true
		}
	}
	return false
}

func callResolveDepartment(t *testing.T, h *LegacyMemberImportHandler, actor uuid.UUID, batchID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/admin/legacy-member-import/"+batchID+"/resolve-department", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: batchID}}
	c.Set("userID", actor)
	h.ResolveDepartment(c)
	return rec
}

func callUpdateRow(t *testing.T, h *LegacyMemberImportHandler, actor uuid.UUID, rowID, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch,
		"/admin/legacy-member-import/rows/"+rowID, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: rowID}}
	c.Set("userID", actor)
	h.UpdateRow(c)
	return rec
}

// Kod bahagian dengan '/' TAK boleh dijadikan kod bahagian (routing
// PATCH/DELETE /admin/departments/:code pecah), jadi superadmin memberi
// kod bersih - dan baris CSV mesti ditulis semula kepada kod itu, kalau
// tidak ia kekal tak padan walaupun bahagian sudah wujud.
func TestResolveDepartmentTulisSemulaBarisDanJadiValid(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	seedBKP(t, ctx, pool)
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	csv := legacyCSVHeader + "\n" +
		legacyCSVRow(1, "S-DEPT-1", "M-DEPT-1", "dept1@test.local", "PEJ. SUM/KPE", "2020") + "\n"
	batchID := runLegacyDryRun(t, h, superadmin, csv)

	before := legacyRowsOf(t, h, superadmin, batchID)
	if !hasCode(before[0], "unknown_department") {
		t.Fatalf("prasyarat gagal: mahu unknown_department, dapat %v", conflictCodes(before[0]))
	}

	rec := callResolveDepartment(t, h, superadmin, batchID,
		`{"from":"PEJ. SUM/KPE","code":"PEJ-SUM-KPE","name":"Pejabat Sumber / KPE"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	after := legacyRowsOf(t, h, superadmin, batchID)
	if hasCode(after[0], "unknown_department") {
		t.Errorf("konflik bahagian masih ada: %v", conflictCodes(after[0]))
	}
	if got := after[0]["department_code"]; got != "PEJ-SUM-KPE" {
		t.Errorf("department_code = %v, mahu PEJ-SUM-KPE", got)
	}
	// Import memilih `where status = 'valid'` - tanpa status dikemas
	// kini, baris ini akan dilangkau senyap walaupun konflik hilang.
	if got := after[0]["status"]; got != "valid" {
		t.Errorf("status = %v, mahu valid", got)
	}
}

// Membetulkan SATU daripada dua baris pendua mesti membersihkan
// KEDUA-DUANYA - konflik pendua ialah sifat pasangan, bukan sifat satu
// baris.
func TestUpdateRowMembersihkanPasanganPendua(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	seedBKP(t, ctx, pool)
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	csv := legacyCSVHeader + "\n" +
		legacyCSVRow(1, "SAMA-1", "M-DUP-1", "dup1@test.local", "BKP", "2020") + "\n" +
		legacyCSVRow(2, "SAMA-1", "M-DUP-2", "dup2@test.local", "BKP", "2020") + "\n"
	batchID := runLegacyDryRun(t, h, superadmin, csv)

	before := legacyRowsOf(t, h, superadmin, batchID)
	if len(before) != 2 {
		t.Fatalf("mahu 2 baris, dapat %d", len(before))
	}
	for i, row := range before {
		if !hasCode(row, "duplicate_staff_id") {
			t.Fatalf("prasyarat gagal pada baris %d: %v", i, conflictCodes(row))
		}
	}

	rowID := fmt.Sprint(before[1]["id"])
	rec := callUpdateRow(t, h, superadmin, rowID, `{"legacy_staff_id":"SAMA-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	after := legacyRowsOf(t, h, superadmin, batchID)
	for i, row := range after {
		if hasCode(row, "duplicate_staff_id") {
			t.Errorf("baris %d masih pendua: %v", i, conflictCodes(row))
		}
		if got := row["status"]; got != "valid" {
			t.Errorf("baris %d status = %v, mahu valid", i, got)
		}
	}
}

func TestUpdateRowMembetulkanPlaceholderStaffID(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	seedBKP(t, ctx, pool)
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	csv := legacyCSVHeader + "\n" +
		legacyCSVRow(1, "XXXX", "M-PH-1", "ph1@test.local", "BKP", "2020") + "\n"
	batchID := runLegacyDryRun(t, h, superadmin, csv)

	before := legacyRowsOf(t, h, superadmin, batchID)
	if !hasCode(before[0], "placeholder_staff_id") {
		t.Fatalf("prasyarat gagal: %v", conflictCodes(before[0]))
	}

	rec := callUpdateRow(t, h, superadmin, fmt.Sprint(before[0]["id"]), `{"legacy_staff_id":"S-REAL-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	after := legacyRowsOf(t, h, superadmin, batchID)
	if len(conflictCodes(after[0])) != 0 {
		t.Errorf("masih berkonflik: %v", conflictCodes(after[0]))
	}
	if got := after[0]["legacy_staff_id"]; got != "S-REAL-1" {
		t.Errorf("legacy_staff_id = %v, mahu S-REAL-1", got)
	}
}

// Tahun daftar TIDAK disimpan dalam legacy_member_import_rows, jadi
// pengiraan semula TAK BOLEH menghasilkannya. Ia mesti DIKEKALKAN, bukan
// digugurkan senyap - kalau tidak baris rosak akan jadi 'valid' dan
// diimport.
func TestRevalidateMengekalkanKonflikYangTakBolehDikira(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	seedBKP(t, ctx, pool)
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	csv := legacyCSVHeader + "\n" +
		legacyCSVRow(1, "XXXX", "M-YR-1", "yr1@test.local", "BKP", "tahun-rosak") + "\n"
	batchID := runLegacyDryRun(t, h, superadmin, csv)

	before := legacyRowsOf(t, h, superadmin, batchID)
	if !hasCode(before[0], "invalid_registration_year") {
		t.Fatalf("prasyarat gagal: %v", conflictCodes(before[0]))
	}

	// Betulkan placeholder sahaja; konflik tahun mesti kekal.
	rec := callUpdateRow(t, h, superadmin, fmt.Sprint(before[0]["id"]), `{"legacy_staff_id":"S-YR-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	after := legacyRowsOf(t, h, superadmin, batchID)
	if hasCode(after[0], "placeholder_staff_id") {
		t.Errorf("placeholder sepatutnya hilang: %v", conflictCodes(after[0]))
	}
	if !hasCode(after[0], "invalid_registration_year") {
		t.Errorf("konflik tahun HILANG selepas revalidate: %v", conflictCodes(after[0]))
	}
	if got := after[0]["status"]; got != "conflict" {
		t.Errorf("status = %v, mahu conflict", got)
	}
}

func TestShortcutImportDitolakUntukBukanSuperadmin(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	manager := seedMember(t, ctx, pool, "manager", "approved")
	seedBKP(t, ctx, pool)
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	csv := legacyCSVHeader + "\n" +
		legacyCSVRow(1, "S-AUTH-1", "M-AUTH-1", "auth1@test.local", "BKP", "2020") + "\n"
	batchID := runLegacyDryRun(t, h, superadmin, csv)
	rowID := fmt.Sprint(legacyRowsOf(t, h, superadmin, batchID)[0]["id"])

	if rec := callUpdateRow(t, h, manager, rowID, `{"legacy_staff_id":"CUBA"}`); rec.Code != http.StatusForbidden {
		t.Errorf("UpdateRow status = %d, mahu 403", rec.Code)
	}
	if rec := callResolveDepartment(t, h, manager, batchID,
		`{"from":"BKP","code":"BKP2","name":"Cuba"}`); rec.Code != http.StatusForbidden {
		t.Errorf("ResolveDepartment status = %d, mahu 403", rec.Code)
	}
}
