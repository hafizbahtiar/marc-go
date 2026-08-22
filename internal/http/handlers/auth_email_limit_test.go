package handlers

import (
	"testing"
	"time"
)

func TestCheckEmailVerifySendLimitPertamaKaliLulus(t *testing.T) {
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	if err := checkEmailVerifySendLimit(nil, 0, now); err != nil {
		t.Fatalf("pertama kali: %v", err)
	}
}

func TestCheckEmailVerifySendLimitCooldown(t *testing.T) {
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	last := now.Add(-30 * time.Second)
	err := checkEmailVerifySendLimit(&last, 1, now)
	if err == nil {
		t.Fatal("mahu ditolak dalam 60s")
	}
	if err.status != 429 {
		t.Fatalf("status = %d, mahu 429", err.status)
	}
	if err.message == "" {
		t.Fatal("mesej kosong")
	}
}

func TestCheckEmailVerifySendLimitLepasCooldownLulus(t *testing.T) {
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	last := now.Add(-60 * time.Second)
	if err := checkEmailVerifySendLimit(&last, 1, now); err != nil {
		t.Fatalf("tepat 60s patut lulus: %v", err)
	}
}

func TestCheckEmailVerifySendLimitHadHarian(t *testing.T) {
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	last := now.Add(-2 * time.Minute)
	err := checkEmailVerifySendLimit(&last, emailVerifySendDailyMax, now)
	if err == nil {
		t.Fatal("mahu ditolak bila dah 5 kali")
	}
	if err.status != 429 {
		t.Fatalf("status = %d, mahu 429", err.status)
	}
}

func TestCheckEmailVerifySendLimitBawahHadHarianLulus(t *testing.T) {
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	last := now.Add(-2 * time.Minute)
	if err := checkEmailVerifySendLimit(&last, emailVerifySendDailyMax-1, now); err != nil {
		t.Fatalf("4 kali patut lulus: %v", err)
	}
}
