package handlers

import (
	"testing"

	"marc/internal/db/sqlc"
)

// seedRoles — padanan internal/db/migrations/20260805223300_seed_roles.sql.
func seedRoles() []sqlc.Role {
	return []sqlc.Role{
		{Key: "ahli", Rank: 10},
		{Key: "supervisor", Rank: 50},
		{Key: "manager", Rank: 60},
		{Key: "superadmin", Rank: 100},
	}
}

func TestVisibleRankCeiling(t *testing.T) {
	roles := seedRoles()
	cases := []struct {
		name       string
		viewerRank int32
		want       int32
	}{
		{"ahli nampak ahli + supervisor", 10, 50},
		{"supervisor nampak sehingga manager", 50, 60},
		{"manager nampak manager ke bawah, bukan superadmin", 60, 60},
		{"superadmin nampak semua", 100, 100},
	}
	for _, c := range cases {
		if got := visibleRankCeiling(roles, c.viewerRank); got != c.want {
			t.Errorf("%s: visibleRankCeiling(%d) = %d, mahu %d", c.name, c.viewerRank, got, c.want)
		}
	}
}

// Role baharu yang disisip di tengah hierarki kena ikut peraturan yang
// sama tanpa perubahan kod (sebab tu siling dikira drpd jadual roles,
// bukan rank hardcoded).
func TestVisibleRankCeilingRoleBaharu(t *testing.T) {
	roles := append(seedRoles(), sqlc.Role{Key: "ketua_ahli", Rank: 30})

	if got := visibleRankCeiling(roles, 10); got != 30 {
		t.Errorf("ahli patut nampak sehingga ketua_ahli (30), dapat %d", got)
	}
	if got := visibleRankCeiling(roles, 30); got != 50 {
		t.Errorf("ketua_ahli patut nampak sehingga supervisor (50), dapat %d", got)
	}
}

// Superadmin tak boleh terdedah walau viewer ada rank tak dikenali yang
// lebih tinggi drpd manager tapi rendah drpd superadmin.
func TestVisibleRankCeilingSuperadminSentiasaTersembunyi(t *testing.T) {
	roles := seedRoles()
	for _, viewerRank := range []int32{0, 10, 49, 50, 60, 99} {
		if got := visibleRankCeiling(roles, viewerRank); got >= 100 {
			t.Errorf("viewer rank %d dapat siling %d — superadmin terdedah", viewerRank, got)
		}
	}
}
