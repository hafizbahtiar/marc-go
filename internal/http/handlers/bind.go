package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
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
			if fe.Tag() == "min" {
				return "Kata laluan diperlukan (minimum 6 aksara)"
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
