package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/storage"
)

type UploadHandler struct {
	r2      *storage.R2Client
	queries *sqlc.Queries
}

func NewUploadHandler(pool *pgxpool.Pool, r2 *storage.R2Client) *UploadHandler {
	return &UploadHandler{r2: r2, queries: sqlc.New(pool)}
}

var allowedImageContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

type presignRequest struct {
	ContentType string `json:"content_type" binding:"required"`
}

// Presign jana URL upload presigned R2 — client upload gambar TERUS ke R2
// guna URL ni (bukan melalui backend Go), lepas tu attach r2_key
// dipulangkan ke POST /posts.
func (h *UploadHandler) Presign(c *gin.Context) {
	if !h.r2.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "upload gambar belum tersedia"})
		return
	}

	var req presignRequest
	if !bindJSON(c, &req) {
		return
	}

	if !allowedImageContentTypes[req.ContentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jenis fail tidak disokong"})
		return
	}

	uploadURL, key, err := h.r2.PresignUpload(c.Request.Context(), req.ContentType)
	if err != nil {
		log.Printf("presign R2 gagal (content_type=%s): %v", req.ContentType, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana upload URL"})
		return
	}

	if err := h.queries.CreatePendingUpload(c.Request.Context(), sqlc.CreatePendingUploadParams{
		R2Key:  key,
		UserID: middleware.UserID(c),
	}); err != nil {
		log.Printf("simpan pending upload gagal (r2_key=%s): %v", key, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana upload URL"})
		return
	}

	// Peringkat seterusnya (PUT ke R2) berlaku TERUS dari peranti ke
	// Cloudflare — ia tak melalui server ni langsung. Jadi baris ni ialah
	// jejak sisi-server terakhir sebelum upload hilang dari pandangan kita:
	// kalau ia ada tapi post tak pernah sampai, kegagalan itu antara
	// peranti dan R2.
	log.Printf("presign dikeluarkan (r2_key=%s, user=%s, content_type=%s)", key, middleware.UserID(c), req.ContentType)

	c.JSON(http.StatusOK, gin.H{"upload_url": uploadURL, "r2_key": key})
}
