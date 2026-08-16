package storage

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestSignedURLKosongBilaTakDikonfigur(t *testing.T) {
	r := NewR2Client("", "", "", "", "")
	if got := r.SignedURL(context.Background(), "posts/x.jpg"); got != "" {
		t.Fatalf("mahu kosong bila R2 tak dikonfigur, dapat %q", got)
	}
}

func TestSignedURLKosongUntukKunciKosong(t *testing.T) {
	r := NewR2Client("acct", "id", "secret", "bucket", "")
	if got := r.SignedURL(context.Background(), ""); got != "" {
		t.Fatalf("mahu kosong untuk kunci kosong, dapat %q", got)
	}
}

// Kestabilan URL ialah SEBAB cache wujud: menandatangani semula pada
// setiap permintaan menghasilkan X-Amz-Date berbeza, dan cache imej
// peranti dikunci ikut URL — jadi setiap tatalan feed akan memuat turun
// semula setiap gambar.
func TestURLStabilDalamTetingkapCache(t *testing.T) {
	r := NewR2Client("acct", "id", "secret", "bucket", "")
	ctx := context.Background()

	first := r.SignedURL(ctx, "posts/a.jpg")
	if first == "" {
		t.Fatal("penandatanganan gagal")
	}
	time.Sleep(1100 * time.Millisecond) // pastikan saat berubah
	second := r.SignedURL(ctx, "posts/a.jpg")

	if first != second {
		t.Fatalf("URL berubah dalam tetingkap cache — cache imej klien akan terlepas setiap kali\n1: %s\n2: %s", first, second)
	}
}

func TestKunciBerbezaDapatURLBerbeza(t *testing.T) {
	r := NewR2Client("acct", "id", "secret", "bucket", "")
	ctx := context.Background()
	if r.SignedURL(ctx, "posts/a.jpg") == r.SignedURL(ctx, "posts/b.jpg") {
		t.Fatal("objek berbeza patut dapat URL berbeza")
	}
}

func TestSignedURLAdaTandatanganDanTempohLuput(t *testing.T) {
	r := NewR2Client("acct", "id", "secret", "bucket", "")
	url := r.SignedURL(context.Background(), "posts/a.jpg")

	for _, want := range []string{"X-Amz-Signature=", "X-Amz-Expires=", "X-Amz-Date="} {
		if !strings.Contains(url, want) {
			t.Errorf("URL tiada %s: %s", want, url)
		}
	}
	// Berumur pendek ialah inti perubahan ni — URL kekal bermakna
	// pendedahan kekal.
	if !strings.Contains(url, "X-Amz-Expires=7200") {
		t.Errorf("mahu tempoh luput 2 jam, dapat %s", url)
	}
}

// Lawan R2 sebenar: URL yang ditandatangani mesti BOLEH diambil, dan
// objek yang sama TANPA tandatangan mesti DITOLAK. Yang kedua tu yang
// membuktikan lubang privasi betul-betul ditutup.
//
//	R2_LIVE_TEST=1 go test ./internal/storage/ -run TestSignedURLLive -v
func TestSignedURLLive(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("set R2_LIVE_TEST=1")
	}
	_ = godotenv.Load("../../.env")

	r := NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r.Enabled() {
		t.Skip("R2 tak dikonfigur")
	}

	ctx := context.Background()
	key := "posts/_signed-url-probe.jpg"
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	putProbeObject(t, r, key, jpeg)
	t.Cleanup(func() { _ = r.DeleteImage(ctx, key) })

	signed := r.SignedURL(ctx, key)
	if signed == "" {
		t.Fatal("penandatanganan gagal")
	}

	resp, err := http.Get(signed)
	if err != nil {
		t.Fatalf("GET ditandatangani: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("URL ditandatangani = %d, mahu 200", resp.StatusCode)
	}

	// Buang query tandatangan — patut ditolak.
	bare := signed[:strings.Index(signed, "?")]
	bareResp, err := http.Get(bare)
	if err != nil {
		t.Fatalf("GET tanpa tandatangan: %v", err)
	}
	defer bareResp.Body.Close()
	if bareResp.StatusCode == http.StatusOK {
		t.Fatal("objek boleh diambil TANPA tandatangan — bucket masih terdedah")
	}
}
