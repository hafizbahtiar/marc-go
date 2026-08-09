package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type AuditHandler struct {
	queries *sqlc.Queries
}

func NewAuditHandler(pool *pgxpool.Pool) *AuditHandler {
	return &AuditHandler{queries: sqlc.New(pool)}
}

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

type auditLogResponse struct {
	ID            int64           `json:"id"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	Action        string          `json:"action"`
	ActorID       *string         `json:"actor_id"`
	ActorMemberID *string         `json:"actor_member_id"`
	ActorRoleKey  *string         `json:"actor_role_key"`
	ChangedFields []string        `json:"changed_fields"`
	OldValues     json.RawMessage `json:"old_values"`
	NewValues     json.RawMessage `json:"new_values"`
	CreatedAt     string          `json:"created_at"`
}

// List (jejak audit) — management sahaja.
//
// Tapisan: ?entity_type=post&entity_id=<uuid>&action=update&actor_id=<uuid>
// Pagination keyset: ?before_id=<id terakhir yang diterima>&limit=50
//
// ip_address dan user_agent SENGAJA tak didedahkan di sini — ia disimpan
// untuk siasatan penyalahgunaan melalui akses DB terus, bukan untuk
// tontonan biasa mana-mana ahli pengurusan.
func (h *AuditHandler) List(c *gin.Context) {
	ctx := c.Request.Context()

	isManagement, err := authz.IsManagement(ctx, h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat jejak audit"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pengurusan boleh lihat jejak audit"})
		return
	}

	limit := auditDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit tidak sah"})
			return
		}
		limit = min(parsed, auditMaxLimit)
	}

	var beforeID pgtype.Int8
	if raw := c.Query("before_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before_id tidak sah"})
			return
		}
		beforeID = pgtype.Int8{Int64: parsed, Valid: true}
	}

	var actorID pgtype.UUID
	if raw := c.Query("actor_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "actor_id tidak sah"})
			return
		}
		actorID = pgtype.UUID{Bytes: parsed, Valid: true}
	}

	// entity_id ialah tapisan timeline satu entiti — guna query khusus yang
	// padan dengan index (entity_type, entity_id, id desc).
	if raw := c.Query("entity_id"); raw != "" {
		entityID, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "entity_id tidak sah"})
			return
		}
		entityType := c.Query("entity_type")
		if entityType == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "entity_type wajib bersama entity_id"})
			return
		}

		rows, err := h.queries.ListAuditLogsByEntity(ctx, sqlc.ListAuditLogsByEntityParams{
			EntityType: entityType,
			EntityID:   entityID,
			Limit:      int32(limit),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat jejak audit"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"logs": toAuditResponses(rows)})
		return
	}

	rows, err := h.queries.ListAuditLogs(ctx, sqlc.ListAuditLogsParams{
		EntityType: ptrToText(c.Query("entity_type")),
		Action:     ptrToText(c.Query("action")),
		ActorID:    actorID,
		BeforeID:   beforeID,
		RowLimit:   int32(limit),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat jejak audit"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": toAuditResponses(rows)})
}

func toAuditResponses(rows []sqlc.AuditLog) []auditLogResponse {
	out := make([]auditLogResponse, len(rows))
	for i, row := range rows {
		out[i] = auditLogResponse{
			ID:            row.ID,
			EntityType:    row.EntityType,
			EntityID:      row.EntityID.String(),
			Action:        row.Action,
			ActorID:       nullableUUIDString(row.ActorID),
			ActorMemberID: textToPtr(row.ActorMemberID),
			ActorRoleKey:  textToPtr(row.ActorRoleKey),
			ChangedFields: row.ChangedFields,
			OldValues:     json.RawMessage(row.OldValues),
			NewValues:     json.RawMessage(row.NewValues),
			CreatedAt:     formatTime(row.CreatedAt),
		}
	}
	return out
}
