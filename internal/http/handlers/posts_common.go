package handlers

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/push"
)

const defaultPageLimit = 20

type authorResponse struct {
	MemberID    string  `json:"member_id"`
	DisplayName *string `json:"display_name"`
}

type postResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Content      string         `json:"content"`
	CreatedAt    string         `json:"created_at"`
	EditedAt     *string        `json:"edited_at"`
	Author       authorResponse `json:"author"`
	Images       []string       `json:"images"`
	LikeCount    int64          `json:"like_count"`
	CommentCount int64          `json:"comment_count"`
	LikedByMe    bool           `json:"liked_by_me"`
}

type commentResponse struct {
	ID              string         `json:"id"`
	ParentCommentID *string        `json:"parent_comment_id"`
	Content         string         `json:"content"`
	CreatedAt       string         `json:"created_at"`
	EditedAt        *string        `json:"edited_at"`
	Author          authorResponse `json:"author"`
	LikeCount       int64          `json:"like_count"`
	LikedByMe       bool           `json:"liked_by_me"`
}

func formatTime(t pgtype.Timestamptz) string {
	return t.Time.Format(time.RFC3339)
}

func formatTimeNullable(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format(time.RFC3339)
	return &s
}

func nullableUUIDString(id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	s := uuid.UUID(id.Bytes).String()
	return &s
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// canModify — pattern ownership (Stage 3) + moderation (Stage 10): pemilik
// resource sendiri, ATAU management, boleh edit/padam.
func canModify(ctx context.Context, q *sqlc.Queries, userID, resourceAuthorID uuid.UUID) (bool, error) {
	if userID == resourceAuthorID {
		return true, nil
	}
	return authz.IsManagement(ctx, q, userID)
}

// notifyOwner rekod notification dalam DB + hantar push, untuk like/comment
// pada content sendiri (bukan self-notify kalau actor == recipient — cth
// like post sendiri). Kegagalan sini tak patut gagalkan request utama
// (like/comment dah berjaya di DB), so cuma log.
func notifyOwner(
	ctx context.Context,
	q *sqlc.Queries,
	pushSvc *push.Service,
	recipientID, actorID uuid.UUID,
	notifType string,
	postID pgtype.UUID,
	commentID pgtype.UUID,
	title, message string,
) {
	if recipientID == actorID {
		return
	}

	if _, err := q.CreateNotification(ctx, sqlc.CreateNotificationParams{
		RecipientID: recipientID,
		ActorID:     actorID,
		Type:        notifType,
		PostID:      postID,
		CommentID:   commentID,
	}); err != nil {
		log.Printf("gagal cipta notification: %v", err)
	}

	if err := pushSvc.NotifyUser(ctx, recipientID, title, message); err != nil {
		log.Printf("gagal hantar push notification: %v", err)
	}
}

// postCore — field sepunya antara GetPostByIDRow dan ListPostsRow (dua
// sqlc row type berlainan tapi shape sama), supaya buildPostResponses
// boleh kongsi logic untuk single post & list.
type postCore struct {
	ID                uuid.UUID
	AuthorID          uuid.UUID
	Type              string
	Content           string
	CreatedAt         pgtype.Timestamptz
	EditedAt          pgtype.Timestamptz
	AuthorMemberID    string
	AuthorDisplayName pgtype.Text
}

func coreFromGetPostByIDRow(r sqlc.GetPostByIDRow) postCore {
	return postCore{
		ID: r.ID, AuthorID: r.AuthorID, Type: r.Type, Content: r.Content,
		CreatedAt: r.CreatedAt, EditedAt: r.EditedAt,
		AuthorMemberID: r.AuthorMemberID, AuthorDisplayName: r.AuthorDisplayName,
	}
}

func coreFromListPostsRow(r sqlc.ListPostsRow) postCore {
	return postCore{
		ID: r.ID, AuthorID: r.AuthorID, Type: r.Type, Content: r.Content,
		CreatedAt: r.CreatedAt, EditedAt: r.EditedAt,
		AuthorMemberID: r.AuthorMemberID, AuthorDisplayName: r.AuthorDisplayName,
	}
}

// buildPostResponses batch semua data tambahan (like count, comment count,
// liked-by-me, images) untuk senarai post sekali gus — elak N+1 query.
func (h *PostHandler) buildPostResponses(ctx context.Context, viewerID uuid.UUID, cores []postCore) ([]postResponse, error) {
	if len(cores) == 0 {
		return []postResponse{}, nil
	}

	postIDs := make([]uuid.UUID, len(cores))
	for i, c := range cores {
		postIDs[i] = c.ID
	}

	likeCounts, err := h.queries.CountPostLikesByPostIDs(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	likeCountByPost := make(map[uuid.UUID]int64, len(likeCounts))
	for _, lc := range likeCounts {
		likeCountByPost[lc.PostID] = lc.LikeCount
	}

	commentCounts, err := h.queries.CountCommentsByPostIDs(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	commentCountByPost := make(map[uuid.UUID]int64, len(commentCounts))
	for _, cc := range commentCounts {
		commentCountByPost[cc.PostID] = cc.CommentCount
	}

	likedPostIDs, err := h.queries.PostsLikedByUser(ctx, sqlc.PostsLikedByUserParams{
		UserID: viewerID, PostIds: postIDs,
	})
	if err != nil {
		return nil, err
	}
	likedByMe := make(map[uuid.UUID]bool, len(likedPostIDs))
	for _, id := range likedPostIDs {
		likedByMe[id] = true
	}

	images, err := h.queries.ListPostImagesByPostIDs(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	imagesByPost := make(map[uuid.UUID][]string)
	for _, img := range images {
		url := h.r2.PublicURL(img.R2Key)
		imagesByPost[img.PostID] = append(imagesByPost[img.PostID], url)
	}

	responses := make([]postResponse, len(cores))
	for i, c := range cores {
		var displayName *string
		if c.AuthorDisplayName.Valid {
			s := c.AuthorDisplayName.String
			displayName = &s
		}
		images := imagesByPost[c.ID]
		if images == nil {
			images = []string{}
		}
		responses[i] = postResponse{
			ID:        c.ID.String(),
			Type:      c.Type,
			Content:   c.Content,
			CreatedAt: formatTime(c.CreatedAt),
			EditedAt:  formatTimeNullable(c.EditedAt),
			Author: authorResponse{
				MemberID:    c.AuthorMemberID,
				DisplayName: displayName,
			},
			Images:       images,
			LikeCount:    likeCountByPost[c.ID],
			CommentCount: commentCountByPost[c.ID],
			LikedByMe:    likedByMe[c.ID],
		}
	}

	return responses, nil
}
