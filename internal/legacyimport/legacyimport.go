package legacyimport

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strconv"
	"strings"

	"marc/internal/phone"
)

// HeaderNames are the columns in the 2026 MARC member export. The workbook
// contains title/legend rows before this header, so the parser locates it
// instead of assuming that the first CSV row is the header.
var HeaderNames = []string{
	"Bil.",
	"Status",
	"Kategori",
	"Jawatan Kelab",
	"Kod Kelab",
	"No. ID.",
	"/",
	"Tahun Daftar",
	"-",
	"Bil. Ahli",
	"No. Ahli",
	"Nama",
	"Telefon",
	"Emel",
	"Bahagian",
	"Alamat",
	"Jawatan",
	"Nama Waris",
	"TelefonWaris",
	"Kesihatan",
	"Saiz Baju",
	"Jenis Baju",
	"Lengan",
	"Catatan",
}

type Row struct {
	SourceRow        int
	LegacyNumber     string
	Status           string
	Category         string
	ClubPosition     string
	LegacyStaffID    string
	RegistrationYear string
	MemberNumber     string
	MemberID         string
	DisplayName      string
	Phone            string
	Email            string
	DepartmentCode   string
	Address          string
	Position         string
	EmergencyName    string
	EmergencyPhone   string
	HealthNotes      string
	ShirtSize        string
	ShirtType        string
	Sleeve           string
	Notes            string
}

type Conflict struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ValidatedRow struct {
	Row
	NormalizedEmail string
	NormalizedPhone string
	Conflicts       []Conflict
	Warnings        []Conflict
}

type Report struct {
	HeaderRow int            `json:"header_row"`
	Rows      []ValidatedRow `json:"rows"`
	Conflicts int            `json:"conflicts"`
	Warnings  int            `json:"warnings"`
	ValidRows int            `json:"valid_rows"`
	TotalRows int            `json:"total_rows"`
	Emails    map[string]int `json:"-"`
	StaffIDs  map[string]int `json:"-"`
	MemberIDs map[string]int `json:"-"`
}

func Parse(r io.Reader) (Report, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, headerRow, err := findHeader(reader)
	if err != nil {
		return Report{}, err
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}

	report := Report{
		HeaderRow: headerRow,
		Rows:      make([]ValidatedRow, 0),
		Emails:    map[string]int{},
		StaffIDs:  map[string]int{},
		MemberIDs: map[string]int{},
	}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, fmt.Errorf("baris CSV selepas %d tidak sah: %w", headerRow, err)
		}
		if blankRecord(record) {
			continue
		}
		if len(record) == 0 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(record[0])); err != nil {
			// The workbook has a legend after the member rows. Only rows
			// with a numeric Bil. value are member records.
			continue
		}
		row := rowFromRecord(record, index, headerRow+len(report.Rows)+1)
		normalized := Validate(row)
		report.Rows = append(report.Rows, normalized)
		report.TotalRows++
		report.Conflicts += len(normalized.Conflicts)
		report.Warnings += len(normalized.Warnings)
		if len(normalized.Conflicts) == 0 {
			report.ValidRows++
		}
		if normalized.NormalizedEmail != "" {
			report.Emails[normalized.NormalizedEmail]++
		}
		if normalized.LegacyStaffID != "" {
			report.StaffIDs[normalizeIdentifier(normalized.LegacyStaffID)]++
		}
		if normalized.MemberID != "" {
			report.MemberIDs[normalized.MemberID]++
		}
	}

	addDuplicateConflicts(report.Rows, report.Emails, func(row *ValidatedRow) string {
		return row.NormalizedEmail
	}, "duplicate_email", "Emel muncul lebih daripada sekali dalam fail.")
	addDuplicateConflicts(report.Rows, report.StaffIDs, func(row *ValidatedRow) string {
		return normalizeIdentifier(row.LegacyStaffID)
	}, "duplicate_staff_id", "No. ID. muncul lebih daripada sekali dalam fail.")
	addDuplicateConflicts(report.Rows, report.MemberIDs, func(row *ValidatedRow) string {
		return row.MemberID
	}, "duplicate_member_id", "No. Ahli muncul lebih daripada sekali dalam fail.")
	report.Conflicts = 0
	report.Warnings = 0
	report.ValidRows = 0
	for i := range report.Rows {
		report.Conflicts += len(report.Rows[i].Conflicts)
		report.Warnings += len(report.Rows[i].Warnings)
		if len(report.Rows[i].Conflicts) == 0 {
			report.ValidRows++
		}
	}
	return report, nil
}

