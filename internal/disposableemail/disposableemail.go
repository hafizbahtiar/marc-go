// Package disposableemail semak sama ada domain sesuatu alamat emel
// tergolong penyedia emel pelupusan/sekali-guna (disposable/temporary/
// throwaway email) — cth yopmail.com, mailinator.com, guerrillamail.com.
//
// Kenapa penting: `email_verified` (Stage 8) cuma buktikan "seseorang
// boleh terima SATU emel", bukan identiti sebenar/kekal — alamat
// pelupusan lulus proses tu dengan sempurna. Untuk app kelab yang gate
// ahli menerusi kelulusan management + yuran pendaftaran sebenar, akaun
// guna emel pelupusan bermakna tiada cara hubungi ahli tu lagi selepas
// domain pelupusan tamat/reset (kebanyakan mati dlm beberapa jam-hari).
//
// Senarai statik terbenam (domains.txt, drpd
// github.com/disposable-email-domains/disposable-email-domains) ialah
// PERTAHANAN UTAMA — beribu domain wujud dan bertambah setiap hari,
// jadi senarai DB manual sahaja tak akan pernah cukup exhaustive.
// internal/http/handlers boleh gabung semakan ni dengan jadual DB
// `blocked_email_domains` (tambahan management, tanpa perlu deploy kod
// baharu) — lihat handlers.checkDisposableEmail.
package disposableemail

import (
	_ "embed"
	"strings"
)

//go:embed domains.txt
var domainsFile string

var staticDomains map[string]bool

func init() {
	lines := strings.Split(domainsFile, "\n")
	staticDomains = make(map[string]bool, len(lines))
	for _, line := range lines {
		d := strings.ToLower(strings.TrimSpace(line))
		if d == "" || strings.HasPrefix(d, "#") {
			continue
		}
		staticDomains[d] = true
	}
}

// allowedEmails — pengecualian EKSPLISIT (alamat PENUH, bukan domain).
// Keputusan produk 2026-08-15: dua akaun tester (review Google Play/App
// Store) SENGAJA guna domain pelupusan yopmail.com — bukan pilihan
// pemaju, keperluan proses review kedua-dua platform. Allowlist alamat
// PENUH (bukan domain) dipilih berbanding "cipta akaun dulu sebelum
// sekatan wujud" supaya sekatan boleh aktif BILA-BILA tanpa bergantung
// pada turutan deploy — kos: dua baris kod ni perlu diselenggara/
// disemak kalau akaun tester ditukar kelak.
var allowedEmails = map[string]bool{
	"google@yopmail.com": true,
	"apple@yopmail.com":  true,
}

// IsAllowed semak SAHAJA allowlist alamat penuh (bukan senarai statik/
// domain) — diexport supaya CALLER LAIN (cth semakan jadual DB
// tambahan `blocked_email_domains` di auth.go) turut menghormati
// pengecualian tester, bukan setakat laluan IsDisposable. Tanpa ni,
// management boleh tambah "yopmail.com" ke jadual DB dan senyap kunci
// keluar dua akaun tester walau allowedEmails kata sebaliknya (Opus
// verify 2026-08-15 tangkap gap ni — laluan DB tak pernah runding
// dengan allowlist).
func IsAllowed(email string) bool {
	return allowedEmails[email]
}

// IsDisposableDomain semak SATU domain (bukan alamat penuh) terhadap
// senarai statik terbenam sahaja — TIDAK menyemak allowlist alamat
// (guna IsDisposable utk tu). Diexport berasingan supaya caller yang
// hanya ada domain (cth semakan jadual DB tambahan) tak perlu bina
// alamat palsu.
func IsDisposableDomain(domain string) bool {
	return staticDomains[strings.ToLower(strings.TrimSpace(domain))]
}

// IsDisposable semak ALAMAT EMEL penuh — allowlist dahulu (pengecualian
// menang atas domain pelupusan), lepas tu domain terhadap senarai
// statik. `email` mesti sudah huruf kecil + trim (padanan normalisasi
// sedia ada di Register).
func IsDisposable(email string) bool {
	if allowedEmails[email] {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return false
	}
	return IsDisposableDomain(email[at+1:])
}

// DomainOf — ekstrak domain (huruf selepas "@", huruf kecil) drpd alamat
// emel. "" kalau tiada "@" atau domain kosong. Guna oleh caller yang
// perlu semak jadual DB `blocked_email_domains` selepas semakan statik.
func DomainOf(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}
