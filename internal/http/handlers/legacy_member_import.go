package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/auth"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
	"marc/internal/legacyimport"
)

const (
	legacyImportMaxBytes = 1 << 20
	legacyClaimTTL       = time.Hour
)

// marshalJSONArray memarshal slice kepada JSON, tetapi memulangkan `[]`
// untuk slice nil.
//
// encoding/json memarshal slice NIL sebagai `null`, bukan `[]`. Lajur
// conflicts/warnings ialah `jsonb not null default '[]'` - tetapi
// DEFAULT tak terpakai bila nilai dibekalkan secara eksplisit, dan
// `null` JSON bukan NULL SQL, jadi constraint NOT NULL pun tak
// menangkapnya. Hasilnya baris bersih tersimpan sebagai jsonb `null`
// dan API memulangkan `"conflicts": null`.
//
// Portal web memanggil `row.conflicts.length` terus, jadi `null` itu
// meletupkan render SSR seluruh halaman /settings/legacy-import.
func marshalJSONArray[T any](items []T) ([]byte, error) {
	if items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(items)
}

// normalizeJSONArray menukar jsonb `null` (dan bacaan kosong) kepada
// `[]` semasa BACA. Diperlukan berasingan daripada marshalJSONArray
// sebab baris yang SUDAH tersimpan sebagai `null` sebelum pembetulan
// ini masih ada dalam DB - normalisasi di sini memulihkan halaman
// tanpa perlu menyentuh data produksi.
func normalizeJSONArray(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

type LegacyMemberImportHandler struct {
	pool          *pgxpool.Pool
	queries       *sqlc.Queries
	emailClient   *email.Client
	publicBaseURL string
}

func NewLegacyMemberImportHandler(pool *pgxpool.Pool, emailClient *email.Client, publicBaseURL string) *LegacyMemberImportHandler {
	return &LegacyMemberImportHandler{
		pool:          pool,
		queries:       sqlc.New(pool),
		emailClient:   emailClient,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

func (h *LegacyMemberImportHandler) requireSuperAdmin(c *gin.Context) bool {
	ok, err := authz.IsAtLeastRole(c.Request.Context(), h.queries, middleware.UserID(c), superAdminRoleKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk superadmin sahaja"})
		return false
	}
	return true
}

// DryRun menerima fail CSV, menyimpan keputusan staging dan tidak menyentuh
// users/profiles. Baris konflik kekal boleh dilihat supaya superadmin boleh
// membetulkannya sebelum import.
func (h *LegacyMemberImportHandler) DryRun(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, legacyImportMaxBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fail CSV diperlukan"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fail CSV tidak dapat dibaca"})
		return
	}
	report, err := legacyimport.Parse(bytes.NewReader(data))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	report, err = h.addDatabaseConflicts(c.Request.Context(), report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak data sedia ada"})
		return
	}

	sum := sha256.Sum256(data)
	batchID, err := h.storeReport(c.Request.Context(), middleware.UserID(c), header.Filename, hex.EncodeToString(sum[:]), report)
	if err != nil {
		log.Printf("legacy import dry-run: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal simpan laporan import"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":            batchID,
		"source_file":   header.Filename,
		"total_rows":    report.TotalRows,
		"valid_rows":    report.ValidRows,
		"conflict_rows": report.TotalRows - report.ValidRows,
		"warnings":      report.Warnings,
		"header_row":    report.HeaderRow,
	})
}

