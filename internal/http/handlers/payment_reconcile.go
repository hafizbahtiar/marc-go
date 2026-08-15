package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/paymentreconcile"
)

// PaymentReconcileHandler — pencetus manual internal/paymentreconcile,
// padanan pola AuditHandler (route hanya perlu `approved`, handler
// SENDIRI gate management melalui authz.IsManagement).
type PaymentReconcileHandler struct {
	reconciler *paymentreconcile.Reconciler
	queries    *sqlc.Queries
}

func NewPaymentReconcileHandler(reconciler *paymentreconcile.Reconciler, queries *sqlc.Queries) *PaymentReconcileHandler {
	return &PaymentReconcileHandler{reconciler: reconciler, queries: queries}
}

// Run — POST /admin/payments/reconcile. Management sahaja. Jalankan
// SATU pusingan internal/paymentreconcile serta-merta (sama logik dengan
// sapuan latar berkala) dan pulangkan ringkasan (checked/mismatches
// fixed/errors) supaya caller (dashboard management) nampak sesuatu yang
// berguna, bukan sekadar "ok".
func (h *PaymentReconcileHandler) Run(c *gin.Context) {
	ctx := c.Request.Context()

	isManagement, err := authz.IsManagement(ctx, h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jalankan reconcile"})
		return
	}
	if !isManagement {
		c.JSON(http.StatusForbidden, gin.H{"error": "akses ditolak"})
		return
	}

	summary := h.reconciler.RunOnce(ctx)
	c.JSON(http.StatusOK, summary)
}
