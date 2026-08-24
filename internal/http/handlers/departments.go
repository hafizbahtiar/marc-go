// Package handlers - DepartmentsHandler ialah CRUD ringkas utk
// `departments` (rujukan bahagian/jabatan organisasi). Superadmin SAHAJA
// (padanan gate `blocked_email_domains.go`) - root-level config org-wide.
package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type DepartmentsHandler struct {
	queries *sqlc.Queries
}

func NewDepartmentsHandler(pool *pgxpool.Pool) *DepartmentsHandler {
	return &DepartmentsHandler{queries: sqlc.New(pool)}
}

func (h *DepartmentsHandler) requireSuperAdmin(c *gin.Context) bool {
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

// List - GET /admin/departments. Superadmin sahaja - skrin CRUD penuh.
func (h *DepartmentsHandler) List(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	rows, ok := h.listRows(c)
	if !ok {
		return // listRows dah tulis respons ralat
	}
	c.JSON(http.StatusOK, gin.H{"departments": rows})
}

// ListForAssignment - GET /departments. Manager KE ATAS (bukan superadmin
// sahaja) - baca SAHAJA, utk pemilih bahagian di skrin tetapkan
// bahagian/jawatan ahli (`PATCH /members/:id/department`). Berasingan
// drpd List (CRUD penuh) sebab manager/admin biasa tak patut boleh
// tambah/buang/edit rujukan bahagian, cuma pilih drpd senarai sedia ada.
func (h *DepartmentsHandler) ListForAssignment(c *gin.Context) {
	isManagerUp, err := authz.IsAtLeastRole(c.Request.Context(), h.queries, middleware.UserID(c), "manager")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return
	}
	if !isManagerUp {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk manager ke atas sahaja"})
		return
	}
	rows, ok := h.listRows(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"departments": rows})
}

// listRows - pulangkan (rows, false) SELEPAS menulis respons ralat kalau
// gagal, supaya pemanggil TIDAK menulis respons kedua (dua c.JSON pada
// context gin yang sama = respons rosak/panic).
func (h *DepartmentsHandler) listRows(c *gin.Context) ([]sqlc.Department, bool) {
	rows, err := h.queries.ListDepartments(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai bahagian"})
		return nil, false
	}
	if rows == nil {
		rows = []sqlc.Department{}
	}
	return rows, true
}

type addDepartmentRequest struct {
	Code      string `json:"code" binding:"required,max=50"`
	Name      string `json:"name" binding:"required,max=200"`
	SortOrder int32  `json:"sort_order"`
}

// Create - POST /admin/departments.
func (h *DepartmentsHandler) Create(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	var req addDepartmentRequest
	if !bindJSON(c, &req) {
		return
	}
	code := strings.TrimSpace(req.Code)
	name := strings.TrimSpace(req.Name)
	if code == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kod dan nama bahagian diperlukan"})
		return
	}
	// '/' dlm kod pecah routing PATCH/DELETE /admin/departments/:code (Gin
	// padan pada path yg dah didahulukan-decode - Opus verify 2026-08-25,
	// lihat migration 20260825110000). Tolak di sumber, bukan cuma dok
	// terperangkap lepas dicipta.
	if strings.Contains(code, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kod bahagian tidak boleh mengandungi '/'"})
		return
	}

	row, err := h.queries.AddDepartment(c.Request.Context(), sqlc.AddDepartmentParams{
		Code:      code,
		Name:      name,
		SortOrder: req.SortOrder,
		AddedBy:   pgUUID(middleware.UserID(c)),
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "kod bahagian sudah wujud"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal tambah bahagian"})
		return
	}
	c.JSON(http.StatusCreated, row)
}

type updateDepartmentRequest struct {
	Name      *string `json:"name"`
	SortOrder *int32  `json:"sort_order"`
}

// Update - PATCH /admin/departments/:code.
func (h *DepartmentsHandler) Update(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	code := c.Param("code")

	var req updateDepartmentRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nama bahagian tidak boleh kosong"})
		return
	}

	params := sqlc.UpdateDepartmentParams{Code: code}
	if req.Name != nil {
		params.Name = pgText(strings.TrimSpace(*req.Name))
	}
	if req.SortOrder != nil {
		params.SortOrder = pgtype.Int4{Int32: *req.SortOrder, Valid: true}
	}

	row, err := h.queries.UpdateDepartment(c.Request.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bahagian tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini bahagian"})
		return
	}
	c.JSON(http.StatusOK, row)
}

// Delete - DELETE /admin/departments/:code.
func (h *DepartmentsHandler) Delete(c *gin.Context) {
	if !h.requireSuperAdmin(c) {
		return
	}
	code := c.Param("code")
	n, err := h.queries.RemoveDepartment(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal buang bahagian"})
		return
	}
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bahagian tidak dijumpai"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