func (h *LegacyMemberImportHandler) ListBatches(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	rows, err := h.pool.Query(c.Request.Context(), `
		select id, source_filename, status, total_rows, valid_rows, conflict_rows, created_at
		from legacy_member_import_batches
		order by created_at desc limit 50`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat batch import"})
		return
	}
	defer rows.Close()
	result := make([]gin.H, 0)
	for rows.Next() {
		var id uuid.UUID
		var filename, status string
		var total, valid, conflicts int
		var createdAt time.Time
		if err := rows.Scan(&id, &filename, &status, &total, &valid, &conflicts, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca batch import"})
			return
		}
		result = append(result, gin.H{
			"id": id, "source_filename": filename, "status": status,
			"total_rows": total, "valid_rows": valid, "conflict_rows": conflicts,
			"created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"batches": result})
}

func (h *LegacyMemberImportHandler) GetBatch(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	batchID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id batch tidak sah"})
		return
	}
	departments, err := h.departmentCodes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak bahagian import"})
		return
	}
	rows, err := h.pool.Query(c.Request.Context(), `
		select id, source_row, status, legacy_staff_id, member_id, display_name,
		       email, phone, department_code, position, legacy_status,
		       conflicts, warnings, user_id
		from legacy_member_import_rows
		where batch_id = $1
		order by source_row`, batchID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat baris import"})
		return
	}
	defer rows.Close()
	result := make([]gin.H, 0)
	for rows.Next() {
		var id uuid.UUID
		var sourceRow int
		var status, staffID, memberID, name, emailAddress, phone, department, position, legacyStatus string
		var conflicts, warnings json.RawMessage
		var userID *uuid.UUID
		if err := rows.Scan(&id, &sourceRow, &status, &staffID, &memberID, &name, &emailAddress, &phone, &department, &position, &legacyStatus, &conflicts, &warnings, &userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca baris import"})
			return
		}
		conflicts = normalizeJSONArray(conflicts)
		warnings = normalizeJSONArray(warnings)
		var conflictList []legacyimport.Conflict
		if err := json.Unmarshal(conflicts, &conflictList); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "format konflik import tidak sah"})
			return
		}
		if unknown := legacyimport.UnknownDepartmentConflict(department, departments); unknown != nil {
			found := false
			for _, conflict := range conflictList {
				if conflict.Code == unknown.Code {
					found = true
					break
				}
			}
			if !found {
				conflictList = append(conflictList, *unknown)
				conflicts, _ = marshalJSONArray(conflictList)
			}
		}
		result = append(result, gin.H{
			"id": id, "source_row": sourceRow, "status": status,
			"legacy_staff_id": staffID, "member_id": memberID, "display_name": name,
			"email": emailAddress, "phone": phone, "department_code": department,
			"position": position, "legacy_status": legacyStatus,
			"conflicts": json.RawMessage(conflicts), "warnings": json.RawMessage(warnings), "user_id": userID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"rows": result})
}

