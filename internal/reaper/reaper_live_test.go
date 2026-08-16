package reaper

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/storage"
)

// Ujian hujung-ke-hujung terhadap R2 SEBENAR + Postgres tempatan.
// Dilangkau melainkan R2_LIVE_TEST=1:
//
//	R2_LIVE_TEST=1 REAPER_TEST_DB="postgres://localhost:5432/marc_reaper_check?sslmode=disable" \
//	  go test ./internal/reaper/ -v
//
// Kenapa lawan infra sebenar: keseluruhan tujuan reaper ialah bait
// benar-benar hilang dari bucket. Mock akan sahkan kita panggil
// DeleteObject, bukan bahawa storan betul-betul dituntut semula.
func TestReaperLive(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("set R2_LIVE_TEST=1 untuk jalankan lawan R2 + Postgres sebenar")
	}
	_ = godotenv.Load("../../.env")

	dbURL := os.Getenv("REAPER_TEST_DB")
	if dbURL == "" {
		t.Skip("set REAPER_TEST_DB kepada DB buangan (JANGAN guna DB dev sebenar)")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := sqlc.New(pool)

	r2 := storage.NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r2.Enabled() {
		t.Fatal("R2 tak dikonfigur")
	}

	key := "posts/_reaper-test-" + uuid.NewString() + ".jpg"
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	putProbe(t, r2, key, jpeg)
	t.Cleanup(func() { _ = r2.DeleteImage(ctx, key) })

	if err := q.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
		R2Key: key, Reason: "post_deleted",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	New(q, r2, time.Minute).RunOnce(ctx)

	// Objek betul-betul hilang dari bucket?
	if err := r2.VerifyImageFormat(ctx, key); err == nil {
		t.Fatal("objek MASIH ada dalam R2 selepas reaper berjalan")
	}

	// Baris gilir dibersihkan?
	left, err := q.ListDueDeletedUploads(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, l := range left {
		if l.R2Key == key {
			t.Fatalf("baris gilir untuk %s masih ada (attempts=%d, err=%s)",
				key, l.Attempts, l.LastError.String)
		}
	}
}

// Pending upload yang ditinggalkan kena disapu ikut umur — punca bocor
// kedua, berasingan daripada padam post.
func TestReaperSweepsAbandonedUploads(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" || os.Getenv("REAPER_TEST_DB") == "" {
		t.Skip("perlukan R2_LIVE_TEST=1 + REAPER_TEST_DB")
	}
	_ = godotenv.Load("../../.env")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("REAPER_TEST_DB"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := sqlc.New(pool)

	r2 := storage.NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)

	userID := seedUser(t, ctx, pool)
	key := "posts/_reaper-abandoned-" + uuid.NewString() + ".jpg"
	putProbe(t, r2, key, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0})
	t.Cleanup(func() { _ = r2.DeleteImage(ctx, key) })

	if err := q.CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: userID,
	}); err != nil {
		t.Fatalf("pending upload: %v", err)
	}
	// Tuakan baris melepasi ambang ditinggalkan.
	if _, err := pool.Exec(ctx,
		`update pending_uploads set created_at = now() - interval '7 hours' where r2_key = $1`,
		key); err != nil {
		t.Fatalf("age row: %v", err)
	}

	New(q, r2, time.Minute).RunOnce(ctx)

	if err := r2.VerifyImageFormat(ctx, key); err == nil {
		t.Fatal("upload ditinggalkan MASIH ada dalam R2")
	}
}

// Upload BARU tak boleh disapu — pengguna mungkin masih mengarang.
func TestReaperTidakSapuUploadBaharu(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" || os.Getenv("REAPER_TEST_DB") == "" {
		t.Skip("perlukan R2_LIVE_TEST=1 + REAPER_TEST_DB")
	}
	_ = godotenv.Load("../../.env")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("REAPER_TEST_DB"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := sqlc.New(pool)

	r2 := storage.NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)

	userID := seedUser(t, ctx, pool)
	key := "posts/_reaper-fresh-" + uuid.NewString() + ".jpg"
	putProbe(t, r2, key, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0})
	t.Cleanup(func() { _ = r2.DeleteImage(ctx, key) })

	if err := q.CreatePendingUpload(ctx, sqlc.CreatePendingUploadParams{
		R2Key: key, UserID: userID,
	}); err != nil {
		t.Fatalf("pending upload: %v", err)
	}

	New(q, r2, time.Minute).RunOnce(ctx)

	if err := r2.VerifyImageFormat(ctx, key); err != nil {
		t.Fatalf("upload BAHARU disapu — pengguna yang masih mengarang akan hilang gambar: %v", err)
	}
}

// Post yang dipadam SEBELUM gilir wujud (baris post_images ada, tiada
// baris deleted_uploads) mesti masih dituntut semula — inilah yang
// membersihkan sampah sedia ada dalam bucket.
func TestReaperTuntutPostDipadamLama(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" || os.Getenv("REAPER_TEST_DB") == "" {
		t.Skip("perlukan R2_LIVE_TEST=1 + REAPER_TEST_DB")
	}
	_ = godotenv.Load("../../.env")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("REAPER_TEST_DB"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := sqlc.New(pool)

	r2 := storage.NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)

	userID := seedUser(t, ctx, pool)
	key := "posts/_reaper-legacy-" + uuid.NewString() + ".jpg"
	putProbe(t, r2, key, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0})
	t.Cleanup(func() { _ = r2.DeleteImage(ctx, key) })

	// Post yang dah di-soft-delete, dengan gambar dilekatkan, tapi TIADA
	// baris gilir — persis keadaan sebelum perubahan ni.
	var postID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into posts (author_id, type, content, deleted_at)
		 values ($1, 'normal', 'post lama', now()) returning id`, userID).Scan(&postID); err != nil {
		t.Fatalf("seed post: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into post_images (post_id, r2_key, "position") values ($1, $2, 0)`,
		postID, key); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	New(q, r2, time.Minute).RunOnce(ctx)

	if err := r2.VerifyImageFormat(ctx, key); err == nil {
		t.Fatal("gambar post yang dipadam MASIH ada dalam R2")
	}

	// Pusingan kedua tak boleh menggilir semula kunci yang sama (batu nisan).
	New(q, r2, time.Minute).RunOnce(ctx)
	var pending int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and deleted_at is null`,
		key).Scan(&pending); err != nil {
		t.Fatalf("count: %v", err)
	}
	if pending != 0 {
		t.Fatalf("kunci digilir semula selepas berjaya dipadam — akan berulang selamanya")
	}
}

func seedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"reaper-"+uuid.NewString()+"@test.local").Scan(&id)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func putProbe(t *testing.T, r2 *storage.R2Client, key string, body []byte) {
	t.Helper()
	client, bucket := storage.ExportedClientForTest(r2)
	if _, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentType: aws.String("image/jpeg"),
	}); err != nil {
		t.Fatalf("upload probe: %v", err)
	}
}

var _ = pgtype.Timestamptz{}
