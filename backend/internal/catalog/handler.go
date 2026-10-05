package catalog

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// Handler memaparkan katalog sebagai endpoint HTTP.
type Handler struct{ products *Products }

// NewHandler membuat handler katalog.
func NewHandler(p *Products) *Handler { return &Handler{products: p} }

// List mengembalikan daftar produk untuk tampilan admin, termasuk produk yang
// sudah tidak aktif bila diminta.
func (h *Handler) List(c echo.Context) error {
	out, err := h.products.List(c.Request().Context(), Filter{
		Category:        c.QueryParam("category"),
		Search:          c.QueryParam("q"),
		IncludeInactive: c.QueryParam("include_inactive") == "true",
	})
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"products": out})
}

// Catalog mengembalikan katalog sebagaimana dilihat pelanggan.
//
// Pengenal pelanggan boleh disertakan agar harga khususnya yang tampil. Tanpa
// itu, yang tampil harga dasar.
func (h *Handler) Catalog(c echo.Context) error {
	var customerID *uuid.UUID
	if raw := c.QueryParam("customer_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "customer_id", Message: "Pengenal pelanggan tidak sah."})
		}
		customerID = &id
	}

	out, err := h.products.CatalogFor(c.Request().Context(), customerID, Filter{
		Category: c.QueryParam("category"),
		Search:   c.QueryParam("q"),
	})
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"products": out})
}

// Get mengembalikan satu produk.
func (h *Handler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "PRODUCT_NOT_FOUND")
	}
	prod, err := h.products.Get(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, prod)
}

type productRequest struct {
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	Packaging      string `json:"packaging"`
	BasePriceCents int64  `json:"base_price_cents"`
	MinOrderQty    int32  `json:"min_order_qty"`
	IsAvailable    *bool  `json:"is_available"`
	IsActive       *bool  `json:"is_active"`
	PhotoURL       string `json:"photo_url"`
}

func (r productRequest) toInput() ProductInput {
	return ProductInput{
		SKU: r.SKU, Name: r.Name, Category: r.Category, Packaging: r.Packaging,
		BasePriceCents: r.BasePriceCents, MinOrderQty: r.MinOrderQty,
		IsAvailable: r.IsAvailable, IsActive: r.IsActive, PhotoURL: r.PhotoURL,
	}
}

// Create menambah produk.
func (h *Handler) Create(c echo.Context) error {
	var req productRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	// Minimum order satu adalah nilai bawaan yang masuk akal, bukan galat.
	// Memaksanya diisi membuat setiap permintaan pembuatan produk harus
	// menyebut angka yang hampir selalu satu.
	if req.MinOrderQty == 0 {
		req.MinOrderQty = 1
	}
	prod, err := h.products.Create(c.Request().Context(), req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, prod)
}

// Update mengubah produk.
func (h *Handler) Update(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "PRODUCT_NOT_FOUND")
	}
	var req productRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if req.MinOrderQty == 0 {
		req.MinOrderQty = 1
	}
	prod, err := h.products.Update(c.Request().Context(), id, req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, prod)
}

type contractPriceRequest struct {
	CustomerID string `json:"customer_id"`
	PriceCents int64  `json:"price_cents"`
	ValidFrom  string `json:"valid_from"`
}

// SetContractPrice menetapkan harga khusus seorang pelanggan untuk satu produk.
func (h *Handler) SetContractPrice(c echo.Context) error {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "PRODUCT_NOT_FOUND")
	}
	var req contractPriceRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	customerID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "customer_id", Message: "Pengenal pelanggan tidak sah."})
	}
	if req.ValidFrom == "" {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "valid_from", Message: "Tanggal mulai berlaku wajib diisi."})
	}

	err = h.products.SetContractPrice(c.Request().Context(),
		customerID, productID, req.PriceCents, req.ValidFrom)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.NoContent(http.StatusNoContent)
}

// codeFor menerjemahkan galat domain katalog menjadi kode galat API.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrProductNotFound):
		return "PRODUCT_NOT_FOUND"
	case errors.Is(err, ErrSKUExists):
		return "SKU_ALREADY_EXISTS"
	case errors.Is(err, ErrPriceNegative), errors.Is(err, ErrMinOrderTooLow),
		errors.Is(err, ErrNameRequired), errors.Is(err, ErrSKURequired):
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrSKUExists):
		return []httpx.Detail{{Field: "sku", Message: "Kode produk ini sudah dipakai."}}
	case errors.Is(err, ErrSKURequired):
		return []httpx.Detail{{Field: "sku", Message: "Kode produk wajib diisi."}}
	case errors.Is(err, ErrNameRequired):
		return []httpx.Detail{{Field: "name", Message: "Nama produk wajib diisi."}}
	case errors.Is(err, ErrPriceNegative):
		return []httpx.Detail{{Field: "base_price_cents", Message: "Harga tidak boleh negatif."}}
	case errors.Is(err, ErrMinOrderTooLow):
		return []httpx.Detail{{Field: "min_order_qty", Message: "Minimum order minimal satu."}}
	default:
		return nil
	}
}