// Import applies only clean rows. Rows without an existing account remain in
// staging and can later be claimed by their owner.
func (h *LegacyMemberImportHandler) Import(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	batchID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id batch tidak sah"})
		return
	}
	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mula import"})
		return
	}
	defer tx.Rollback(ctx)
	txQueries := h.queries.WithTx(tx)
	actor := auditActor(c, txQueries)
	departments, err := h.departmentCodes(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak bahagian import"})
		return
	}
	rows, err := tx.Query(ctx, `
		select id, legacy_staff_id, member_id, display_name, email, phone,
		       department_code, position, emergency_name, emergency_phone,
		       health_notes, address, legacy_status, user_id
		from legacy_member_import_rows
		where batch_id = $1 and status = 'valid'
		order by source_row
		for update`, batchID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca baris import"})
		return
	}
	defer rows.Close()
	imported := 0
	blocked := 0
	for rows.Next() {
		var rowID uuid.UUID
		var staffID, memberID, name, emailAddress, phone, department, position, emergencyName, emergencyPhone, health, address, legacyStatus string
		var userID *uuid.UUID
		if err := rows.Scan(&rowID, &staffID, &memberID, &name, &emailAddress, &phone, &department, &position, &emergencyName, &emergencyPhone, &health, &address, &legacyStatus, &userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca data import"})
			return
		}
		canonicalDepartment, departmentOK := legacyimport.CanonicalDepartmentCode(department, departments)
		if !departmentOK {
			conflict, _ := marshalJSONArray([]legacyimport.Conflict{
				*legacyimport.UnknownDepartmentConflict(department, departments),
			})
			if _, err := tx.Exec(ctx, `
				update legacy_member_import_rows
				set status = 'conflict',
				    conflicts = coalesce(nullif(conflicts, 'null'::jsonb), '[]'::jsonb) || $2::jsonb
				where id = $1
			`, rowID, conflict); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tandakan konflik import"})
				return
			}
			blocked++
			continue
		}
		department = canonicalDepartment
		if userID == nil {
			continue
		}
		var profileID uuid.UUID
		if err := tx.QueryRow(ctx, `
			update profiles set
			  display_name = coalesce(display_name, nullif($2, '')),
			  phone = coalesce(phone, nullif($3, '')),
			  department_code = coalesce(department_code, nullif($4, '')),
			  position = coalesce(position, nullif($5, '')),
			  emergency_contact_name = coalesce(emergency_contact_name, nullif($6, '')),
			  emergency_contact_phone = coalesce(emergency_contact_phone, nullif($7, '')),
			  health_notes = coalesce(health_notes, nullif($8, '')),
			  staff_id = case when staff_id = user_id::text then $9 else staff_id end,
			  member_id = coalesce(member_id, nullif($10, '')),
			  is_active = case when $11 = 'Aktif' then true when $11 = 'Tidak Aktif' then false else is_active end,
			  staff_id_verified_at = coalesce(staff_id_verified_at, now())
			where user_id = $1
			returning id`, *userID, name, phone, department, position, emergencyName, emergencyPhone, health, staffID, memberID, legacyStatus).Scan(&profileID); err != nil {
			log.Printf("legacy import profile update failed: batch=%s row=%s user=%s email=%q department=%q err=%v",
				batchID, rowID, userID, emailAddress, department, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini profil import"})
			return
		}
		if err := audit.Record(ctx, txQueries, audit.Entry{
			EntityType: audit.EntityProfile,
			EntityID:   profileID,
			Action:     audit.ActionUpdate,
			Actor:      actor,
			New: map[string]any{
				"display_name": name, "phone": phone, "department_code": department,
				"position": position, "emergency_contact_name": emergencyName,
				"emergency_contact_phone": emergencyPhone, "health_notes": health,
				"staff_id": staffID, "member_id": memberID,
				"is_active": legacyStatus,
			},
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod audit import"})
			return
		}
		if _, err := tx.Exec(ctx, `update legacy_member_import_rows set status = 'imported' where id = $1`, rowID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini status import"})
			return
		}
		imported++
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal import baris"})
		return
	}
	batchStatus := "imported"
	if blocked > 0 {
		batchStatus = "ready"
	}
	if _, err := tx.Exec(ctx, `update legacy_member_import_batches set status = $2 where id = $1`, batchID, batchStatus); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini batch import"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan import"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"imported":  imported,
		"blocked":   blocked,
		"unclaimed": "baris tanpa akaun kekal untuk claim",
	})
}

type legacyClaimRequest struct {
	Email   string `json:"email" binding:"required,email"`
	StaffID string `json:"staff_id" binding:"required,max=64"`
}

func (h *LegacyMemberImportHandler) RequestClaim(c *gin.Context) {
	var req legacyClaimRequest
	if !bindJSON(c, &req) {
		return
	}
	emailAddress := strings.ToLower(strings.TrimSpace(req.Email))
	staffID := strings.TrimSpace(req.StaffID)
	ctx := c.Request.Context()
	// Always return the same response, including for no-match and existing
	// accounts, so the endpoint cannot enumerate legacy membership records.
	rowID, matchedEmail, err := h.findClaimRow(ctx, emailAddress, staffID)
	if err == nil && rowID != uuid.Nil && matchedEmail != "" {
		token, tokenErr := auth.GenerateOpaqueToken()
		if tokenErr == nil {
			_, _ = h.pool.Exec(ctx, `delete from legacy_member_claim_tokens where row_id = $1`, rowID)
			_, tokenErr = h.pool.Exec(ctx, `
				insert into legacy_member_claim_tokens (row_id, token_hash, expires_at)
				values ($1, $2, $3)`, rowID, auth.HashToken(token), time.Now().Add(legacyClaimTTL))
			if tokenErr == nil {
				link := fmt.Sprintf("%s/claim-account?token=%s", h.publicBaseURL, token)
				if h.emailClient != nil && h.emailClient.Enabled() {
					if err := h.emailClient.Send(ctx, matchedEmail, "Tuntut akaun MARC", claimEmailHTML(link)); err != nil {
						log.Printf("gagal hantar claim email: %v", err)
					}
				} else {
					log.Printf("legacy claim link (provider belum configure): %s", link)
				}
			}
		}
	}
	c.Status(http.StatusNoContent)
}

type completeClaimRequest struct {
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required,min=6,max=72"`
}

