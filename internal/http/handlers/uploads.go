package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"marc/internal/storage"
)

type UploadHandler struct {
	r2 *storage.R2Client
}

func NewUploadHandler(r2 *storage.R2Client) *UploadHandler {
	return &UploadHandler{r2: r2}
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana upload URL"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"upload_url": uploadURL, "r2_key": key})
}
