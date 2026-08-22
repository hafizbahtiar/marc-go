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
	Content         string  `json:"content" binding:"required,max=2000"`
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

	author := h.authorOf(ctx, userID)

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

// authorOf bina blok `author` untuk respons komen tunggal (Create/Update).
//
// Best-effort dengan sengaja: profil yang gagal dibaca memulangkan blok
// kosong dan bukan menggagalkan permintaan — komen SUDAH tersimpan pada
// tahap ni, jadi 500 di sini akan membuat klien fikir suntingannya gagal
// sedangkan ia berjaya.
//
// Dikongsi antara Create dan Update supaya kedua-duanya tak boleh
// terpesong — Update dulu tak mengisi medan ni langsung (L34).
func (h *CommentHandler) authorOf(ctx context.Context, userID uuid.UUID) authorResponse {
	author := authorResponse{}
	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		log.Printf("baca profil %s untuk respons komen: %v", userID, err)
		return author
	}
	author.MemberID = profile.MemberID
	if profile.DisplayName.Valid {
		s := profile.DisplayName.String
		author.DisplayName = &s
	}
	author.AvatarURL = avatarURLFor(ctx, h.r2, profile.AvatarR2Key)
	return author
}

// likeStateOf baca kiraan like + "aku dah like?" untuk SATU komen.
//
// Guna semula query berkumpulan yang sama dengan List (hantar kepingan
// satu elemen) supaya tiada query kedua yang boleh terpesong daripada
// yang dipakai senarai.
func (h *CommentHandler) likeStateOf(ctx context.Context, commentID, viewerID uuid.UUID) (int64, bool) {
	ids := []uuid.UUID{commentID}

	var count int64
	if rows, err := h.queries.CountCommentLikesByCommentIDs(ctx, ids); err != nil {
		log.Printf("kira like komen %s: %v", commentID, err)
	} else if len(rows) > 0 {
		count = rows[0].LikeCount
	}

	liked := false
	if rows, err := h.queries.CommentsLikedByUser(ctx, sqlc.CommentsLikedByUserParams{
		UserID: viewerID, CommentIds: ids,
	}); err != nil {
		log.Printf("semak like komen %s: %v", commentID, err)
	} else {
		liked = len(rows) > 0
	}

	return count, liked
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
				AvatarURL:   avatarURLFor(ctx, h.r2, r.AuthorAvatarR2Key),
			},
			LikeCount: likeCountByComment[r.ID],
			LikedByMe: likedByMe[r.ID],
		}
	}

	c.JSON(http.StatusOK, gin.H{"comments": resp})
}

type updateCommentRequest struct {
	Content string `json:"content" binding:"required,max=2000"`
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
		Actor:      auditActor(c, q),
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

	// Author + kiraan like DIISI (Opus verify 2026-08-22, L34). Sebelum
	// ni respons PATCH tinggalkan ketiga-tiganya pada nilai sifar —
	// `authorResponse` ialah struct NILAI, jadi ia bersiri sebagai
	// {"member_id":"","display_name":null,"avatar_url":null} dan bukan
	// tiada. Klien yang menulis ganti komen dalam senarai daripada
	// respons ni (corak biasa selepas edit) nampak nama, avatar DAN
	// kiraan like penulis lenyap sehingga muat semula.
	//
	// Bentuknya kini padan Create dan List — ketiga-tiga laluan komen
	// memulangkan `commentResponse` yang LENGKAP.
	likeCount, likedByMe := h.likeStateOf(ctx, id, userID)

	c.JSON(http.StatusOK, commentResponse{
		ID:              updated.ID.String(),
		ParentCommentID: nullableUUIDString(updated.ParentCommentID),
		Content:         updated.Content,
		CreatedAt:       formatTime(updated.CreatedAt),
		EditedAt:        formatTimeNullable(updated.EditedAt),
		Author:          h.authorOf(ctx, userID),
		LikeCount:       likeCount,
		LikedByMe:       likedByMe,
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
		Actor:      auditActor(c, q),
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

	rows, err := h.queries.LikeComment(ctx, sqlc.LikeCommentParams{CommentID: id, UserID: userID})
	if err != nil {
		if isForeignKeyViolation(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "comment tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal like comment"})
		return
	}

	// Beritahu penulis komen (L35, keputusan produk 2026-08-22) —
	// padanan gelagat like pada POST, yang sudah memberitahu sejak awal.
	//
	// `rows > 0` WAJIB, bukan kemasan: `LikeComment` ialah `on conflict
	// do nothing`, jadi menghantar like berulang ialah no-op di DB.
	// Memberitahu tanpa syarat menjadikan endpoint ni gelung harassment
	// bersasar — tepat pepijat yang L18 baiki pada laluan post. Rate
	// limiter TIADA pada route like (dedup inilah mekanismenya), jadi
	// guard ni satu-satunya yang menahannya.
	if rows > 0 {
		if comment, err := h.queries.GetCommentByID(ctx, id); err == nil {
			notifyOwner(ctx, h.queries, h.push, comment.AuthorID, userID, "comment_like",
				pgtype.UUID{Bytes: comment.PostID, Valid: true},
				pgtype.UUID{Bytes: comment.ID, Valid: true},
				"Komen anda disukai", "Seseorang menyukai komen anda")
		}
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
