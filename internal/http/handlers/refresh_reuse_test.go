package handlers

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestConsumedIPMatchesSamaIP(t *testing.T) {
	consumedIP := pgtype.Text{String: "203.0.113.7", Valid: true}
	if !consumedIPMatches(consumedIP, "203.0.113.7") {
		t.Fatal("IP sama patut padan")
	}
}

func TestConsumedIPMatchesIPBerbeza(t *testing.T) {
	// Attacker yang race replay dari rangkaian lain daripada pemilik sah
	// tak patut lolos grace window walaupun timing dia tepat.
	consumedIP := pgtype.Text{String: "203.0.113.7", Valid: true}
	if consumedIPMatches(consumedIP, "198.51.100.9") {
		t.Fatal("IP berbeza tak patut padan")
	}
}

func TestConsumedIPMatchesKosongFailClosed(t *testing.T) {
	// consumed_ip tak direkod (cth row lama sebelum migration ni) -
	// fail closed, jangan bagi grace.
	if consumedIPMatches(pgtype.Text{}, "203.0.113.7") {
		t.Fatal("consumed_ip kosong patut fail closed (tak padan)")
	}
	if consumedIPMatches(pgtype.Text{String: "", Valid: true}, "203.0.113.7") {
		t.Fatal("consumed_ip string kosong patut fail closed (tak padan)")
	}
}
