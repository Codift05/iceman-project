package customer

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// Handler memaparkan pengelolaan pelanggan sebagai endpoint HTTP.
type Handler struct{ customers *Customers }

// NewHandler membuat handler pelanggan.
func NewHandler(c *Customers) *Handler { return &Handler{customers: c} }

// List mengembalikan daftar pelanggan.
func (h *Handler) List(c echo.Context) error {
	out, err := h.customers.List(c.Request().Context(), Filter{
		Search:          c.QueryParam("q"),
		Type:            strings.ToUpper(c.QueryParam("type")),
		IncludeInactive: c.QueryParam("include_inactive") == "true",
	})
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"customers": out})
}

// Get mengembalikan satu pelanggan.
func (h *Handler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	cust, err := h.customers.Get(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, cust)
}

type customerRequest struct {
	Phone    string `json:"phone"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Type     string `json:"type"`
	IsActive *bool  `json:"is_active"`
}

func (r customerRequest) toInput() Input {
	return Input{
		Phone: r.Phone, Name: r.Name, Email: r.Email,
		Type: strings.ToUpper(r.Type), IsActive: r.IsActive,
	}
}

// Create menambah pelanggan. Dipakai admin saat menerima pesanan dari
// pelanggan baru lewat telepon (SRS-ORD-006).
func (h *Handler) Create(c echo.Context) error {
	var req customerRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	cust, err := h.customers.Create(c.Request().Context(), req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, cust)
}

// Update mengubah pelanggan.
func (h *Handler) Update(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req customerRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	cust, err := h.customers.Update(c.Request().Context(), id, req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, cust)
}

// ListAddresses mengembalikan alamat pelanggan.
func (h *Handler) ListAddresses(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	out, err := h.customers.ListAddresses(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"addresses": out})
}

type addressRequest struct {
	ServiceAreaID string  `json:"service_area_id"`
	Label         string  `json:"label"`
	RecipientName string  `json:"recipient_name"`
	Phone         string  `json:"phone"`
	AddressLine   string  `json:"address_line"`
	Notes         string  `json:"notes"`
	Latitude      float64 `json:"latitude"`
	Longitude     float64 `json:"longitude"`
}

// AddAddress menambah alamat pelanggan.
func (h *Handler) AddAddress(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req addressRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	areaID, err := uuid.Parse(req.ServiceAreaID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "service_area_id", Message: "Pengenal wilayah layanan tidak sah."})
	}

	alamat, err := h.customers.AddAddress(c.Request().Context(), customerID, AddressInput{
		ServiceAreaID: areaID, Label: req.Label,
		RecipientName: req.RecipientName, Phone: req.Phone,
		AddressLine: req.AddressLine, Notes: req.Notes,
		Latitude: req.Latitude, Longitude: req.Longitude,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, alamat)
}

// SetPrimaryAddress menjadikan satu alamat sebagai alamat utama.
func (h *Handler) SetPrimaryAddress(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	addressID, err := uuid.Parse(c.Param("addressID"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	if err := h.customers.SetPrimaryAddress(c.Request().Context(), customerID, addressID); err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// DeactivateAddress menonaktifkan alamat.
func (h *Handler) DeactivateAddress(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	addressID, err := uuid.Parse(c.Param("addressID"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	if err := h.customers.DeactivateAddress(c.Request().Context(), customerID, addressID); err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.NoContent(http.StatusNoContent)
}

type termRequest struct {
	PaymentTermDays  int32  `json:"payment_term_days"`
	CreditLimitCents int64  `json:"credit_limit_cents"`
	ValidFrom        string `json:"valid_from"`
	ValidUntil       string `json:"valid_until"`
}

// SetContractTerm menetapkan termin pembayaran pelanggan kontrak.
func (h *Handler) SetContractTerm(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req termRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	in := TermInput{
		PaymentTermDays:  req.PaymentTermDays,
		CreditLimitCents: req.CreditLimitCents,
	}
	if req.ValidFrom != "" {
		t, err := time.ParseInLocation("2006-01-02", req.ValidFrom, time.Local)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "valid_from", Message: "Tanggal harus berbentuk YYYY-MM-DD."})
		}
		in.ValidFrom = t
	}
	if req.ValidUntil != "" {
		t, err := time.ParseInLocation("2006-01-02", req.ValidUntil, time.Local)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "valid_until", Message: "Tanggal harus berbentuk YYYY-MM-DD."})
		}
		in.ValidUntil = &t
	}

	term, err := h.customers.SetContractTerm(c.Request().Context(), customerID, in)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, term)
}

// codeFor menerjemahkan galat domain pelanggan menjadi kode galat API.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrAddressNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrPhoneExists):
		return "PHONE_ALREADY_EXISTS"
	case errors.Is(err, ErrAreaInactive):
		return "AREA_NOT_SERVED"
	case errors.Is(err, ErrCreditLimit):
		return "CREDIT_LIMIT_EXCEEDED"
	case errors.Is(err, ErrPhoneRequired), errors.Is(err, ErrNameRequired),
		errors.Is(err, ErrCoordRange), errors.Is(err, ErrTermInvalid),
		errors.Is(err, ErrTypeUnknown):
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrPhoneExists):
		return []httpx.Detail{{Field: "phone", Message: "Nomor telepon ini sudah terdaftar."}}
	case errors.Is(err, ErrPhoneRequired):
		return []httpx.Detail{{Field: "phone", Message: "Nomor telepon wajib diisi."}}
	case errors.Is(err, ErrNameRequired):
		return []httpx.Detail{{Field: "name", Message: "Nama wajib diisi."}}
	case errors.Is(err, ErrCoordRange):
		return []httpx.Detail{{Field: "latitude", Message: "Koordinat di luar rentang yang sah."}}
	case errors.Is(err, ErrTermInvalid):
		return []httpx.Detail{{Field: "payment_term_days", Message: "Termin pembayaran tidak sah."}}
	case errors.Is(err, ErrTypeUnknown):
		return []httpx.Detail{{Field: "type",
			Message: "Jenis pelanggan harus RETAIL, BUSINESS, atau CONTRACT."}}
	case errors.Is(err, ErrAreaInactive):
		return []httpx.Detail{{Field: "service_area_id", Message: "Wilayah layanan ini tidak aktif."}}
	default:
		return nil
	}
}

// Credit mengembalikan keadaan piutang seorang pelanggan.
//
// Dipakai tampilan admin agar petugas dapat melihat sisa plafon sebelum
// menerima pesanan lewat telepon, bukan menunggu penolakan saat menyimpannya.
func (h *Handler) Credit(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	out, err := h.customers.Credit(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, out)
}
