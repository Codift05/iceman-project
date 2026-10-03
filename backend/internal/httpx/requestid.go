package httpx

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/audit"
)

const headerRequestID = "X-Request-Id"
const ctxRequestID = "request_id"

// RequestID melekatkan pengenal pada setiap permintaan, lalu meneruskannya pada
// respons dan log. Pengenal ini dipakai menelusuri satu transaksi dari klien
// sampai ke pekerjaan latar, sesuai Architecture Bab 15.
func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get(headerRequestID)
			if id == "" {
				id = uuid.NewString()
			}
			c.Set(ctxRequestID, id)
			c.Response().Header().Set(headerRequestID, id)

			// Pengenal ikut dibawa pada konteks agar jejak audit yang ditulis
			// jauh di dalam lapisan layanan dapat dikaitkan dengan log.
			req := c.Request()
			c.SetRequest(req.WithContext(audit.WithRequestID(req.Context(), id)))
			return next(c)
		}
	}
}

// RequestIDFrom membaca pengenal permintaan yang sedang berjalan.
func RequestIDFrom(c echo.Context) string {
	if v, ok := c.Get(ctxRequestID).(string); ok {
		return v
	}
	return ""
}
