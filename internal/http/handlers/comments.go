package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/push"
)

type CommentHandler struct {
	queries *sqlc.Queries
	push    *push.Service
}

func NewCommentHandler(pool *pgxpool.Pool, pushSvc *push.Service) *CommentHandler {
	return &CommentHandler{queries: sqlc.New(pool), push: pushSvc}
}

type createCommentRequest struct {
	Content         string  `json:"content" binding:"required"`
	ParentCommentID *string `json:"parent_comment_id"`
}

// resolveParentCommentID kuatkuasakan cap nested depth 2 (macam Facebook):
// reply kat comment top-level (depth 1) → jadi depth 2 macam biasa. Reply
// kat comment yang DAH depth 2 → "flatten", attach terus kat depth-1
// parent asal (bukan cipta depth 3).
func resolveParentCommentID(ctx context.Context, q *sqlc.Queries, requestedParentID uuid.UUID) (pgtype.UUID, error) {
	parent, err := q.GetCommentByID(ctx, requestedParentID)
	if err != nil {
		return pgtype.UUID{}, err
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
		parentID, err = resolveParentCommentID(ctx, h.queries, requestedID)
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

	authorID, err := h.queries.GetCommentAuthorID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
		return
	}
	if authorID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cuma pemilik boleh edit comment"})
		return
	}

	updated, err := h.queries.UpdateComment(ctx, sqlc.UpdateCommentParams{ID: id, Content: req.Content})
	if err != nil {
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

	authorID, err := h.queries.GetCommentAuthorID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
		return
	}

	allowed, err := canModify(ctx, h.queries, userID, authorID)
	if err != nil || !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "tidak dibenarkan padam comment ini"})
		return
	}

	if err := h.queries.SoftDeleteComment(ctx, id); err != nil {
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
