package storage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func jpegHeader(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	// Hantar HEADER sahaja — meniru julat 64KB yang dibaca dari R2, dan
	// membuktikan semakan tak perlukan fail penuh.
	b := buf.Bytes()
	if len(b) > 64*1024 {
		b = b[:64*1024]
	}
	return b
}

func TestDimensiDalamHadDiterima(t *testing.T) {
	if err := verifyDimensions(jpegHeader(t, 2048, 1536), MaxImageDimension); err != nil {
		t.Fatalf("2048x1536 patut lulus: %v", err)
	}
}

// Ini sebab semakan ni wujud: presigned URL membenarkan client menaikkan
// apa-apa terus ke R2, jadi "client dah kecilkan" bukan jaminan.
func TestDimensiTerlaluBesarDitolak(t *testing.T) {
	err := verifyDimensions(jpegHeader(t, 5000, 100), MaxImageDimension)
	if !errors.Is(err, ErrImageTooManyPixels) {
		t.Fatalf("5000px lebar patut ditolak, dapat %v", err)
	}

	err = verifyDimensions(jpegHeader(t, 100, 5000), MaxImageDimension)
	if !errors.Is(err, ErrImageTooManyPixels) {
		t.Fatalf("5000px tinggi patut ditolak, dapat %v", err)
	}
}

func TestTepatPadaHadDiterima(t *testing.T) {
	if err := verifyDimensions(jpegHeader(t, MaxImageDimension, 10), MaxImageDimension); err != nil {
		t.Fatalf("tepat pada had patut lulus: %v", err)
	}
}

func TestPNGDiukurJuga(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 6000, 10))); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifyDimensions(buf.Bytes(), MaxImageDimension), ErrImageTooManyPixels) {
		t.Fatal("PNG besar patut ditolak")
	}
}

// Header tak boleh dibaca (terpotong, atau WEBP yang tiada decoder) tak
// boleh menolak gambar — magic number dah lulus dan had bait masih
// terpakai. Gagal-terbuka di sini sengaja.
func TestHeaderTakBolehDibacaTidakMenolak(t *testing.T) {
	if err := verifyDimensions([]byte{0xFF, 0xD8, 0xFF}, MaxImageDimension); err != nil {
		t.Fatalf("header terpotong patut lulus senyap, dapat %v", err)
	}
	webp := append([]byte("RIFF0000WEBP"), make([]byte, 40)...)
	if err := verifyDimensions(webp, MaxImageDimension); err != nil {
		t.Fatalf("WEBP patut lulus (tiada decoder), dapat %v", err)
	}
}
