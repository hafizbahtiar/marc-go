package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

const accountDeletionReason = "account_deleted"

func validateDeletionReason(reason string) (string, error) {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "", fmt.Errorf("sebab pemadaman diperlukan")
	}
	if len(trimmed) > 500 {
		return "", fmt.Errorf("sebab pemadaman terlalu panjang")
	}
	return trimmed, nil
}

type AccountLifecycleHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewAccountLifecycleHandler(pool *pgxpool.Pool) *AccountLifecycleHandler {
	return &AccountLifecycleHandler{pool: pool, queries: sqlc.New(pool)}
}

func (h *AccountLifecycleHandler) requireSuperAdmin(c *gin.Context) bool {
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

type accountDeletionRow struct {
	UserID       uuid.UUID `json:"user_id"`
	MemberID     *string   `json:"member_id"`
	DisplayName  *string   `json:"display_name"`
	Email        string    `json:"email"`
	RoleKey      string    `json:"role_key"`
	AccountState string    `json:"account_status"`
	Status       string    `json:"status"`
	RequestedAt  string    `json:"requested_at"`
	CompletedAt  *string   `json:"completed_at"`
}

// List - GET /admin/account-deletion-requests.
func (h *AccountLifecycleHandler) List(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}

	rows, err := h.pool.Query(c.Request.Context(), `
		select adr.user_id, p.member_id, p.display_name, u.email, r.key,
		       p.status, adr.status, adr.requested_at, adr.completed_at
		from account_deletion_requests adr
		join users u on u.id = adr.user_id
		join profiles p on p.user_id = u.id
		join roles r on r.id = p.role_id
		order by adr.requested_at asc
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat permintaan pemadaman akaun"})
		return
	}
	defer rows.Close()

	result := make([]accountDeletionRow, 0)
	for rows.Next() {
		var row accountDeletionRow
		var requestedAt time.Time
		var completedAt *time.Time
		if err := rows.Scan(
			&row.UserID,
			&row.MemberID,
			&row.DisplayName,
			&row.Email,
			&row.RoleKey,
			&row.AccountState,
			&row.Status,
			&requestedAt,
			&completedAt,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca permintaan pemadaman akaun"})
			return
		}
		row.RequestedAt = requestedAt.Format(time.RFC3339)
		if completedAt != nil {
			formatted := completedAt.Format(time.RFC3339)
			row.CompletedAt = &formatted
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca permintaan pemadaman akaun"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"requests": result})
}

// ListTargets - GET /admin/account-deletion-targets.
// Direct deletion intentionally excludes all superadmin accounts.
func (h *AccountLifecycleHandler) ListTargets(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}

	rows, err := h.pool.Query(c.Request.Context(), `
		select u.id, p.member_id, p.display_name, u.email, r.key,
		       p.status, '', u.created_at, null
		from users u
		join profiles p on p.user_id = u.id
		join roles r on r.id = p.role_id
		where r.key <> $1
		order by p.display_name nulls last, u.email
	`, superAdminRoleKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai akaun"})
		return
	}
	defer rows.Close()

	result := make([]accountDeletionRow, 0)
	for rows.Next() {
		var row accountDeletionRow
		var createdAt time.Time
		if err := rows.Scan(
			&row.UserID,
			&row.MemberID,
			&row.DisplayName,
			&row.Email,
			&row.RoleKey,
			&row.AccountState,
			&row.Status,
			&createdAt,
			&row.CompletedAt,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca senarai akaun"})
			return
		}
		row.RequestedAt = createdAt.Format(time.RFC3339)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca senarai akaun"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"accounts": result})
}

func deletionRejection(callerID, targetID uuid.UUID, targetRole string, superadminCount int64) string {
	if callerID == targetID {
		return "akaun sendiri tidak boleh dipadam melalui modul ini"
	}
	if targetRole == superAdminRoleKey {
		return "akaun superadmin tidak boleh dipadam melalui modul ini"
	}
	if targetRole == superAdminRoleKey && superadminCount <= 1 {
		return "superadmin terakhir tidak boleh dipadam"
	}
	return ""
}

// Execute - POST /admin/account-deletion-requests/:id/execute.
func (h *AccountLifecycleHandler) Execute(c *gin.Context) {
	h.execute(c, true, "member_request", "")
}

type directDeletionRequest struct {
	Reason string `json:"reason" binding:"required,max=500"`
}

// ExecuteDirect - POST /admin/account-deletion-targets/:id/execute.
func (h *AccountLifecycleHandler) ExecuteDirect(c *gin.Context) {
	var request directDeletionRequest
	if !bindJSON(c, &request) {
		return
	}
	reason, err := validateDeletionReason(request.Reason)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.execute(c, false, "admin_direct", reason)
}

func (h *AccountLifecycleHandler) execute(c *gin.Context, requiresRequest bool, mode, reason string) {
	if !h.requireSuperAdmin(c) {
		return
	}

	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID pengguna tidak sah"})
		return
	}
	callerID := middleware.UserID(c)
	if rejection := deletionRejection(callerID, targetID, "", 0); rejection != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": rejection})
		return
	}

	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mula pemadaman akaun"})
		return
	}
	defer tx.Rollback(ctx)

	var email, roleKey string
	var memberID, displayName *string
	err = tx.QueryRow(ctx, `
		select u.email, r.key, p.member_id, p.display_name
		from users u
		join profiles p on p.user_id = u.id
		join roles r on r.id = p.role_id
		where u.id = $1
		for update
	`, targetID).Scan(&email, &roleKey, &memberID, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "akaun tidak dijumpai"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca akaun sasaran"})
		return
	}
	if requiresRequest {
		var requested bool
		if err := tx.QueryRow(ctx, `
			select exists(
				select 1 from account_deletion_requests
				where user_id = $1 and status = 'pending'
			)
		`, targetID).Scan(&requested); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak permintaan pemadaman"})
			return
		}
		if !requested {
			c.JSON(http.StatusConflict, gin.H{"error": "akaun ini tiada permintaan pemadaman yang menunggu"})
			return
		}
	}

	var superadminCount int64
	if err := tx.QueryRow(ctx, `
		select count(*)
		from profiles p
		join roles r on r.id = p.role_id
		where r.key = $1
	`, superAdminRoleKey).Scan(&superadminCount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak akaun superadmin"})
		return
	}
	if rejection := deletionRejection(callerID, targetID, roleKey, superadminCount); rejection != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": rejection})
		return
	}

	keys, err := userUploadKeys(ctx, tx, targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal senarai fail akaun"})
		return
	}
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `
			insert into deleted_uploads (r2_key, reason)
			values ($1, $2)
			on conflict (r2_key) do nothing
		`, key, accountDeletionReason); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jadualkan pemadaman fail"})
			return
		}
	}

	// Historical rows remain, but references to the deleted user are cleared.
	for _, statement := range []string{
		`update profiles set approved_by = null where approved_by = $1`,
		`update profiles set staff_id_verified_by = null where staff_id_verified_by = $1`,
		`update legacy_member_import_batches set created_by = null where created_by = $1`,
		`update legacy_member_import_rows set user_id = null where user_id = $1`,
		`update donations set user_id = null where user_id = $1`,
	} {
		if _, err := tx.Exec(ctx, statement, targetID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal putuskan rujukan sejarah akaun"})
			return
		}
	}

	q := h.queries.WithTx(tx)
	actor := auditActor(c, q)
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityProfile,
		EntityID:   targetID,
		Action:     audit.ActionDelete,
		Actor:      actor,
		Old: map[string]any{
			"email":         email,
			"member_id":     pointerValue(memberID),
			"display_name":  pointerValue(displayName),
			"role_key":      roleKey,
			"deletion_mode": mode,
			"reason":        reason,
		},
	}); err != nil {
		log.Printf("audit pemadaman akaun: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal rekod audit pemadaman akaun"})
		return
	}

	if _, err := tx.Exec(ctx, `delete from users where id = $1`, targetID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam akaun"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal sahkan pemadaman akaun"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "user_id": targetID})
}

func userUploadKeys(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		select avatar_r2_key from profiles
		where user_id = $1 and avatar_r2_key is not null
		union
		select r2_key from pending_uploads where user_id = $1
		union
		select pi.r2_key
		from post_images pi
		join posts p on p.id = pi.post_id
		where p.author_id = $1
		union
		select r2_key from activity_certificates
		where user_id = $1 and r2_key is not null
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func pointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
