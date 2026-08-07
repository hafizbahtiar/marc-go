package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type NotificationHandler struct {
	queries *sqlc.Queries
}

func NewNotificationHandler(pool *pgxpool.Pool) *NotificationHandler {
	return &NotificationHandler{queries: sqlc.New(pool)}
}

type notificationResponse struct {
	ID        string  `json:"id"`
	ActorID   string  `json:"actor_id"`
	Type      string  `json:"type"`
	PostID    *string `json:"post_id"`
	CommentID *string `json:"comment_id"`
	Read      bool    `json:"read"`
	CreatedAt string  `json:"created_at"`
}

func (h *NotificationHandler) List(c *gin.Context) {
	limit := defaultPageLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	var cursorCreatedAt pgtype.Timestamptz
	var cursorID pgtype.UUID
	if v := c.Query("cursor"); v != "" {
		t, id, err := decodeCursor(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cursor tidak sah"})
			return
		}
		cursorCreatedAt = pgtype.Timestamptz{Time: t, Valid: true}
		cursorID = pgtype.UUID{Bytes: id, Valid: true}
	}

	rows, err := h.queries.ListNotifications(c.Request.Context(), sqlc.ListNotificationsParams{
		RecipientID:     middleware.UserID(c),
		CursorCreatedAt: cursorCreatedAt,
		CursorID:        cursorID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat notifikasi"})
		return
	}

	resp := make([]notificationResponse, len(rows))
	for i, r := range rows {
		resp[i] = notificationResponse{
			ID:        r.ID.String(),
			ActorID:   r.ActorID.String(),
			Type:      r.Type,
			PostID:    nullableUUIDString(r.PostID),
			CommentID: nullableUUIDString(r.CommentID),
			Read:      r.ReadAt.Valid,
			CreatedAt: formatTime(r.CreatedAt),
		}
	}

	var nextCursor *string
	if len(resp) == limit {
		last := rows[len(rows)-1]
		s := encodeCursor(last.CreatedAt.Time, last.ID)
		nextCursor = &s
	}

	c.JSON(http.StatusOK, gin.H{"notifications": resp, "next_cursor": nextCursor})
}

func (h *NotificationHandler) MarkRead(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	if err := h.queries.MarkNotificationRead(c.Request.Context(), sqlc.MarkNotificationReadParams{
		ID: id, RecipientID: middleware.UserID(c),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda dibaca"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	if err := h.queries.MarkAllNotificationsRead(c.Request.Context(), middleware.UserID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tanda semua dibaca"})
		return
	}

	c.Status(http.StatusNoContent)
}