func (h *LegacyMemberImportHandler) CompleteClaim(c *gin.Context) {
	var req completeClaimRequest
	if !bindJSON(c, &req) {
		return
	}
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sediakan kata laluan"})
		return
	}
	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mula tuntutan akaun"})
		return
	}
	defer tx.Rollback(ctx)
	var rowID, batchOwner uuid.UUID
	var emailAddress, staffID, memberID, name, phone, department, position, emergencyName, emergencyPhone, health string
	var rowUserID *uuid.UUID
	err = tx.QueryRow(ctx, `
		select r.id, b.created_by, r.email, r.legacy_staff_id, r.member_id,
		       r.display_name, r.phone, r.department_code, r.position,
		       r.emergency_name, r.emergency_phone, r.health_notes, r.user_id
		from legacy_member_claim_tokens t
		join legacy_member_import_rows r on r.id = t.row_id
		join legacy_member_import_batches b on b.id = r.batch_id
		where t.token_hash = $1 and t.consumed_at is null and t.expires_at > now()
		for update of t, r`, auth.HashToken(req.Token)).
		Scan(&rowID, &batchOwner, &emailAddress, &staffID, &memberID, &name, &phone, &department, &position, &emergencyName, &emergencyPhone, &health, &rowUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pautan claim tidak sah atau sudah luput"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak pautan claim"})
		return
	}
	if rowUserID != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "akaun untuk rekod ini sudah wujud"})
		return
	}
	departments, err := h.departmentCodes(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak bahagian tuntutan"})
		return
	}
	canonicalDepartment, departmentOK := legacyimport.CanonicalDepartmentCode(department, departments)
	if !departmentOK {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Kod bahagian tidak wujud: %s.", department)})
		return
	}
	department = canonicalDepartment
	var roleID int16
	if err := tx.QueryRow(ctx, `select id from roles where key = 'ahli'`).Scan(&roleID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "role ahli tidak ditemui"})
		return
	}
	var userID uuid.UUID
	if err := tx.QueryRow(ctx, `insert into users (email, password_hash) values ($1, $2) returning id`, emailAddress, passwordHash).Scan(&userID); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "akaun dengan emel ini sudah wujud"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta akaun"})
		return
	}
	_, err = tx.Exec(ctx, `
		insert into profiles (
		  user_id, member_id, staff_id, display_name, phone, role_id,
		  email_verified, status, department_code, position,
		  emergency_contact_name, emergency_contact_phone, health_notes,
		  staff_id_verified_at, staff_id_verified_by
		) values ($1, nullif($2, ''), $3, nullif($4, ''), nullif($5, ''), $6,
		          true, 'approved', nullif($7, ''), nullif($8, ''),
		          nullif($9, ''), nullif($10, ''), nullif($11, ''), now(), $12)`,
		userID, memberID, staffID, name, phone, roleID, department, position,
		emergencyName, emergencyPhone, health, batchOwner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta profil ahli"})
		return
	}
	if _, err := tx.Exec(ctx, `update legacy_member_claim_tokens set consumed_at = now() where row_id = $1`, rowID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal lengkapkan tuntutan"})
		return
	}
	if _, err := tx.Exec(ctx, `update legacy_member_import_rows set status = 'claimed', user_id = $2 where id = $1`, rowID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal simpan status tuntutan"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan tuntutan"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *LegacyMemberImportHandler) findClaimRow(ctx context.Context, emailAddress, staffID string) (uuid.UUID, string, error) {
	var rowID uuid.UUID
	var matchedEmail string
	err := h.pool.QueryRow(ctx, `
		select r.id, r.email
		from legacy_member_import_rows r
		join legacy_member_import_batches b on b.id = r.batch_id
		where r.status in ('valid', 'imported')
		  and r.user_id is null
		  and lower(r.email) = lower($1)
		  and r.legacy_staff_id = $2
		order by b.created_at desc
		limit 1`, emailAddress, staffID).Scan(&rowID, &matchedEmail)
	return rowID, matchedEmail, err
}

