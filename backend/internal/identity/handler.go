package identity

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
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

	// Kredensial benar namun faktor kedua masih harus dilewati. Yang diberikan
	// adalah token tantangan berumur pendek, bukan token akses.
	switch {
	case errors.Is(err, ErrMFARequired):
		ch, cerr := h.svc.Challenge(u.ID, PurposeMFAVerify)
		if cerr != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		return c.JSON(http.StatusOK, challengeResponse{
			MFARequired:    true,
			ChallengeToken: ch,
			Next:           "verify",
		})
	case errors.Is(err, ErrMFAEnrollRequired):
		ch, cerr := h.svc.Challenge(u.ID, PurposeMFAEnroll)
		if cerr != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		return c.JSON(http.StatusOK, challengeResponse{
			MFARequired:    true,
			ChallengeToken: ch,
			Next:           "enroll",
		})
	case err != nil:
		return httpx.Fail(c, codeFor(err))
	}

	tok, err := h.svc.IssueTokens(c.Request().Context(), u, c.Request().UserAgent())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, toResponse(tok))
}

// challengeResponse dikirim ketika masuk belum selesai karena faktor kedua.
type challengeResponse struct {
	MFARequired    bool   `json:"mfa_required"`
	ChallengeToken string `json:"challenge_token"`
	Next           string `json:"next"` // "verify" bila sudah terdaftar, "enroll" bila belum
}

type mfaVerifyRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"`
}

// MFAVerify menukar token tantangan dan kode TOTP dengan token penuh.
func (h *Handler) MFAVerify(c echo.Context) error {
	var req mfaVerifyRequest
	if err := c.Bind(&req); err != nil || req.ChallengeToken == "" || req.Code == "" {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	ctx := c.Request().Context()

	userID, err := h.svc.ParseChallenge(req.ChallengeToken, PurposeMFAVerify)
	if err != nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	if err := h.svc.VerifyMFA(ctx, userID, req.Code); err != nil {
		return httpx.Fail(c, codeFor(err))
	}

	u, err := h.svc.UserByID(ctx, userID)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	tok, err := h.svc.IssueTokens(ctx, u, c.Request().UserAgent())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, toResponse(tok))
}

type mfaEnrollRequest struct {
	ChallengeToken string `json:"challenge_token"`
}

// MFAEnroll memulai pendaftaran faktor kedua.
//
// Menerima token tantangan dari alur masuk, atau token akses bila pengguna
// yang sudah masuk ingin mengaktifkannya sendiri.
func (h *Handler) MFAEnroll(c echo.Context) error {
	ctx := c.Request().Context()

	userID, ok := h.subject(c)
	if !ok {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	enr, err := h.svc.BeginMFAEnrollment(ctx, userID)
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"secret":           enr.Secret,
		"provisioning_uri": enr.URI,
		"next":             "confirm",
	})
}

type mfaConfirmRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"`
}

// MFAConfirm mengaktifkan faktor kedua setelah satu kode terbukti benar.
func (h *Handler) MFAConfirm(c echo.Context) error {
	var req mfaConfirmRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	ctx := c.Request().Context()

	userID, ok := h.subjectWith(c, req.ChallengeToken)
	if !ok {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	if err := h.svc.ConfirmMFAEnrollment(ctx, userID, req.Code); err != nil {
		return httpx.Fail(c, codeFor(err))
	}

	u, err := h.svc.UserByID(ctx, userID)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	tok, err := h.svc.IssueTokens(ctx, u, c.Request().UserAgent())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, toResponse(tok))
}

// subject membaca pengguna dari token akses pada header, atau dari token
// tantangan pendaftaran pada badan permintaan.
func (h *Handler) subject(c echo.Context) (uuid.UUID, bool) {
	var req mfaEnrollRequest
	_ = c.Bind(&req)
	return h.subjectWith(c, req.ChallengeToken)
}

func (h *Handler) subjectWith(c echo.Context, challenge string) (uuid.UUID, bool) {
	if p := httpx.PrincipalFrom(c); p != nil {
		return p.UserID, true
	}
	if challenge != "" {
		if id, err := h.svc.ParseChallenge(challenge, PurposeMFAEnroll); err == nil {
			return id, true
		}
	}
	return uuid.Nil, false
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
	case errors.Is(err, ErrMFARequired), errors.Is(err, ErrMFAEnrollRequired):
		return "MFA_REQUIRED"
	case errors.Is(err, ErrMFACodeInvalid):
		return "MFA_CODE_INVALID"
	case errors.Is(err, ErrMFACodeReplayed):
		return "MFA_CODE_REPLAYED"
	case errors.Is(err, ErrMFANotEnrolled):
		return "MFA_NOT_ENROLLED"
	case errors.Is(err, ErrMFAAlreadyOn):
		return "MFA_ALREADY_ENABLED"
	case errors.Is(err, ErrRefreshReused):
		return "REFRESH_REUSED"
	case errors.Is(err, ErrRefreshInvalid):
		return "REFRESH_INVALID"
	default:
		return "INTERNAL"
	}
}
