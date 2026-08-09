package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/push"
	"marc/internal/storage"
)

type CommentHandler struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	push    *push.Service
	r2      *storage.R2Client
}

func NewCommentHandler(pool *pgxpool.Pool, pushSvc *push.Service, r2 *storage.R2Client) *CommentHandler {
	return &CommentHandler{pool: pool, queries: sqlc.New(pool), push: pushSvc, r2: r2}
}

type createCommentRequest struct {
	Content         string  `json:"content" binding:"required"`
	ParentCommentID *string `json:"parent_comment_id"`
}

// resolveParentCommentID kuatkuasakan cap nested depth 2 (macam Facebook):
// reply kat comment top-level (depth 1) → jadi depth 2 macam biasa. Reply
// kat comment yang DAH depth 2 → "flatten", attach terus kat depth-1
// parent asal (bukan cipta depth 3).
func resolveParentCommentID(ctx context.Context, q *sqlc.Queries, postID, requestedParentID uuid.UUID) (pgtype.UUID, error) {
	parent, err := q.GetCommentByID(ctx, requestedParentID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if parent.PostID != postID {
		return pgtype.UUID{}, fmt.Errorf("parent comment bukan milik post ini")
	}
	if parent.ParentCommentID.Valid {
		return parent.ParentCommentID, nil
	}
	return pgtype.UUID{Bytes: parent.ID, Valid: true}, nil
}

func (h *CommentHandler) Create(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id post tidak sah"})
		return
	}

	var req createCommentRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var parentID pgtype.UUID
	if req.ParentCommentID != nil {
		requestedID, err := uuid.Parse(*req.ParentCommentID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent_comment_id tidak sah"})
			return
		}
		parentID, err = resolveParentCommentID(ctx, h.queries, postID, requestedID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "comment induk tidak dijumpai"})
			return
		}
	}

	comment, err := h.queries.CreateComment(ctx, sqlc.CreateCommentParams{
		PostID:          postID,
		ParentCommentID: parentID,
		AuthorID:        userID,
		Content:         req.Content,
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "post tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal hantar comment"})
		return
	}

	postAuthorID, err := h.queries.GetPostAuthorID(ctx, postID)
	if err == nil {
		notifyOwner(ctx, h.queries, h.push, postAuthorID, userID, "post_comment", pgtype.UUID{Bytes: postID, Valid: true},
			pgtype.UUID{Bytes: comment.ID, Valid: true}, "Comment baru", "Seseorang comment pada post anda")
	}

	author := authorResponse{}
	if profile, err := h.queries.GetProfileByUserID(ctx, userID); err == nil {
		author.MemberID = profile.MemberID
		if profile.DisplayName.Valid {
			s := profile.DisplayName.String
			author.DisplayName = &s
		}
		author.AvatarURL = avatarURLFor(h.r2, profile.AvatarR2Key)
	}

	c.JSON(http.StatusCreated, commentResponse{
		ID:              comment.ID.String(),
		ParentCommentID: nullableUUIDString(comment.ParentCommentID),
		Content:         comment.Content,
		CreatedAt:       formatTime(comment.CreatedAt),
		EditedAt:        formatTimeNullable(comment.EditedAt),
		Author:          author,
		LikeCount:       0,
		LikedByMe:       false,
	})
}