func (h *LegacyMemberImportHandler) addDatabaseConflicts(ctx context.Context, report legacyimport.Report) (legacyimport.Report, error) {
	departments, err := h.departmentCodes(ctx)
	if err != nil {
		return report, err
	}

	for i := range report.Rows {
		row := &report.Rows[i]
		if canonical, ok := legacyimport.CanonicalDepartmentCode(row.DepartmentCode, departments); ok {
			row.DepartmentCode = canonical
		} else {
			row.Conflicts = append(row.Conflicts, *legacyimport.UnknownDepartmentConflict(row.DepartmentCode, departments))
		}
		var existingUser *uuid.UUID
		err := h.pool.QueryRow(ctx, `select id from users where lower(email) = lower($1)`, row.NormalizedEmail).Scan(&existingUser)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return report, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			existingUser = nil
		}
		var conflictUser *uuid.UUID
		err = h.pool.QueryRow(ctx, `select user_id from profiles where staff_id = $1`, row.LegacyStaffID).Scan(&conflictUser)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return report, err
		}
		if err == nil && (existingUser == nil || *conflictUser != *existingUser) {
			row.Conflicts = append(row.Conflicts, legacyimport.Conflict{Code: "existing_staff_id", Message: "No. ID. sudah digunakan oleh akaun lain."})
		}
		err = h.pool.QueryRow(ctx, `select user_id from profiles where member_id = $1`, row.MemberID).Scan(&conflictUser)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return report, err
		}
		if err == nil && (existingUser == nil || *conflictUser != *existingUser) {
			row.Conflicts = append(row.Conflicts, legacyimport.Conflict{Code: "existing_member_id", Message: "No. Ahli sudah digunakan oleh akaun lain."})
		}
	}
	report.Conflicts = 0
	report.ValidRows = 0
	for i := range report.Rows {
		report.Conflicts += len(report.Rows[i].Conflicts)
		if len(report.Rows[i].Conflicts) == 0 {
			report.ValidRows++
		}
	}
	return report, nil
}

func (h *LegacyMemberImportHandler) departmentCodes(ctx context.Context) (map[string]string, error) {
	rows, err := h.pool.Query(ctx, `select code from departments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	departments := make(map[string]string)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		departments[strings.ToLower(strings.TrimSpace(code))] = code
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return departments, nil
}

func (h *LegacyMemberImportHandler) storeReport(ctx context.Context, createdBy uuid.UUID, filename, checksum string, report legacyimport.Report) (uuid.UUID, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var batchID uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into legacy_member_import_batches
		  (source_filename, source_sha256, created_by, total_rows, valid_rows, conflict_rows)
		values ($1, $2, $3, $4, $5, $6) returning id`,
		filename, checksum, createdBy, report.TotalRows, report.ValidRows, report.TotalRows-report.ValidRows).Scan(&batchID); err != nil {
		return uuid.Nil, err
	}
	for _, row := range report.Rows {
		conflicts, _ := marshalJSONArray(row.Conflicts)
		warnings, _ := marshalJSONArray(row.Warnings)
		status := "valid"
		if len(row.Conflicts) > 0 {
			status = "conflict"
		}
		var userID *uuid.UUID
		if row.NormalizedEmail != "" {
			_ = tx.QueryRow(ctx, `select id from users where lower(email) = lower($1)`, row.NormalizedEmail).Scan(&userID)
		}
		if _, err := tx.Exec(ctx, `
			insert into legacy_member_import_rows
			  (batch_id, source_row, legacy_number, status, legacy_staff_id, member_id,
			   display_name, email, phone, department_code, position, emergency_name,
			   emergency_phone, health_notes, address, category, club_position,
			   legacy_status, conflicts, warnings, user_id)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
			batchID, row.SourceRow, row.LegacyNumber, status, row.LegacyStaffID, row.MemberID,
			row.DisplayName, row.NormalizedEmail, row.NormalizedPhone, row.DepartmentCode,
			row.Position, row.EmergencyName, row.EmergencyPhone, row.HealthNotes, row.Address,
			row.Category, row.ClubPosition, row.Status, conflicts, warnings, userID); err != nil {
			return uuid.Nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return batchID, nil
}

func claimEmailHTML(link string) string {
	return fmt.Sprintf(`<html><body><p>Gunakan pautan ini untuk menuntut akaun MARC anda:</p><p><a href="%s">Tuntut akaun MARC</a></p><p>Pautan ini sah selama 1 jam.</p></body></html>`, link)
}