func Validate(row Row) ValidatedRow {
	out := ValidatedRow{Row: row, NormalizedEmail: strings.ToLower(strings.TrimSpace(row.Email))}
	out.NormalizedPhone, _ = phone.NormalizeMY(strings.TrimSpace(row.Phone))
	add := func(code, message string) {
		out.Conflicts = append(out.Conflicts, Conflict{Code: code, Message: message})
	}
	warn := func(code, message string) {
		out.Warnings = append(out.Warnings, Conflict{Code: code, Message: message})
	}

	if out.NormalizedEmail == "" {
		add("missing_email", "Emel diperlukan.")
	} else if _, err := mail.ParseAddress(out.NormalizedEmail); err != nil {
		add("invalid_email", "Format emel tidak sah.")
	}
	if strings.TrimSpace(row.DisplayName) == "" {
		add("missing_name", "Nama diperlukan.")
	}
	if strings.TrimSpace(row.MemberID) == "" {
		add("missing_member_id", "No. Ahli diperlukan.")
	}
	if strings.TrimSpace(row.LegacyStaffID) == "" {
		add("missing_staff_id", "No. ID. diperlukan.")
	}
	staffID := normalizeIdentifier(row.LegacyStaffID)
	if staffID == "XXXX" || staffID == "MS" {
		add("placeholder_staff_id", "No. ID. ialah placeholder dan perlu disahkan.")
	}
	if strings.TrimSpace(row.Phone) != "" && out.NormalizedPhone == "" {
		warn("invalid_phone_cleared", "Telefon tidak dapat dinormalisasi dan akan dikosongkan.")
	}
	if row.Status != "Aktif" && row.Status != "Tidak Aktif" {
		add("invalid_status", "Status mesti Aktif atau Tidak Aktif.")
	}
	if year := strings.TrimSpace(row.RegistrationYear); year != "" {
		if _, err := strconv.Atoi(year); err != nil {
			add("invalid_registration_year", "Tahun daftar tidak sah.")
		}
	}
	return out
}

func findHeader(reader *csv.Reader) ([]string, int, error) {
	for rowNumber := 1; ; rowNumber++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil, 0, errors.New("header CSV MARC tidak ditemui")
		}
		if err != nil {
			return nil, 0, fmt.Errorf("gagal membaca CSV: %w", err)
		}
		if len(record) >= len(HeaderNames) && strings.TrimSpace(record[0]) == HeaderNames[0] &&
			strings.TrimSpace(record[1]) == HeaderNames[1] && strings.TrimSpace(record[10]) == HeaderNames[10] {
			return record, rowNumber, nil
		}
	}
}

func rowFromRecord(record []string, index map[string]int, sourceRow int) Row {
	get := func(name string) string {
		i, ok := index[name]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	return Row{
		SourceRow:        sourceRow,
		LegacyNumber:     get("Bil."),
		Status:           get("Status"),
		Category:         get("Kategori"),
		ClubPosition:     get("Jawatan Kelab"),
		LegacyStaffID:    get("No. ID."),
		RegistrationYear: get("Tahun Daftar"),
		MemberNumber:     get("Bil. Ahli"),
		MemberID:         get("No. Ahli"),
		DisplayName:      get("Nama"),
		Phone:            get("Telefon"),
		Email:            get("Emel"),
		DepartmentCode:   get("Bahagian"),
		Address:          get("Alamat"),
		Position:         get("Jawatan"),
		EmergencyName:    get("Nama Waris"),
		EmergencyPhone:   get("TelefonWaris"),
		HealthNotes:      get("Kesihatan"),
		ShirtSize:        get("Saiz Baju"),
		ShirtType:        get("Jenis Baju"),
		Sleeve:           get("Lengan"),
		Notes:            get("Catatan"),
	}
}

func addDuplicateConflicts(rows []ValidatedRow, counts map[string]int, key func(*ValidatedRow) string, code, message string) {
	for i := range rows {
		value := key(&rows[i])
		if value != "" && counts[value] > 1 {
			rows[i].Conflicts = append(rows[i].Conflicts, Conflict{Code: code, Message: message})
		}
	}
}

func blankRecord(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func normalizeIdentifier(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

// CanonicalDepartmentCode accepts the CSV value case-insensitively and
// returns the exact code stored in departments for the foreign key write.
func CanonicalDepartmentCode(value string, departments map[string]string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(value))
	if key == "" {
		return "", true
	}
	code, ok := departments[key]
	return code, ok
}

func UnknownDepartmentConflict(value string, departments map[string]string) *Conflict {
	if _, ok := CanonicalDepartmentCode(value, departments); ok {
		return nil
	}
	return &Conflict{
		Code:    "unknown_department",
		Message: fmt.Sprintf("Kod bahagian tidak wujud: %s.", strings.TrimSpace(value)),
	}
}

// SortedConflictCodes is useful for stable API responses and tests.
func SortedConflictCodes(row ValidatedRow) []string {
	codes := make([]string, 0, len(row.Conflicts))
	for _, conflict := range row.Conflicts {
		codes = append(codes, conflict.Code)
	}
	sort.Strings(codes)
	return codes
}
