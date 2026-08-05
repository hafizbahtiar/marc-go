package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type DeviceTokenHandler struct {
	queries *sqlc.Queries
}

func NewDeviceTokenHandler(pool *pgxpool.Pool) *DeviceTokenHandler {
	return &DeviceTokenHandler{queries: sqlc.New(pool)}
}

type upsertDeviceTokenRequest struct {
	OnesignalID string `json:"onesignal_id" binding:"required"`
	Platform    string `json:"platform"`
}

// Upsert setara RPC `upsert_device_token` — daftar/kemas kini push
// subscription id peranti untuk user semasa.
func (h *DeviceTokenHandler) Upsert(c *gin.Context) {
	var req upsertDeviceTokenRequest
	if !bindJSON(c, &req) {
		return
	}

	err := h.queries.UpsertDeviceToken(c.Request.Context(), sqlc.UpsertDeviceTokenParams{
		UserID:      middleware.UserID(c),
		OnesignalID: req.OnesignalID,
		Platform:    ptrToText(req.Platform),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal simpan device token"})
		return
	}

	c.Status(http.StatusNoContent)
}

// Delete buang device token — discope terus dalam query (id + user_id),
// jadi user tak boleh padam token orang lain walaupun teka id betul.
func (h *DeviceTokenHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	if err := h.queries.DeleteDeviceToken(c.Request.Context(), sqlc.DeleteDeviceTokenParams{
		ID:     id,
		UserID: middleware.UserID(c),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang device token"})
		return
	}

	c.Status(http.StatusNoContent)
}

// DeleteByOnesignalID — sama macam Delete, tapi discope guna onesignal_id
// (bukan row id Postgres). Dipakai waktu logout: Flutter tahu
// `OneSignal.User.pushSubscription.id` terus dari SDK, tak perlu simpan/
// query row id balik daripada POST /device-tokens (yang cuma pulang 204).
func (h *DeviceTokenHandler) DeleteByOnesignalID(c *gin.Context) {
	onesignalID := c.Param("onesignalId")
	if onesignalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "onesignal id diperlukan"})
		return
	}

	if err := h.queries.DeleteDeviceTokenByOnesignalID(c.Request.Context(), sqlc.DeleteDeviceTokenByOnesignalIDParams{
		OnesignalID: onesignalID,
		UserID:      middleware.UserID(c),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang device token"})
		return
	}

	c.Status(http.StatusNoContent)
}
