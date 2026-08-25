package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var ErrInvalidToken = errors.New("invalid or expired token")

type JWT struct {
	secret    []byte
	accessTTL time.Duration
}

func NewJWT(secret string, accessTTL time.Duration) *JWT {
	return &JWT{secret: []byte(secret), accessTTL: accessTTL}
}

func (j *JWT) AccessTTL() time.Duration {
	return j.accessTTL
}

// accessClaims - RegisteredClaims + `sid` (family_id refresh token).
// `sid` kenal SESI (device), bukan baris token yang berputar setiap
// refresh. Token lama tanpa `sid` tetap sah; SessionID = uuid.Nil.
type accessClaims struct {
	SessionID string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

func (j *JWT) GenerateAccessToken(userID, sessionID uuid.UUID) (string, error) {
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	if sessionID != uuid.Nil {
		claims.SessionID = sessionID.String()
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

func (j *JWT) ParseAccessToken(tokenString string) (userID, sessionID uuid.UUID, err error) {
	claims := &accessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return j.secret, nil
	})
	if err != nil || !token.Valid {
		return uuid.UUID{}, uuid.UUID{}, ErrInvalidToken
	}

	userID, err = uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, ErrInvalidToken
	}
	if claims.SessionID != "" {
		sessionID, err = uuid.Parse(claims.SessionID)
		if err != nil {
			return uuid.UUID{}, uuid.UUID{}, ErrInvalidToken
		}
	}
	return userID, sessionID, nil
}
