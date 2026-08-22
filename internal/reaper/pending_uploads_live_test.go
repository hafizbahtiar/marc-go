package reaper

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/storage"
)

// Ujian invarian L28: `pending_uploads` ialah senarai PADAM, jadi apa-apa
// kunci yang MASIH dirujuk oleh post atau profil tak boleh muncul di
// dalamnya sebagai calon sapuan.
//
// Berbeza daripada ujian live lain dalam pakej ni, ujian di sini perlukan
// **Postgres SAHAJA** — bukan R2. Yang diuji ialah predikat SQL
// (`ListStalePendingUploads`), bukan sama ada bait benar-benar hilang dari
// bucket, jadi tiada sebab untuk menuntut kredential R2 dan menyempitkan
// siapa yang boleh menjalankannya:
//
//	REAPER_TEST_DB="postgres://localhost:5432/marc_reaper_check?sslmode=disable" \
//	  go test ./internal/reaper/ -run TestReaperPending -v
//
// R2Client sengaja dibina TIDAK dikonfigur: `DeleteImage` pulang nil bila
// begitu, jadi `drainDeleteQueue` berjalan tanpa rangkaian dan penegasan
// di bawah kekal mengenai baris DB.
func pendingTestPool(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, *storage.R2Client) {
	t.Helper()
	_ = godotenv.Load("../../.env")

	dbURL := os.Getenv("REAPER_TEST_DB")
	if dbURL == "" {
		t.Skip("set REAPER_TEST_DB kepada DB buangan (JANGAN guna DB dev sebenar)")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool, sqlc.New(pool), storage.NewR2Client("", "", "", "", "")
}

// seedAgedPendingUpload cipta baris pending_uploads yang sudah melepasi
// ambang "ditinggalkan" — mensimulasikan DELETE yang gagal semasa post
// dicipta (punca L28).
func seedAgedPendingUpload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *sqlc.Queries, key string, userID uuid.UUID) {
	t.Helper()
	if err := q.CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{R2Key: key, UserID: userID}); err != nil {
		t.Fatalf("pending upload: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`update pending_uploads set created_at = now() - interval '7 hours' where r2_key = $1`,
		key); err != nil {
		t.Fatalf("age row: %v", err)
	}
}

func enqueuedCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("count deleted_uploads: %v", err)
	}
	return n
}

// Kunci yang MASIH dilekatkan pada post tak boleh disapu, walaupun baris
// pending_uploadsnya tertinggal dan sudah tua.
//
// Ini kes L28 tepat-tepat: sebelum pembaikan, `ListStalePendingUploads`
// menapis ikut umur SAHAJA, jadi satu `DeletePendingUpload` yang gagal
// semasa post dicipta bermakna gambar post yang MASIH dipaparkan dipadam
// dari R2 enam jam kemudian — kekal, tanpa ralat di mana-mana.
func TestReaperPendingTidakSapuGambarPostHidup(t *testing.T) {
	ctx := context.Background()
	pool, q, r2 := pendingTestPool(t)

	userID := seedUser(t, ctx, pool)
	key := "posts/_l28-attached-" + uuid.NewString() + ".jpg"

	var postID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into posts (author_id, type, content) values ($1, 'normal', 'post hidup') returning id`,
		userID).Scan(&postID); err != nil {
		t.Fatalf("seed post: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into post_images (post_id, r2_key, "position") values ($1, $2, 0)`,
		postID, key); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	// Baris tracking yang SEPATUTNYA dibuang semasa post dicipta, tapi
	// tertinggal kerana DELETEnya gagal.
	seedAgedPendingUpload(t, ctx, pool, q, key, userID)

	New(q, r2, time.Minute).RunOnce(ctx)

	if n := enqueuedCount(t, ctx, pool, key); n != 0 {
		t.Fatalf("gambar post HIDUP digilir untuk dipadam (%d baris deleted_uploads) — "+
			"post yang dipaparkan akan kehilangan gambarnya secara kekal", n)
	}
}

// Padanan kes di atas untuk avatar: kunci yang dirujuk profiles.avatar_r2_key
// tak boleh disapu.
func TestReaperPendingTidakSapuAvatarHidup(t *testing.T) {
	ctx := context.Background()
	pool, q, r2 := pendingTestPool(t)

	userID := seedUser(t, ctx, pool)
	key := "posts/_l28-avatar-" + uuid.NewString() + ".jpg"

	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id, avatar_r2_key)
		 values ($1, $2, (select id from roles where key = 'ahli'), $3)`,
		userID, "L28/"+uuid.NewString()[:8], key); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	seedAgedPendingUpload(t, ctx, pool, q, key, userID)

	New(q, r2, time.Minute).RunOnce(ctx)

	if n := enqueuedCount(t, ctx, pool, key); n != 0 {
		t.Fatalf("avatar HIDUP digilir untuk dipadam (%d baris deleted_uploads)", n)
	}
}

// Sisi yang bertentangan — pembaikan L28 tak boleh mematikan sapuan itu
// sendiri. Kunci yang benar-benar yatim (tiada post, tiada profil) MESTI
// masih disapu, kalau tidak karangan yang ditinggalkan bocor selamanya
// dan seluruh tujuan reaper hilang.
func TestReaperPendingMasihSapuYatimSebenar(t *testing.T) {
	ctx := context.Background()
	pool, q, r2 := pendingTestPool(t)

	userID := seedUser(t, ctx, pool)
	key := "posts/_l28-orphan-" + uuid.NewString() + ".jpg"

	seedAgedPendingUpload(t, ctx, pool, q, key, userID)

	New(q, r2, time.Minute).RunOnce(ctx)

	if n := enqueuedCount(t, ctx, pool, key); n == 0 {
		t.Fatal("upload yatim TIDAK disapu — pembaikan L28 terlebih ketat, " +
			"karangan yang ditinggalkan akan bocor selamanya")
	}

	// Baris pending juga dibuang, kalau tidak ia digilir semula setiap
	// pusingan selama-lamanya.
	var left int
	if err := pool.QueryRow(ctx,
		`select count(*) from pending_uploads where r2_key = $1`, key).Scan(&left); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if left != 0 {
		t.Fatal("baris pending_uploads kekal selepas disapu — akan digilir semula setiap pusingan")
	}
}
