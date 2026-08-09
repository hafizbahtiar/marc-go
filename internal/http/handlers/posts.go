package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/push"
	"marc/internal/storage"
)

type PostHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	r2      *storage.R2Client
	push    *push.Service
}

func NewPostHandler(pool *pgxpool.Pool, r2 *storage.R2Client, pushSvc *push.Service) *PostHandler {
	return &PostHandler{
		pool:    pool,
		queries: sqlc.New(pool),
		r2:      r2,
		push:    pushSvc,
	}
}

type createPostRequest struct {
	Type    string   `json:"type"`
	Content string   `json:"content" binding:"required"`
	R2Keys  []string `json:"r2_keys"`
}

func (h *PostHandler) Create(c *gin.Context) {
	var req createPostRequest
	if !bindJSON(c, &req) {
		return
	}

	postType := req.Type
	if postType == "" {
		postType = "normal"
	}
	if postType != "normal" && postType != "announcement" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jenis post tidak sah"})
		return
	}
	if len(req.R2Keys) > storage.MaxImagesPerPost {
		c.JSON(http.StatusBadRequest, gin.H{"error": "maksimum 4 gambar setiap post"})
		return
	}

	userID := middleware.UserID(c)
	ctx := c.Request.Context()

	if postType == "announcement" {
		isManagement, err := authz.IsManagement(ctx, h.queries, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta post"})
			return
		}
		if !isManagement {
			c.JSON(http.StatusForbidden, gin.H{"error": "cuma management boleh buat pengumuman"})
			return
		}
	}

	// R2 tak support content-length-range di presign PUT (verified: 501
	// "Presigned post requests are not yet implemented") — jadi had saiz
	// dikuatkuasakan DI SINI, lepas upload siap, sebelum r2_key diterima
	// masuk post. Gambar yang lebih besar dibuang dari R2 terus (elak
	// orphan storage).
	for _, key := range req.R2Keys {
		owned, err := h.queries.IsPendingUploadOwnedByUser(ctx, sqlc.IsPendingUploadOwnedByUserParams{
			R2Key: key, UserID: userID,
		})
		if err != nil || !owned {
			c.JSON(http.StatusBadRequest, gin.H{"error": "gambar tidak sah atau belum diupload"})
			return
		}

		if err := h.r2.VerifyImageSize(ctx, key); err != nil {
			_ = h.r2.DeleteImage(ctx, key)
			_ = h.queries.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID})
			if errors.Is(err, storage.ErrImageTooLarge) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "gambar melebihi had 5MB"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "gambar tidak sah atau belum diupload"})
			return
		}

		if err := h.r2.VerifyImageFormat(ctx, key); err != nil {
			// Log ralat SEBENAR sebelum ditukar jadi 400 generik. Dua punca
			// yang berbeza sama sekali runtuh jadi mesej yang sama di sini:
			// gambar betul-betul rosak/tak diupload, ATAU R2 tolak GetObject
			// kita dengan AccessDenied (skop token salah). Tanpa baris ni,
			// masalah kredential nampak macam masalah fail pengguna.
			log.Printf("verify gambar gagal (r2_key=%s, user=%s): %v", key, userID, err)
			_ = h.r2.DeleteImage(ctx, key)
			_ = h.queries.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID})
			c.JSON(http.StatusBadRequest, gin.H{"error": "gambar tidak sah atau belum diupload"})
			return
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta post"})
		return
	}
	defer tx.Rollback(ctx)

	q := h.queries.WithTx(tx)

	post, err := q.CreatePost(ctx, sqlc.CreatePostParams{
		AuthorID: userID,
		Type:     postType,
		Content:  req.Content,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta post"})
		return
	}

	for i, key := range req.R2Keys {
		if _, err := q.CreatePostImage(ctx, sqlc.CreatePostImageParams{
			PostID:   post.ID,
			R2Key:    key,
			Position: int16(i),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta post"})
			return
		}

		// Padam tracking row DALAM transaksi yang sama — kalau
		// CreatePost/CreatePostImage/Commit gagal selepas ni dan tx
		// rollback, row pending_uploads ni SELAMAT (delete tak commit),
		// jadi client boleh retry POST /posts dengan r2_keys yang sama
		// tanpa kena "gambar tidak sah" sedangkan gambar tu still ada
		// dan sah kepunyaan dia (lihat Fix H2 follow-up, audit 2026-08-07).
		// Best-effort — kegagalan padam row ni tak patut gagalkan post
		// yang dah berjaya dicipta (row lingering harmless, cuma
		// tracking stale untuk key yang dah attached).
		_ = q.DeletePendingUpload(ctx, sqlc.DeletePendingUploadParams{R2Key: key, UserID: userID})
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta post"})
		return
	}

	full, err := h.queries.GetPostByID(ctx, post.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "post dicipta tapi gagal muat semula"})
		return
	}

	resp, err := h.buildPostResponses(ctx, userID, []postCore{coreFromGetPostByIDRow(full)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "post dicipta tapi gagal muat semula"})
		return
	}

	c.JSON(http.StatusCreated, resp[0])
}

