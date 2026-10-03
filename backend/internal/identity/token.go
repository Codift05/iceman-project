package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Masa berlaku token mengikuti SRS-AUT-002.
const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
)

var ErrTokenInvalid = errors.New("token tidak sah")

// Claims adalah isi token akses.
type Claims struct {
	UserID  uuid.UUID  `json:"uid"`
	Role    string     `json:"role"`
	DepotID *uuid.UUID `json:"depot,omitempty"`
	jwt.RegisteredClaims
}

// Signer menerbitkan dan memeriksa token akses.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner membuat penandatangan dengan kunci rahasia dari konfigurasi.
func NewSigner(key []byte) *Signer {
	return &Signer{key: key, now: time.Now}
}

// IssueAccess menerbitkan token akses berumur pendek.
func (s *Signer) IssueAccess(userID uuid.UUID, role string, depotID *uuid.UUID) (string, error) {
	now := s.now()
	claims := Claims{
		UserID:  userID,
		Role:    role,
		DepotID: depotID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("menandatangani token: %w", err)
	}
	return signed, nil
}

// ParseAccess memeriksa tanda tangan dan masa berlaku token akses.
func (s *Signer) ParseAccess(raw string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrTokenInvalid
		}
		return s.key, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, ErrTokenInvalid
	}
	return &claims, nil
}

// NewRefreshToken membangkitkan token penyegar acak beserta hash yang disimpan.
//
// Yang tersimpan di basis data hanya hash-nya. Bila basis data bocor, token
// yang ada di sana tidak dapat dipakai masuk.
func NewRefreshToken() (plain, hashed string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("membangkitkan token penyegar: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashRefreshToken(plain), nil
}

// HashRefreshToken menghitung hash token penyegar untuk pencocokan.
func HashRefreshToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
