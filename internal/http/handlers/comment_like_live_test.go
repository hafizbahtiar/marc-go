package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/onesignal"
	"marc/internal/push"
	"marc/internal/storage"
)

// L35 — like pada komen memberitahu penulis komen (keputusan produk
// 2026-08-22), dengan guard dedup yang SAMA seperti L18 tegakkan pada
// laluan post.
//
// Route like TIADA rate limiter — dedup inilah mekanismenya. Jadi guard
// `rows > 0` bukan kemasan: tanpanya, menghantar like berulang menjadi
// gelung push bersasar.

func seedPostAndComment(t *testing.T, pool *pgxpool.Pool, authorID uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	var postID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into posts (author_id, type, content) values ($1, 'normal', 'post ujian') returning id`,
		authorID).Scan(&postID); err != nil {
		t.Fatalf("seed post: %v", err)
	}
	var commentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into comments (post_id, author_id, content) values ($1, $2, 'komen ujian') returning id`,
		postID, authorID).Scan(&commentID); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	return postID, commentID
}

func countCommentLikeNotifications(t *testing.T, pool *pgxpool.Pool, recipientID, commentID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from notifications
		 where recipient_id = $1 and comment_id = $2 and type = 'comment_like'`,
		recipientID, commentID).Scan(&n); err != nil {
		t.Fatalf("kira notifikasi: %v", err)
	}
	return n
}

func likeCommentCall(t *testing.T, pool *pgxpool.Pool, commentID, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/comments/"+commentID.String()+"/like", nil)
	c.Params = gin.Params{{Key: "id", Value: commentID.String()}}
	c.Set("userID", userID)

	// Servis push SEBENAR tapi DIMATIKAN (OneSignal tanpa kredential →
	// `Enabled()` false → `NotifyUser` no-op tanpa rangkaian). Menghantar
	// `nil` di sini PANIK dalam `notifyOwner`, yang tak menjaga nil —
	// berbeza daripada `notifyMembers`, yang menjaganya kerana ia
	// berjalan dalam goroutine latar (panik di sana = proses mati, bukan
	// satu permintaan gagal). Ini padanan apa yang produksi bina bila
	// ONESIGNAL_APP_ID kosong.
	pushSvc := push.NewService(sqlc.New(pool), onesignal.NewClient("", ""))
	r2 := storage.NewR2Client("", "", "", "", "")

	NewCommentHandler(pool, pushSvc, r2).Like(c)

	// Laluan bahagia pulang 204 TANPA badan, dan gin menangguhkan
	// `WriteHeader` sehingga sesuatu ditulis — jadi `rec.Code` kekal 200
	// (lalai perakam) melainkan ia dipaksa. Handler lain dalam pakej ni
	// guna `c.JSON`, yang menulis, jadi masalah ni tak muncul di sana.
	c.Writer.WriteHeaderNow()
	return rec
}

func TestLikeCommentMemberitahuPenulis(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	penulis := seedMember(t, ctx, pool, "ahli", "approved")
	peminat := seedMember(t, ctx, pool, "ahli", "approved")
	_, commentID := seedPostAndComment(t, pool, penulis)

	if rec := likeCommentCall(t, pool, commentID, peminat); rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d, mahu 204. Badan: %s", rec.Code, rec.Body.String())
	}

	if got := countCommentLikeNotifications(t, pool, penulis, commentID); got != 1 {
		t.Errorf("notifikasi = %d, mahu 1 — penulis komen tak diberitahu", got)
	}
}

// Guard dedup: like berulang ialah no-op di DB (`on conflict do
// nothing`), jadi ia tak boleh menghasilkan notifikasi kedua.
func TestLikeCommentBerulangTidakSpamNotifikasi(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	penulis := seedMember(t, ctx, pool, "ahli", "approved")
	peminat := seedMember(t, ctx, pool, "ahli", "approved")
	_, commentID := seedPostAndComment(t, pool, penulis)

	for i := 0; i < 5; i++ {
		if rec := likeCommentCall(t, pool, commentID, peminat); rec.Code != http.StatusNoContent {
			t.Fatalf("like #%d: kod = %d", i+1, rec.Code)
		}
	}

	if got := countCommentLikeNotifications(t, pool, penulis, commentID); got != 1 {
		t.Errorf("notifikasi = %d selepas 5 like, mahu 1 — endpoint ni gelung "+
			"harassment bersasar (tiada rate limiter pada route like; dedup "+
			"inilah mekanismenya)", got)
	}
}

// Like pada komen SENDIRI tak memberitahu sesiapa — padanan notifyOwner
// pada laluan post.
func TestLikeKomenSendiriTidakMemberitahu(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	penulis := seedMember(t, ctx, pool, "ahli", "approved")
	_, commentID := seedPostAndComment(t, pool, penulis)

	if rec := likeCommentCall(t, pool, commentID, penulis); rec.Code != http.StatusNoContent {
		t.Fatalf("kod = %d", rec.Code)
	}

	if got := countCommentLikeNotifications(t, pool, penulis, commentID); got != 0 {
		t.Errorf("notifikasi = %d — ahli diberitahu tentang likenya sendiri", got)
	}
}

// `LikeComment` mesti `:execrows`. Kalau ia kembali kepada `:exec`,
// handler tak dapat membezakan insert baharu drpd conflict dan guard
// dedup runtuh secara senyap.
func TestLikeCommentPulangkanBilanganBaris(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	penulis := seedMember(t, ctx, pool, "ahli", "approved")
	peminat := seedMember(t, ctx, pool, "ahli", "approved")
	_, commentID := seedPostAndComment(t, pool, penulis)

	q := sqlc.New(pool)
	params := sqlc.LikeCommentParams{CommentID: commentID, UserID: peminat}

	pertama, err := q.LikeComment(ctx, params)
	if err != nil {
		t.Fatalf("LikeComment (pertama): %v", err)
	}
	if pertama != 1 {
		t.Errorf("baris (pertama) = %d, mahu 1", pertama)
	}

	kedua, err := q.LikeComment(ctx, params)
	if err != nil {
		t.Fatalf("LikeComment (kedua): %v", err)
	}
	if kedua != 0 {
		t.Errorf("baris (kedua) = %d, mahu 0 — `on conflict do nothing` tak "+
			"lagi dilaporkan, guard dedup handler jadi buta", kedua)
	}
}
