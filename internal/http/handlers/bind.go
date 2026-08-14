package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// bindJSON macam c.ShouldBindJSON, tapi respond terus dengan mesej ralat
// Bahasa Melayu yang sesuai dipaparkan di UI (bukan raw validator text
// macam "Key: 'registerRequest.Password' Error:Field validation...").
func bindJSON(c *gin.Context, req interface{}) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": friendlyBindError(err)})
		return false
	}
	return true
}

func friendlyBindError(err error) string {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) && len(ve) > 0 {
		fe := ve[0]
		switch fe.Field() {
		case "Email":
			if fe.Tag() == "email" {
				return "Format email tidak sah"
			}
			return "Email diperlukan"
		case "Password":
			switch fe.Tag() {
			case "min":
				return "Kata laluan diperlukan (minimum 6 aksara)"
			case "max":
				return "Kata laluan terlalu panjang (maksimum 72 aksara)"
			}
			return "Kata laluan diperlukan"
		case "RefreshToken":
			return "Refresh token diperlukan"
		case "Token":
			return "Token diperlukan"
		case "OnesignalID":
			return "OneSignal ID diperlukan"
		case "Content":
			return "Kandungan diperlukan"
		case "ContentType":
			return "Jenis fail diperlukan"
		}
	}
	return "Data tidak sah"
}

// parseUUIDParam baca parameter laluan sebagai UUID, dan respond 400
// sendiri kalau ia tak sah. `false` bermakna respons sudah ditulis.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id tidak sah"})
		return uuid.Nil, false
	}
	return id, true
}

// Penukar jenis pgtype. Nilai kosong (zero time / string kosong / uuid.Nil)
// jadi NULL — itu yang dimaksudkan oleh medan pilihan yang tak dihantar.
func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// malaysiaTZ — sama seperti internal/receipt: FixedZone dan bukan
// LoadLocation, supaya ia tak bergantung pada tzdata dalam imej container
// yang nipis.
var malaysiaTZ = time.FixedZone("MYT", 8*60*60)

// pgDate — hanya bahagian tarikh, dipotong mengikut waktu MALAYSIA.
//
// Sijil menyimpan `date` dan bukan `timestamptz`: "1 Sep 2026" yang
// tercetak tak boleh beralih hari ikut zon waktu pembaca. Pemotongan itu
// mesti berlaku dalam zon waktu ACARA, bukan UTC — aktiviti yang bermula
// 00:30 MYT pada 1 September ialah 16:30 UTC pada 31 Ogos, dan sijil yang
// mencetak "31 Ogos" salah pada dokumen yang tak boleh dibetulkan selepas
// diterbitkan.
func pgDate(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	y, m, d := t.In(malaysiaTZ).Date()
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}
