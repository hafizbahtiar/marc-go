package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CSV minimum yang lulus semua semakan: satu baris ahli tanpa sebarang
// konflik dan tanpa warning. Baris "bersih" inilah yang mendedahkan bug
// null - slice Conflicts/Warnings kekal nil bila tiada apa-apa ditambah.
const legacyCleanCSV = `Bil.,Status,Kategori,Jawatan Kelab,Kod Kelab,No. ID.,/,Tahun Daftar,-,Bil. Ahli,No. Ahli,Nama,Telefon,Emel,Bahagian,Alamat,Jawatan,Nama Waris,TelefonWaris,Kesihatan,Saiz Baju,Jenis Baju,Lengan,Catatan
1,Aktif,A,Ahli,MARC,S-CLEAN-1,/,2020,-,1,M-CLEAN-1,Ali Bersih,0123456789,ali.bersih@test.local,BKP,Alamat 1,Pegawai,Waris A,0198887777,Tiada,M,T,Pendek,
`

// TestGetBatchSentiasaPulangArrayBukanNull mengunci kontrak API: baris
// import tanpa konflik/warning MESTI pulang `[]`, bukan `null`.
//
// Kenapa ini penting di luar kekemasan JSON: portal web memanggil
// `row.conflicts.length` terus (legacy-import-console.tsx). `null`
// meletupkan render SSR keseluruhan halaman dengan
// "Cannot read properties of null (reading 'length')" - dan fallback
// `rows ?? []` di halaman TAK menangkapnya sebab null itu satu lapisan
// lebih dalam, pada setiap baris.
func TestGetBatchSentiasaPulangArrayBukanNull(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")

	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	// Bahagian BKP mesti wujud supaya baris dikira 'valid'.
	if _, err := pool.Exec(ctx,
		`insert into departments (code, name) values ('BKP', 'Bahagian Ujian')
		 on conflict (code) do nothing`); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	batchID := runLegacyDryRun(t, h, superadmin, legacyCleanCSV)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/legacy-member-import/"+batchID, nil)
	c.Params = gin.Params{{Key: "id", Value: batchID}}
	c.Set("userID", superadmin)
	h.GetBatch(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Rows) != 1 {
		t.Fatalf("mahu 1 baris, dapat %d: %s", len(payload.Rows), rec.Body.String())
	}
	for _, field := range []string{"conflicts", "warnings"} {
		got := string(payload.Rows[0][field])
		if got != "[]" {
			t.Errorf("rows[0].%s = %s, mahu []", field, got)
		}
	}

	// Sumbernya: nilai yang DISIMPAN mesti array juga, bukan jsonb null.
	// Kalau hanya lapisan baca dinormalkan, `conflicts || ...` dalam
	// Import akan hasilkan [null, {...}] pada baris lama.
	var conflictsType, warningsType string
	if err := pool.QueryRow(ctx, `
		select jsonb_typeof(conflicts), jsonb_typeof(warnings)
		from legacy_member_import_rows where batch_id = $1`,
		uuid.MustParse(batchID)).Scan(&conflictsType, &warningsType); err != nil {
		t.Fatalf("query stored: %v", err)
	}
	if conflictsType != "array" || warningsType != "array" {
		t.Errorf("tersimpan sebagai conflicts=%s warnings=%s, mahu array/array", conflictsType, warningsType)
	}
}

func runLegacyDryRun(t *testing.T, h *LegacyMemberImportHandler, actor uuid.UUID, csv string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "legacy.csv")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		t.Fatalf("tulis csv: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tutup writer: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/legacy-member-import/dry-run", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set("userID", actor)
	h.DryRun(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("dry-run status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal dry-run: %v", err)
	}
	return created.ID
}

// TestGetBatchNormalkanBarisNullSediaAda - baris yang SUDAH tersimpan
// sebagai jsonb `null` sebelum pembetulan ini masih ada dalam DB
// staging/produksi. Lapisan baca mesti memulangkannya sebagai `[]`
// supaya halaman pulih tanpa migrasi data.
func TestGetBatchNormalkanBarisNullSediaAda(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	if _, err := pool.Exec(ctx,
		`insert into departments (code, name) values ('BKP', 'Bahagian Ujian')
		 on conflict (code) do nothing`); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	var batchID uuid.UUID
	if err := pool.QueryRow(ctx, `
		insert into legacy_member_import_batches
		  (source_filename, source_sha256, created_by, total_rows, valid_rows, conflict_rows)
		values ('lama.csv', $1, $2, 1, 1, 0) returning id`,
		uuid.NewString(), superadmin).Scan(&batchID); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	// Tiru dengan TEPAT apa yang kod lama tulis: jsonb `null`.
	if _, err := pool.Exec(ctx, `
		insert into legacy_member_import_rows
		  (batch_id, source_row, status, legacy_staff_id, member_id, display_name,
		   email, phone, department_code, position, legacy_status, conflicts, warnings)
		values ($1, 2, 'valid', 'S-LAMA-1', 'M-LAMA-1', 'Ali Lama',
		        'ali.lama@test.local', '0123456789', 'BKP', 'Pegawai', 'Aktif',
		        'null'::jsonb, 'null'::jsonb)`, batchID); err != nil {
		t.Fatalf("seed baris null: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/legacy-member-import/"+batchID.String(), nil)
	c.Params = gin.Params{{Key: "id", Value: batchID.String()}}
	c.Set("userID", superadmin)
	h.GetBatch(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Rows) != 1 {
		t.Fatalf("mahu 1 baris, dapat %d", len(payload.Rows))
	}
	for _, field := range []string{"conflicts", "warnings"} {
		if got := string(payload.Rows[0][field]); got != "[]" {
			t.Errorf("rows[0].%s = %s, mahu []", field, got)
		}
	}
}

// TestListBatchesPulangArrayBilaKosong - `batches` mesti `[]` walaupun
// tiada satu batch pun, supaya `batches.map` di portal selamat.
func TestListBatchesPulangArrayBilaKosong(t *testing.T) {
	pool, ctx := statusTestPool(t)
	superadmin := seedMember(t, ctx, pool, "superadmin", "approved")
	h := NewLegacyMemberImportHandler(pool, nil, "http://localhost")

	if _, err := pool.Exec(ctx, `delete from legacy_member_import_batches`); err != nil {
		t.Fatalf("kosongkan batch: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/legacy-member-import/batches", nil)
	c.Set("userID", superadmin)
	h.ListBatches(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"batches":[]}` {
		t.Errorf("body = %s, mahu {\"batches\":[]}", got)
	}
}
