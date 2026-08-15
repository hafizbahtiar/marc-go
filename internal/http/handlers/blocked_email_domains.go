// Package handlers — BlockedEmailDomainsHandler ialah CRUD ringkas utk
// `blocked_email_domains` (pelengkap kpd senarai statik terbenam
// internal/disposableemail — lihat komen package tu untuk rasional
// penuh). Superadmin SAHAJA (bukan management umum) — keputusan produk
// 2026-08-15: jadual ni kawal SIAPA BOLEH DAFTAR langsung (root-level
// config, kesan seluruh sistem), beza drpd kategori aktiviti (skop
// modul tunggal) yang cukup dgn "manager ke atas".
package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type BlockedEmailDomainsHandler struct {
	queries *sqlc.Queries
}

func NewBlockedEmailDomainsHandler(pool *pgxpool.Pool) *BlockedEmailDomainsHandler {
	return &BlockedEmailDomainsHandler{queries: sqlc.New(pool)}
}

func (h *BlockedEmailDomainsHandler) requireSuperAdmin(c *gin.Context) bool {
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

// List — GET /admin/blocked-email-domains.
func (h *BlockedEmailDomainsHandler) List(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	rows, err := h.queries.ListBlockedEmailDomains(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai domain"})
		return
	}
	if rows == nil {
		rows = []sqlc.BlockedEmailDomain{}
	}
	c.JSON(http.StatusOK, gin.H{"domains": rows})
}

type addBlockedEmailDomainRequest struct {
	Domain string `json:"domain" binding:"required,max=253"`
}

// Create — POST /admin/blocked-email-domains. Tambahan MANUAL kpd
// senarai statik terbenam — utk domain baharu yang senarai tu terlepas.
func (h *BlockedEmailDomainsHandler) Create(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	var req addBlockedEmailDomainRequest
	if !bindJSON(c, &req) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain diperlukan"})
		return
	}

	row, err := h.queries.AddBlockedEmailDomain(c.Request.Context(), sqlc.AddBlockedEmailDomainParams{
		Domain:  domain,
		AddedBy: pgUUID(middleware.UserID(c)),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tambah domain"})
			return
		}
		// `on conflict do nothing` — kalau domain dah wujud, `:one` +
		// RETURNING kosong pulang `pgx.ErrNoRows` (BUKAN struct sifar
		// nilai + nil error — Opus verify 2026-08-15 tangkap salah anggap
		// ni). Layan sebagai berjaya (idempoten, padanan pola
		// ApproveProfile, profile.go:748-759) — domain tu MEMANG disekat
		// selepas panggilan ni, tak kira sama ada baris ni yang cipta
		// atau baris sedia ada.
		row.Domain = domain
	}
	c.JSON(http.StatusCreated, row)
}

// Delete — DELETE /admin/blocked-email-domains/:domain.
func (h *BlockedEmailDomainsHandler) Delete(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(c.Param("domain")))
	if _, err := h.queries.RemoveBlockedEmailDomain(c.Request.Context(), domain); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang domain"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