func (h *PostHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	row, err := h.queries.GetPostByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post tidak dijumpai"})
		return
	}

	resp, err := h.buildPostResponses(ctx, middleware.UserID(c), []postCore{coreFromGetPostByIDRow(row)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat post"})
		return
	}

	c.JSON(http.StatusOK, resp[0])
}

func (h *PostHandler) List(c *gin.Context) {
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

	ctx := c.Request.Context()
	rows, err := h.queries.ListPosts(ctx, sqlc.ListPostsParams{
		CursorCreatedAt: cursorCreatedAt,
		CursorID:        cursorID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat feed"})
		return
	}

	cores := make([]postCore, len(rows))
	for i, r := range rows {
		cores[i] = coreFromListPostsRow(r)
	}

	resp, err := h.buildPostResponses(ctx, middleware.UserID(c), cores)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat feed"})
		return
	}

	var nextCursor *string
	if len(resp) == limit {
		last := cores[len(cores)-1]
		s := encodeCursor(last.CreatedAt.Time, last.ID)
		nextCursor = &s
	}

	c.JSON(http.StatusOK, gin.H{"posts": resp, "next_cursor": nextCursor})
}

type updatePostRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *PostHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	var req updatePostRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	before, err := h.queries.GetPostByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post tidak dijumpai"})
		return
	}
	if before.AuthorID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pemilik boleh edit post"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini post"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	updated, err := q.UpdatePost(ctx, sqlc.UpdatePostParams{ID: id, Content: req.Content})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini post"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityPost,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"content": before.Content},
		New:        map[string]any{"content": updated.Content},
	}); err != nil {
		log.Printf("audit post update: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini post"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini post"})
		return
	}

	full, err := h.queries.GetPostByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat semula post"})
		return
	}

	resp, err := h.buildPostResponses(ctx, userID, []postCore{coreFromGetPostByIDRow(full)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat semula post"})
		return
	}

	c.JSON(http.StatusOK, resp[0])
}

func (h *PostHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	before, err := h.queries.GetPostByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post tidak dijumpai"})
		return
	}

	allowed, err := canModify(ctx, h.queries, userID, before.AuthorID)
	if err != nil || !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak dibenarkan padam post ini"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam post"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	if err := q.SoftDeletePost(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam post"})
		return
	}

	// Snapshot penuh — management boleh padam post orang lain, jadi ini
	// satu-satunya rekod kekal tentang apa yang dibuang.
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityPost,
		EntityID:   id,
		Action:     audit.ActionDelete,
		Actor:      auditActor(c, h.queries),
		Old: map[string]any{
			"content":   before.Content,
			"type":      before.Type,
			"author_id": before.AuthorID.String(),
		},
	}); err != nil {
		log.Printf("audit post delete: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam post"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam post"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *PostHandler) Like(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	if err := h.queries.LikePost(ctx, sqlc.LikePostParams{PostID: id, UserID: userID}); err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "post tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal like post"})
		return
	}

	authorID, err := h.queries.GetPostAuthorID(ctx, id)
	if err == nil {
		notifyOwner(ctx, h.queries, h.push, authorID, userID, "post_like", pgtype.UUID{Bytes: id, Valid: true}, pgtype.UUID{}, "Post anda disukai", "Seseorang menyukai post anda")
	}

	c.Status(http.StatusNoContent)
}

func (h *PostHandler) Unlike(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	if err := h.queries.UnlikePost(c.Request.Context(), sqlc.UnlikePostParams{
		PostID: id, UserID: middleware.UserID(c),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal unlike post"})
		return
	}

	c.Status(http.StatusNoContent)
}
