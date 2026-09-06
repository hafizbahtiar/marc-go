package middleware

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"marc/internal/auth"
	"marc/internal/db/sqlc"
)

const userIDKey = "userID"
const sessionIDKey = "sessionID"

func RequireAuth(j *auth.JWT, q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak dijumpai"})
			return
		}

		userID, sessionID, err := j.ParseAccessToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token tidak sah"})
			return
		}

		if ok, status, msg := familyAlive(c, q, userID, sessionID); !ok {
			c.AbortWithStatusJSON(status, gin.H{"error": msg})
			return
		}

		c.Set(userIDKey, userID)
		c.Set(sessionIDKey, sessionID)
		c.Next()
	}
}

// UserID pulangkan user id dari context. Hanya selamat dipanggil dalam
// route yang dilindungi RequireAuth.
func UserID(c *gin.Context) uuid.UUID {
	return c.MustGet(userIDKey).(uuid.UUID)
}

// SessionID - family_id sesi dari claim `sid`. uuid.Nil untuk token lama
// (sebelum sid wujud) - GET /me/sessions tak akan tandakan "peranti ini".
func SessionID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(sessionIDKey)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}

// OptionalAuth cuba parse Bearer token kalau ada, tapi TAK abort request
// kalau tiada/tak sah - untuk route awam yang nak "tahu siapa kalau log
// masuk" tanpa wajibkan auth (cth: donation checkout, Stage 12 - ahli
// MARC yang log masuk dikaitkan user_id, orang luar tetap boleh donate).
func OptionalAuth(j *auth.JWT, q *sqlc.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if ok && token != "" {
			if userID, sessionID, err := j.ParseAccessToken(token); err == nil {
				if ok, _, _ := familyAlive(c, q, userID, sessionID); ok {
					c.Set(userIDKey, userID)
					c.Set(sessionIDKey, sessionID)
				}
			}
		}
		c.Next()
	}
}

// familyAlive - token lama tanpa sid (uuid.Nil) diluluskan; token baru
// kena ada family refresh yang belum luput. Revoke/logout-all padam
// family → access JWT ditolak serta-merta, bukan tunggu TTL.
//
// Ralat DB = 500 (bukan 401) supaya interceptor mobile tak clear sesi
// semasa outage.
func familyAlive(c *gin.Context, q *sqlc.Queries, userID, sessionID uuid.UUID) (ok bool, status int, msg string) {
	if sessionID == uuid.Nil {
		return true, 0, ""
	}
	alive, err := q.HasActiveRefreshTokenFamily(c.Request.Context(), sqlc.HasActiveRefreshTokenFamilyParams{
		UserID:   userID,
		FamilyID: sessionID,
	})
	if err != nil {
		log.Printf("gagal semak refresh token family (user=%s, family=%s): %v", userID, sessionID, err)
		return false, http.StatusInternalServerError, "gagal sahkan sesi"
	}
	if !alive {
		return false, http.StatusUnauthorized, "token tidak sah"
	}
	return true, 0, ""
}

// UserIDOptional pulangkan user id + true kalau OptionalAuth (atau
// RequireAuth) berjaya set context; false kalau request tak
// authenticated (bukan ralat - route awam yang benarkan anonymous).
func UserIDOptional(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(userIDKey)
	if !ok {
		return uuid.UUID{}, false
	}
	return v.(uuid.UUID), true
}
