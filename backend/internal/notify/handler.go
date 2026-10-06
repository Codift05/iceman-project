package notify

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// Handler memaparkan pengaturan dan pemantauan notifikasi sebagai endpoint
// HTTP.
type Handler struct{ svc *Service }

// NewHandler membuat handler notifikasi.
func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

// List mengembalikan percobaan notifikasi untuk pemantauan.
//
// Kanal yang tersedia ikut dikirim agar antarmuka dapat membedakan kanal yang
// disebut pengaturan dari kanal yang implementasinya memang sudah ada. Selama
// OQ-012 belum diputuskan, keduanya tidak selalu sama.
func (h *Handler) List(c echo.Context) error {
	f := ListFilter{
		Status: c.QueryParam("status"),
		Event:  c.QueryParam("event"),
	}
	if raw := c.QueryParam("order_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "order_id", Message: "Pengenal pesanan tidak sah."})
		}
		f.OrderID = &id
	}
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Limit = n
		}
	}

	out, err := h.svc.List(c.Request().Context(), f)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"notifications":      out,
		"available_channels": h.svc.AvailableChannels(),
	})
}

// Settings mengembalikan pengaturan seluruh event.
func (h *Handler) Settings(c echo.Context) error {
	out, err := h.svc.Settings(c.Request().Context())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"settings":           out,
		"known_events":       KnownEvents(),
		"known_channels":     KnownChannels(),
		"available_channels": h.svc.AvailableChannels(),
	})
}

type settingRequest struct {
	Event    string   `json:"event"`
	Enabled  bool     `json:"enabled"`
	Channels []string `json:"channels"`
}

// SetSetting mengubah pengaturan satu event.
func (h *Handler) SetSetting(c echo.Context) error {
	var req settingRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if strings.TrimSpace(req.Event) == "" {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "event", Message: "Jenis event wajib diisi."})
	}

	out, err := h.svc.SetSetting(c.Request().Context(), Setting{
		Event:    strings.ToUpper(strings.TrimSpace(req.Event)),
		Enabled:  req.Enabled,
		Channels: req.Channels,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, out)
}

// codeFor menerjemahkan galat domain menjadi kode galat API.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrEventUnknown), errors.Is(err, ErrChannelUnknown):
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrEventUnknown):
		return []httpx.Detail{{Field: "event", Message: err.Error()}}
	case errors.Is(err, ErrChannelUnknown):
		return []httpx.Detail{{Field: "channels", Message: err.Error()}}
	default:
		return nil
	}
}
