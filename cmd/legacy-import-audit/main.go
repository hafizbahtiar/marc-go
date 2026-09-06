package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"marc/internal/legacyimport"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: legacy-import-audit [--details] <csv-file>")
		os.Exit(2)
	}
	details := false
	path := os.Args[1]
	if os.Args[1] == "--details" {
		details = true
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: legacy-import-audit [--details] <csv-file>")
			os.Exit(2)
		}
		path = os.Args[2]
	}
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open CSV: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	report, err := legacyimport.Parse(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse CSV: %v\n", err)
		os.Exit(1)
	}
	counts := map[string]int{}
	warningCounts := map[string]int{}
	for _, row := range report.Rows {
		for _, conflict := range row.Conflicts {
			counts[conflict.Code]++
		}
		for _, warning := range row.Warnings {
			warningCounts[warning.Code]++
		}
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	fmt.Printf("header_row=%d total_rows=%d valid_rows=%d conflict_rows=%d\n",
		report.HeaderRow, report.TotalRows, report.ValidRows, report.TotalRows-report.ValidRows)
	for _, code := range codes {
		fmt.Printf("%s=%d\n", code, counts[code])
	}
	for code, count := range warningCounts {
		fmt.Printf("warning_%s=%d\n", code, count)
	}
	if details {
		fmt.Println("conflict_rows:")
		for _, row := range report.Rows {
			if len(row.Conflicts) == 0 {
				continue
			}
			rowCodes := make([]string, 0, len(row.Conflicts))
			for _, conflict := range row.Conflicts {
				rowCodes = append(rowCodes, conflict.Code)
			}
			for _, warning := range row.Warnings {
				rowCodes = append(rowCodes, "warning:"+warning.Code)
			}
			sort.Strings(rowCodes)
			fmt.Printf("source_row=%d member_id=%s staff_id=%s conflicts=%s\n",
				row.SourceRow, row.MemberID, row.LegacyStaffID, strings.Join(rowCodes, ","))
		}
	}
}