func (h *CommentHandler) List(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id post tidak sah"})
		return
	}

	ctx := c.Request.Context()
	rows, err := h.queries.ListCommentsByPostID(ctx, postID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat comment"})
		return
	}

	if len(rows) == 0 {
		c.JSON(http.StatusOK, gin.H{"comments": []commentResponse{}})
		return
	}

	commentIDs := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		commentIDs[i] = r.ID
	}

	likeCounts, err := h.queries.CountCommentLikesByCommentIDs(ctx, commentIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat comment"})
		return
	}
	likeCountByComment := make(map[uuid.UUID]int64, len(likeCounts))
	for _, lc := range likeCounts {
		likeCountByComment[lc.CommentID] = lc.LikeCount
	}

	likedIDs, err := h.queries.CommentsLikedByUser(ctx, sqlc.CommentsLikedByUserParams{
		UserID: middleware.UserID(c), CommentIds: commentIDs,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat comment"})
		return
	}
	likedByMe := make(map[uuid.UUID]bool, len(likedIDs))
	for _, id := range likedIDs {
		likedByMe[id] = true
	}

	resp := make([]commentResponse, len(rows))
	for i, r := range rows {
		var displayName *string
		if r.AuthorDisplayName.Valid {
			s := r.AuthorDisplayName.String
			displayName = &s
		}
		resp[i] = commentResponse{
			ID:              r.ID.String(),
			ParentCommentID: nullableUUIDString(r.ParentCommentID),
			Content:         r.Content,
			CreatedAt:       formatTime(r.CreatedAt),
			EditedAt:        formatTimeNullable(r.EditedAt),
			Author: authorResponse{
				MemberID:    r.AuthorMemberID,
				DisplayName: displayName,
				AvatarURL:   avatarURLFor(h.r2, r.AuthorAvatarR2Key),
			},
			LikeCount: likeCountByComment[r.ID],
			LikedByMe: likedByMe[r.ID],
		}
	}

	c.JSON(http.StatusOK, gin.H{"comments": resp})
}

type updateCommentRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *CommentHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	var req updateCommentRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	// GetCommentByID (bukan GetCommentAuthorID) — kandungan lama diperlukan
	// untuk jejak audit, dan baris yang sama dah bawa author_id.
	existing, err := h.queries.GetCommentByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
		return
	}
	if existing.AuthorID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pemilik boleh edit comment"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini comment"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	updated, err := q.UpdateComment(ctx, sqlc.UpdateCommentParams{ID: id, Content: req.Content})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini comment"})
		return
	}

	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityComment,
		EntityID:   id,
		Action:     audit.ActionUpdate,
		Actor:      auditActor(c, h.queries),
		Old:        map[string]any{"content": existing.Content},
		New:        map[string]any{"content": updated.Content},
	}); err != nil {
		log.Printf("audit comment update: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini comment"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini comment"})
		return
	}

	c.JSON(http.StatusOK, commentResponse{
		ID:              updated.ID.String(),
		ParentCommentID: nullableUUIDString(updated.ParentCommentID),
		Content:         updated.Content,
		CreatedAt:       formatTime(updated.CreatedAt),
		EditedAt:        formatTimeNullable(updated.EditedAt),
	})
}

func (h *CommentHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	existing, err := h.queries.GetCommentByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
		return
	}

	allowed, err := canModify(ctx, h.queries, userID, existing.AuthorID)
	if err != nil || !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak dibenarkan padam comment ini"})
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam comment"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	if err := q.SoftDeleteComment(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam comment"})
		return
	}

	// Snapshot penuh pada padam — selepas ni kandungan tak dapat dibaca
	// semula melalui API, jadi jejak audit satu-satunya rekod apa yang
	// dibuang dan oleh siapa (management boleh padam comment orang lain).
	if err := audit.Record(ctx, q, audit.Entry{
		EntityType: audit.EntityComment,
		EntityID:   id,
		Action:     audit.ActionDelete,
		Actor:      auditActor(c, h.queries),
		Old: map[string]any{
			"content":   existing.Content,
			"author_id": existing.AuthorID.String(),
			"post_id":   existing.PostID.String(),
		},
	}); err != nil {
		log.Printf("audit comment delete: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam comment"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam comment"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *CommentHandler) Like(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	if err := h.queries.LikeComment(ctx, sqlc.LikeCommentParams{CommentID: id, UserID: userID}); err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal like comment"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *CommentHandler) Unlike(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return
	}

	if err := h.queries.UnlikeComment(c.Request.Context(), sqlc.UnlikeCommentParams{
		CommentID: id, UserID: middleware.UserID(c),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal unlike comment"})
		return
	}

	c.Status(http.StatusNoContent)
}
