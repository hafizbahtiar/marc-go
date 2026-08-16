package certificate

import (
	"testing"
	"time"
)

func TestIsEligible(t *testing.T) {
	tests := []struct {
		name         string
		attended     int
		total        int
		thresholdPct int
		want         bool
	}{
		{"hadir semua, ambang 100", 3, 3, 100, true},
		{"terlepas satu, ambang 100", 2, 3, 100, false},
		{"2 dari 3 pada ambang 66", 2, 3, 66, true},
		{"2 dari 3 pada ambang 67", 2, 3, 67, false},
		{"langsung tak hadir", 0, 3, 1, false},
		{"sesi tunggal hadir", 1, 1, 100, true},
		// Aktiviti tanpa sesi tak sepatutnya wujud, tapi kalau data rosak
		// kita pilih 'tidak layak' berbanding pembahagian dengan sifar.
		{"sifar sesi", 0, 0, 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEligible(tt.attended, tt.total, tt.thresholdPct); got != tt.want {
				t.Errorf("IsEligible(%d, %d, %d) = %v, mahu %v",
					tt.attended, tt.total, tt.thresholdPct, got, tt.want)
			}
		})
	}
}

func TestWithinCheckinWindow(t *testing.T) {
	start := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"semasa sesi", start.Add(30 * time.Minute), true},
		{"tepat pada mula", start, true},
		{"1 jam sebelum", start.Add(-time.Hour), true},
		{"3 jam sebelum", start.Add(-3 * time.Hour), false},
		{"1 jam selepas tamat", end.Add(time.Hour), true},
		{"3 jam selepas tamat", end.Add(3 * time.Hour), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithinCheckinWindow(tt.now, start, end); got != tt.want {
				t.Errorf("WithinCheckinWindow(%v) = %v, mahu %v", tt.now, got, tt.want)
			}
		})
	}
}
