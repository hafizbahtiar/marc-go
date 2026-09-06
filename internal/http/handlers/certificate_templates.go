package handlers

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

var certificateHexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type CertificateTemplateHandler struct {
	queries *sqlc.Queries
}

func NewCertificateTemplateHandler(pool *pgxpool.Pool) *CertificateTemplateHandler {
	return &CertificateTemplateHandler{queries: sqlc.New(pool)}
}

func (h *CertificateTemplateHandler) requireManagement(c *gin.Context) bool {
	ok, err := authz.IsManagement(c.Request.Context(), h.queries, middleware.UserID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal semak kebenaran"})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "tindakan ini untuk pengurusan sahaja"})
		return false
	}
	return true
}

type certificateTemplateResponse struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	IsActive       bool      `json:"is_active"`
	PrimaryColor   string    `json:"primary_color"`
	SecondaryColor string    `json:"secondary_color"`
	LogoURL        *string   `json:"logo_url"`
	Title          string    `json:"title"`
	Subtitle       string    `json:"subtitle"`
	BodyText       string    `json:"body_text"`
	IssuerName     string    `json:"issuer_name"`
	SignatureName  string    `json:"signature_name"`
	FooterText     string    `json:"footer_text"`
	UpdatedAt      string    `json:"updated_at"`
}

func toCertificateTemplateResponse(row sqlc.CertificateTemplate) certificateTemplateResponse {
	var logo *string
	if row.LogoUrl.Valid {
		value := row.LogoUrl.String
		logo = &value
	}
	return certificateTemplateResponse{
		ID: row.ID, Name: row.Name, IsActive: row.IsActive,
		PrimaryColor: row.PrimaryColor, SecondaryColor: row.SecondaryColor,
		LogoURL: logo, Title: row.Title, Subtitle: row.Subtitle,
		BodyText: row.BodyText, IssuerName: row.IssuerName,
		SignatureName: row.SignatureName, FooterText: row.FooterText,
		UpdatedAt: row.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *CertificateTemplateHandler) List(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	rows, err := h.queries.ListCertificateTemplates(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca template sijil"})
		return
	}
	result := make([]certificateTemplateResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, toCertificateTemplateResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"templates": result})
}

func (h *CertificateTemplateHandler) Get(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	row, err := h.queries.GetCertificateTemplate(c.Request.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "template sijil tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal baca template sijil"})
		return
	}
	c.JSON(http.StatusOK, toCertificateTemplateResponse(row))
}

type certificateTemplateRequest struct {
	Name           string  `json:"name"`
	PrimaryColor   string  `json:"primary_color"`
	SecondaryColor string  `json:"secondary_color"`
	LogoURL        *string `json:"logo_url"`
	Title          string  `json:"title"`
	Subtitle       string  `json:"subtitle"`
	BodyText       string  `json:"body_text"`
	IssuerName     string  `json:"issuer_name"`
	SignatureName  string  `json:"signature_name"`
	FooterText     string  `json:"footer_text"`
}

func (r certificateTemplateRequest) values() (sqlc.UpdateCertificateTemplateParams, string) {
	fields := map[string]string{
		"nama": r.Name, "tajuk": r.Title, "subtajuk": r.Subtitle,
		"teks utama": r.BodyText, "nama penerbit": r.IssuerName,
		"nama penandatangan": r.SignatureName,
	}
	for label, value := range fields {
		if strings.TrimSpace(value) == "" {
			return sqlc.UpdateCertificateTemplateParams{}, label + " diperlukan"
		}
	}
	if !certificateHexColor.MatchString(r.PrimaryColor) || !certificateHexColor.MatchString(r.SecondaryColor) {
		return sqlc.UpdateCertificateTemplateParams{}, "format warna tidak sah"
	}
	if len(r.Name) > 120 || len(r.Title) > 200 || len(r.BodyText) > 2000 || len(r.FooterText) > 500 {
		return sqlc.UpdateCertificateTemplateParams{}, "teks template terlalu panjang"
	}
	return sqlc.UpdateCertificateTemplateParams{
		Name: r.Name, PrimaryColor: r.PrimaryColor, SecondaryColor: r.SecondaryColor,
		LogoUrl: pgText(strings.TrimSpace(valueOrEmpty(r.LogoURL))),
		Title:   r.Title, Subtitle: r.Subtitle, BodyText: r.BodyText,
		IssuerName: r.IssuerName, SignatureName: r.SignatureName, FooterText: r.FooterText,
	}, ""
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h *CertificateTemplateHandler) Update(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var request certificateTemplateRequest
	if !bindJSON(c, &request) {
		return
	}
	params, validationError := request.values()
	if validationError != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": validationError})
		return
	}
	params.ID = id
	row, err := h.queries.UpdateCertificateTemplate(c.Request.Context(), params)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "template sijil tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal simpan template sijil"})
		return
	}
	c.JSON(http.StatusOK, toCertificateTemplateResponse(row))
}

func (h *CertificateTemplateHandler) Publish(c *gin.Context) {
	if !h.requireManagement(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	row, err := h.queries.PublishCertificateTemplate(c.Request.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "template sijil tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal terbitkan template sijil"})
		return
	}
	c.JSON(http.StatusOK, toCertificateTemplateResponse(row))
}
