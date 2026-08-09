package redisclient

import (
	"context"
	"testing"
)

// URL kosong MESTI menghasilkan client dimatikan dan bukan ralat — app
// perlu boot tanpa Redis, sama macam tanpa R2/Stripe/Resend.
func TestUrlKosongJadiDimatikanBukanRalat(t *testing.T) {
	c, err := New("")
	if err != nil {
		t.Fatalf("URL kosong patut tiada ralat, dapat %v", err)
	}
	if c.Enabled() {
		t.Error("patut dimatikan")
	}
	if c.Redis() != nil {
		t.Error("client mentah patut nil bila dimatikan")
	}
	// Operasi pada client dimatikan mesti no-op senyap, bukan panik.
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping pada client dimatikan = %v, mahu nil", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close pada client dimatikan = %v, mahu nil", err)
	}
}

func TestUrlSahDiterima(t *testing.T) {
	c, err := New("redis://default:secret@localhost:6379/0")
	if err != nil {
		t.Fatalf("URL sah ditolak: %v", err)
	}
	if !c.Enabled() {
		t.Error("patut didayakan")
	}
	if c.Redis() == nil {
		t.Error("client mentah patut ada")
	}
	t.Cleanup(func() { _ = c.Close() })
}

// Salah taip dalam URL patut gagal SEMASA BOOT dengan mesej jelas, bukan
// senyap jadi client yang mati.
func TestUrlTakSahPulangRalat(t *testing.T) {
	if _, err := New("bukan-url-redis"); err == nil {
		t.Fatal("URL tak sah patut pulang ralat")
	}
}
