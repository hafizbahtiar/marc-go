package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// Ujian live — di-skip melainkan R2_LIVE_TEST=1, sama corak dengan
// TestR2LivePermissions sedia ada.
func TestR2PutObjectLive(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("tetapkan R2_LIVE_TEST=1 untuk jalankan")
	}
	_ = godotenv.Load("../../.env")

	r := NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"),
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r.Enabled() {
		t.Fatal("kredential R2 tak lengkap dalam .env")
	}

	ctx := context.Background()
	key := fmt.Sprintf("test/putobject-%d.pdf", time.Now().UnixNano())
	want := []byte("%PDF-1.4 ujian")

	if err := r.PutObject(ctx, key, "application/pdf", want); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	t.Cleanup(func() { _ = r.DeleteImage(context.Background(), key) })

	resp, err := http.Get(r.SignedURL(ctx, key))
	if err != nil {
		t.Fatalf("ambil semula: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, mahu 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("baca badan: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("kandungan = %q, mahu %q", got, want)
	}
}
