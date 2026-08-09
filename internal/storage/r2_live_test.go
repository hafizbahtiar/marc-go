package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/joho/godotenv"
)

// Ujian diagnostik terhadap bucket R2 SEBENAR. Dilangkau melainkan
// R2_LIVE_TEST=1, jadi ia tak pernah jalan dalam CI atau `go test ./...`
// biasa:
//
//	R2_LIVE_TEST=1 go test ./internal/storage/ -run TestR2LivePermissions -v
//
// Kenapa wujud: 403 AccessDenied daripada R2 nampak SERUPA sama ada
// puncanya presign yang salah bentuk atau token yang tiada kebenaran
// tulis. Ujian ni pisahkan dua-duanya dalam beberapa saat — pada
// 2026-08-09 ia buktikan token boleh List tapi tak boleh Put, iaitu
// masalah skop token Cloudflare dan bukan bug kod.
func TestR2LivePermissions(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("set R2_LIVE_TEST=1 untuk jalankan ujian terhadap R2 sebenar")
	}
	_ = godotenv.Load("../../.env")

	r := NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r.Enabled() {
		t.Fatal("kredential R2 tak lengkap dalam .env")
	}

	ctx := context.Background()
	key := "posts/_live-test-probe.jpg"
	// Header JPEG minimum supaya VerifyImageFormat lulus.
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 0, 0, 0, 1}

	// Cleanup didaftarkan pada t INDUK, bukan dalam subtest yang menulis:
	// t.Cleanup dalam t.Run berjalan sebaik subtest itu tamat, jadi objek
	// akan dipadam sebelum subtest verify sempat membacanya (404 palsu).
	t.Cleanup(func() { _ = r.DeleteImage(ctx, key) })

	t.Run("boleh baca bucket", func(t *testing.T) {
		if _, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(r.bucket), MaxKeys: aws.Int32(1),
		}); err != nil {
			t.Fatalf("ListObjectsV2: %v", err)
		}
	})

	t.Run("boleh tulis ke bucket", func(t *testing.T) {
		_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(r.bucket), Key: aws.String(key),
			Body: bytes.NewReader(jpeg), ContentType: aws.String("image/jpeg"),
		})
		if err != nil {
			t.Fatalf("PutObject gagal — token R2 kemungkinan besar baca-sahaja.\n"+
				"Betulkan di Cloudflare: R2 > Manage API Tokens > kebenaran\n"+
				"\"Object Read & Write\" pada bucket %q.\nRalat: %v", r.bucket, err)
		}
	})

	t.Run("VerifyImageFormat kenal objek yang diupload", func(t *testing.T) {
		if err := r.VerifyImageFormat(ctx, key); err != nil {
			t.Fatalf("VerifyImageFormat: %v", err)
		}
	})

	// Upload boleh berjaya sepenuhnya sementara gambar tetap TAK dapat
	// dipapar — itulah keadaan pada 2026-08-09. Subtest ni menutup jurang
	// tu: ia ambil objek melalui URL awam yang sama yang app guna.
	t.Run("gambar boleh dibaca melalui R2_PUBLIC_URL", func(t *testing.T) {
		if !r.HasPublicURL() {
			t.Skip("R2_PUBLIC_URL kosong — set dulu (r2.dev dev URL atau custom domain)")
		}

		url := r.PublicURL(key)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			t.Fatalf("GET %s\nstatus=%d body=%s\n"+
				"Public Development URL kemungkinan belum diaktifkan pada bucket, "+
				"atau R2_PUBLIC_URL salah (jangan letak '/' di hujung).",
				url, resp.StatusCode, body)
		}

		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, jpeg) {
			t.Fatalf("kandungan tak sama: dapat %d bait, jangka %d", len(got), len(jpeg))
		}
	})
}
