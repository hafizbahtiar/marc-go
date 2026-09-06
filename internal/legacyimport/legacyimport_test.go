package legacyimport

import (
	"strings"
	"testing"
)

func TestParseFindsHeaderAfterWorkbookPreamble(t *testing.T) {
	input := strings.Join([]string{
		",,,,,",
		"SENARAI DAFTAR AHLI,,,,,",
		strings.Join(HeaderNames, ","),
		`1,Aktif,LAMA,Tiada,MARC-,1234,/,2026,-,1,MARC-1234/2026-1,Nama Ahli,0123456789,nama@example.com,BPI,,Pegawai,,,,,,`,
	}, "\n")

	report, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if report.TotalRows != 1 || report.ValidRows != 1 {
		t.Fatalf("report = %+v, want one valid row", report)
	}
	if report.Rows[0].NormalizedEmail != "nama@example.com" {
		t.Fatalf("email = %q", report.Rows[0].NormalizedEmail)
	}
}

func TestParseMarksPlaceholdersAndDuplicates(t *testing.T) {
	input := strings.Join([]string{
		strings.Join(HeaderNames, ","),
		`1,Aktif,LAMA,Tiada,MARC-,XXXX,/,2026,-,1,MARC-0001/2026-1,Nama Satu,0123456789,a@example.com,BPI,,,,,,,,`,
		`2,Aktif,LAMA,Tiada,MARC-,XXXX,/,2026,-,2,MARC-0002/2026-2,Nama Dua,0123456789,a@example.com,BPI,,,,,,,,`,
	}, "\n")

	report, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if report.ValidRows != 0 {
		t.Fatalf("ValidRows = %d, want 0", report.ValidRows)
	}
	for _, row := range report.Rows {
		codes := SortedConflictCodes(row)
		if len(codes) != 3 || codes[0] != "duplicate_email" || codes[1] != "duplicate_staff_id" || codes[2] != "placeholder_staff_id" {
			t.Fatalf("conflict codes = %v, want duplicate_email, duplicate_staff_id and placeholder_staff_id", codes)
		}
	}
}

func TestValidateRejectsInvalidPhoneAndStatus(t *testing.T) {
	row := Validate(Row{
		Status:        "Unknown",
		LegacyStaffID: "1234",
		MemberID:      "MARC-1234/2026-1",
		DisplayName:   "Nama",
		Phone:         "not-a-phone",
		Email:         "bad",
	})
	codes := SortedConflictCodes(row)
	for _, wanted := range []string{"invalid_email", "invalid_status"} {
		found := false
		for _, code := range codes {
			if code == wanted {
				found = true
			}
		}
		if !found {
			t.Errorf("missing conflict %q in %v", wanted, codes)
		}
	}
	if len(row.Warnings) != 1 || row.Warnings[0].Code != "invalid_phone_cleared" {
		t.Fatalf("warnings = %+v, want cleared phone warning", row.Warnings)
	}
}
