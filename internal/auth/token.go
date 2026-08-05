package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// GenerateOpaqueToken buat token rawak (untuk refresh token & email
// verification token). Ini yang dihantar ke client / email — bukan
// yang disimpan dalam DB.
func GenerateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken hash token rawak di atas sebelum simpan dalam DB, supaya
// kalau DB bocor, token asal tak boleh digunakan terus.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
