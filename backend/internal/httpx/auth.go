package httpx

import (
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const ctxPrincipal = "principal"

// Principal adalah identitas pemanggil yang sudah terverifikasi.
//
// Paket ini sengaja tidak mengenal paket identity, agar lapisan HTTP dan
// lapisan identitas dapat berubah sendiri sendiri tanpa saling mengunci.
// Penyambungannya dilakukan di cmd/api.
type Principal struct {
	UserID  uuid.UUID
	Role    string
	DepotID *uuid.UUID
}

// ParseToken memeriksa token akses dan mengembalikan identitas pemanggil.
type ParseToken func(raw string) (*Principal, error)

// CheckPermission memeriksa apakah sebuah peran memegang izin tertentu.
type CheckPermission func(c echo.Context, role, permission string) (bool, error)

// RequireAuth memastikan permintaan membawa token akses yang sah.
//
// Pemeriksaan diletakkan di middleware, bukan di tiap handler, agar tidak ada
// endpoint yang lolos karena pemeriksaannya lupa ditulis (SRS-AUT-006).
func RequireAuth(parse ParseToken) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw := bearerToken(c)
			if raw == "" {
				return Fail(c, "AUTH_REQUIRED")
			}
			p, err := parse(raw)
			if err != nil || p == nil {
				return Fail(c, "AUTH_REQUIRED")
			}
			c.Set(ctxPrincipal, p)
			return next(c)
		}
	}
}

// RequirePermission menolak permintaan yang perannya tidak memegang izin.
func RequirePermission(check CheckPermission, permission string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := PrincipalFrom(c)
			if p == nil {
				return Fail(c, "AUTH_REQUIRED")
			}
			ok, err := check(c, p.Role, permission)
			if err != nil {
				return Fail(c, "INTERNAL")
			}
			if !ok {
				return Fail(c, "FORBIDDEN")
			}
			return next(c)
		}
	}
}

// PrincipalFrom membaca identitas pemanggil pada permintaan yang berjalan.
func PrincipalFrom(c echo.Context) *Principal {
	if v, ok := c.Get(ctxPrincipal).(*Principal); ok {
		return v
	}
	return nil
}

func bearerToken(c echo.Context) string {
	const prefix = "Bearer "
	h := c.Request().Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
