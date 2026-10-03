package identity

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// Handler memaparkan layanan identity sebagai endpoint HTTP.
type Handler struct{ svc *Service }

// NewHandler membuat handler identity.
func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	TokenType    string `json:"token_type"`
}

// Login memeriksa kredensial lalu menerbitkan pasangan token (SRS-AUT-005).
func (h *Handler) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if req.Email == "" || req.Password == "" {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "email", Message: "Surel dan kata sandi wajib diisi."})
	}

	u, err := h.svc.Authenticate(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}

	tok, err := h.svc.IssueTokens(c.Request().Context(), u, c.Request().UserAgent())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, toResponse(tok))
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh menukar token penyegar dengan pasangan baru (SRS-AUT-003).
func (h *Handler) Refresh(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil || req.RefreshToken == "" {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	tok, err := h.svc.Refresh(c.Request().Context(), req.RefreshToken, c.Request().UserAgent())
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.JSON(http.StatusOK, toResponse(tok))
}

// Logout membatalkan sesi yang sedang dipakai.
func (h *Handler) Logout(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil || req.RefreshToken == "" {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.svc.Logout(c.Request().Context(), req.RefreshToken); err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// Me mengembalikan identitas dan izin pengguna yang sedang masuk.
func (h *Handler) Me(c echo.Context) error {
	p := httpx.PrincipalFrom(c)
	if p == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	roleID, err := h.svc.RoleIDByCode(c.Request().Context(), p.Role)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	perms, err := h.svc.Permissions(c.Request().Context(), roleID)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"user_id":     p.UserID,
		"role":        p.Role,
		"depot_id":    p.DepotID,
		"permissions": perms,
	})
}

func toResponse(t *Tokens) tokenResponse {
	return tokenResponse{
		AccessToken:  t.Access,
		RefreshToken: t.Refresh,
		ExpiresAt:    t.AccessExpiry.UTC().Format("2006-01-02T15:04:05Z"),
		TokenType:    "Bearer",
	}
}

// codeFor memetakan galat layanan ke kode galat API pada SRS Bab 8.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrCredentialsInvalid):
		return "CREDENTIALS_INVALID"
	case errors.Is(err, ErrAccountLocked):
		return "ACCOUNT_LOCKED"
	case errors.Is(err, ErrAccountInactive):
		return "ACCOUNT_INACTIVE"
	case errors.Is(err, ErrMFARequired):
		return "MFA_REQUIRED"
	case errors.Is(err, ErrRefreshReused):
		return "REFRESH_REUSED"
	case errors.Is(err, ErrRefreshInvalid):
		return "REFRESH_INVALID"
	default:
		return "INTERNAL"
	}
}
