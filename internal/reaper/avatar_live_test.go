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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"marc/internal/db"
	"marc/internal/db/sqlc"
	"marc/internal/storage"
)

// Bukti hujung-ke-hujung bahawa menukar avatar betul-betul MEMBEBASKAN
// storan: objek lama mesti hilang dari bucket, bukan sekadar ada baris
// dalam gilir. Baris gilir tanpa pemadaman sebenar ialah kebocoran yang
// nampak macam dah selesai.
func TestAvatarLamaBetulBetulDipadamDariR2(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" || os.Getenv("REAPER_TEST_DB") == "" {
		t.Skip("perlukan R2_LIVE_TEST=1 + REAPER_TEST_DB")
	}
	_ = godotenv.Load("../../.env")
	if err := db.Migrate(os.Getenv("REAPER_TEST_DB")); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("REAPER_TEST_DB"))
	if err != nil {
		t.Fatal(err)
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

	oldKey := "posts/_avatar-lama-" + uuid.NewString() + ".jpg"
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	client, bucket := storage.ExportedClientForTest(r2)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(oldKey),
		Body: bytes.NewReader(jpeg), ContentType: aws.String("image/jpeg"),
	}); err != nil {
		t.Fatalf("upload avatar lama: %v", err)
	}
	t.Cleanup(func() { _ = r2.DeleteImage(ctx, oldKey) })

	// Persis apa yang applyAvatar tulis bila avatar DIGANTI.
	if err := q.EnqueueDeletedUpload(ctx, sqlc.EnqueueDeletedUploadParams{
		R2Key: oldKey, Reason: "avatar_replaced",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	New(q, r2, time.Minute).RunOnce(ctx)

	if err := r2.VerifyImageFormat(ctx, oldKey); err == nil {
		t.Fatal("avatar lama MASIH dalam R2 — storan bocor setiap kali tukar gambar")
	}

	// Gilir mesti ditanda selesai, bukan dicuba berulang selamanya.
	var pending int
	if err := pool.QueryRow(ctx,
		`select count(*) from deleted_uploads where r2_key = $1 and deleted_at is null`,
		oldKey).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("baris gilir masih tertunggak selepas berjaya dipadam")
	}
}
