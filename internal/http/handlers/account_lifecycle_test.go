package handlers

import (
	"testing"

	"github.com/google/uuid"
)

func TestDeletionRejection(t *testing.T) {
	userID := uuid.New()

	tests := []struct {
		name        string
		callerID    uuid.UUID
		targetID    uuid.UUID
		targetRole  string
		superadmins int64
		want        string
	}{
		{"self deletion", userID, userID, "ahli", 1, "akaun sendiri tidak boleh dipadam melalui modul ini"},
		{"superadmin target", uuid.New(), userID, "superadmin", 2, "akaun superadmin tidak boleh dipadam melalui modul ini"},
		{"last superadmin guard", uuid.New(), userID, "ahli", 1, ""},
		{"regular target", uuid.New(), userID, "ahli", 2, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deletionRejection(test.callerID, test.targetID, test.targetRole, test.superadmins); got != test.want {
				t.Fatalf("deletionRejection() = %q, want %q", got, test.want)
			}
		})
	}
}
