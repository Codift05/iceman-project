package order

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/httpx"
	"github.com/iceman/backend/internal/scheduling"
)

// Handler memaparkan pesanan sebagai endpoint HTTP.
type Handler struct {
	orders *Orders
	carts  *cart.Carts
}

// NewHandler membuat handler pesanan.
func NewHandler(o *Orders, c *cart.Carts) *Handler {
	return &Handler{orders: o, carts: c}
}

// List mengembalikan daftar pesanan untuk dashboard.
func (h *Handler) List(c echo.Context) error {
	f := ListFilter{
		Status:        strings.ToUpper(c.QueryParam("status")),
		ScheduledDate: c.QueryParam("scheduled_date"),
	}
	if raw := c.QueryParam("customer_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "customer_id", Message: "Pengenal pelanggan tidak sah."})
		}
		f.CustomerID = &id
	}
	if raw := c.QueryParam("depot_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "depot_id", Message: "Pengenal depo tidak sah."})
		}
		f.DepotID = &id
	}
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Limit = n
		}
	}

	out, err := h.orders.List(c.Request().Context(), f)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"orders": out})
}

// Get mengembalikan satu pesanan beserta isinya.
func (h *Handler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	got, err := h.orders.Get(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	// Tindakan yang mungkin dikirim bersama pesanannya, agar antarmuka tidak
	// perlu menyusun ulang matriks transisi di sisinya.
	return c.JSON(http.StatusOK, map[string]any{
		"order":            got,
		"allowed_statuses": AllowedFrom(got.Status),
	})
}

// History mengembalikan riwayat perubahan status pesanan.
func (h *Handler) History(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	out, err := h.orders.History(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"history": out})
}

// --- keranjang pelanggan, dikelola admin ---

// GetCart mengembalikan keranjang seorang pelanggan.
func (h *Handler) GetCart(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	k, err := h.carts.Get(c.Request().Context(), customerID)
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.JSON(http.StatusOK, cartResponse(k))
}

type cartItemRequest struct {
	ProductID string `json:"product_id"`
	Qty       int32  `json:"qty"`
}

// AddToCart menambah produk ke keranjang pelanggan.
func (h *Handler) AddToCart(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req cartItemRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "product_id", Message: "Pengenal produk tidak sah."})
	}

	k, err := h.carts.Add(c.Request().Context(), customerID, productID, req.Qty)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, cartResponse(k))
}

// SetCartQty menetapkan jumlah satu item. Jumlah nol menghapus barisnya.
func (h *Handler) SetCartQty(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	productID, err := uuid.Parse(c.Param("productID"))
	if err != nil {
		return httpx.Fail(c, "PRODUCT_NOT_FOUND")
	}
	var req cartItemRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	k, err := h.carts.SetQty(c.Request().Context(), customerID, productID, req.Qty)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, cartResponse(k))
}

// cartResponse menambahkan penanda siap checkout pada keranjang, agar
// antarmuka tidak perlu menelusuri seluruh item untuk memutuskannya.
func cartResponse(k *cart.Cart) map[string]any {
	return map[string]any{"cart": k, "checkoutable": k.Checkoutable()}
}

// --- checkout ---

type checkoutRequest struct {
	CustomerID     string `json:"customer_id"`
	AddressID      string `json:"address_id"`
	SlotID         string `json:"slot_id"`
	Notes          string `json:"notes"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Checkout membuat pesanan dari keranjang pelanggan.
//
// Dipakai admin untuk pesanan manual (SRS-ORD-006), dengan aturan yang sama
// persis seperti pesanan dari aplikasi. Kunci idempotensi juga dibaca dari
// header Idempotency-Key, karena itu tempat yang biasa dipakai klien HTTP.
func (h *Handler) Checkout(c echo.Context) error {
	var req checkoutRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	customerID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "customer_id", Message: "Pengenal pelanggan tidak sah."})
	}
	addressID, err := uuid.Parse(req.AddressID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "address_id", Message: "Pengenal alamat tidak sah."})
	}
	slotID, err := uuid.Parse(req.SlotID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "slot_id", Message: "Pengenal jadwal tidak sah."})
	}

	kunci := req.IdempotencyKey
	if kunci == "" {
		kunci = c.Request().Header.Get("Idempotency-Key")
	}

	pelaku := pelakuDari(c)
	ctx := c.Request().Context()
	got, err := h.orders.Checkout(ctx, CheckoutInput{
		CustomerID: customerID, AddressID: addressID, SlotID: slotID,
		Notes: req.Notes, IdempotencyKey: kunci,
		Channel: ChannelWhatsApp, CreatedBy: pelaku,
	})
	if err != nil {
		return h.failCheckout(c, slotID, err)
	}
	return c.JSON(http.StatusCreated, got)
}

// failCheckout membalas galat checkout, dan menyertakan tawaran jadwal
// terdekat ketika slotnya penuh.
//
// Tawaran itu diwajibkan diagram alur checkout pada SRS: pelanggan yang
// slotnya penuh perlu tahu kapan ia bisa, bukan hanya bahwa ia tidak bisa.
// Tanpa itu, satu satunya jalan adalah mencoba slot satu per satu.
func (h *Handler) failCheckout(c echo.Context, slotID uuid.UUID, err error) error {
	kode := codeFor(err)
	if kode != "SLOT_FULL" {
		return httpx.Fail(c, kode, detailFor(err)...)
	}

	badan := httpx.Body{
		Code:      kode,
		Message:   "Slot penuh. Pilih waktu lain.",
		RequestID: httpx.RequestIDFrom(c),
	}
	jawab := map[string]any{"error": badan}
	if tawaran := h.orders.NextAvailableFor(c.Request().Context(), slotID); tawaran != nil {
		jawab["next_available"] = tawaran
	}
	return c.JSON(http.StatusConflict, jawab)
}

type statusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// ChangeStatus memindahkan pesanan ke status lain.
func (h *Handler) ChangeStatus(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req statusRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if req.Status == "" {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "status", Message: "Status tujuan wajib diisi."})
	}

	got, err := h.orders.ChangeStatus(c.Request().Context(), id,
		strings.ToUpper(req.Status), ChangeInput{Reason: req.Reason})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, got)
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

// Cancel membatalkan pesanan.
func (h *Handler) Cancel(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req cancelRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	got, err := h.orders.Cancel(c.Request().Context(), id, req.Reason)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, got)
}

type rescheduleRequest struct {
	SlotID string `json:"slot_id"`
	Reason string `json:"reason"`
}

// Reschedule memindahkan pesanan ke slot lain.
func (h *Handler) Reschedule(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req rescheduleRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	slotID, err := uuid.Parse(req.SlotID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "slot_id", Message: "Pengenal jadwal tidak sah."})
	}

	got, err := h.orders.Reschedule(c.Request().Context(), id, slotID, req.Reason)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, got)
}

// Reorder menyusun keranjang baru dari pesanan sebelumnya.
//
// Daftar item yang gagal selalu disertakan, baik saat berhasil sebagian maupun
// saat seluruhnya gagal, karena pelanggan perlu tahu produk apa yang hilang.
func (h *Handler) Reorder(c echo.Context) error {
	customerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	orderID, err := uuid.Parse(c.Param("orderID"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}

	hasil, err := h.orders.Reorder(c.Request().Context(), customerID, orderID)
	if errors.Is(err, ErrItemsUnavailable) {
		return c.JSON(http.StatusConflict, httpx.Envelope{Error: httpx.Body{
			Code:      "REORDER_ITEMS_UNAVAILABLE",
			Message:   "Tidak ada produk pada pesanan itu yang masih dapat dipesan.",
			RequestID: httpx.RequestIDFrom(c),
			Details:   detailDilewati(hasil),
		}})
	}
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, hasil)
}

// detailDilewati menerjemahkan daftar item yang gagal menjadi rincian galat,
// agar antarmuka dapat menyebut nama produknya satu per satu.
func detailDilewati(hasil *ReorderResult) []httpx.Detail {
	if hasil == nil {
		return nil
	}
	out := make([]httpx.Detail, 0, len(hasil.Skipped))
	for _, s := range hasil.Skipped {
		nama := s.Name
		if nama == "" {
			nama = s.ProductID.String()
		}
		out = append(out, httpx.Detail{Field: "items", Message: nama + " tidak dapat dipesan lagi."})
	}
	return out
}

// pelakuDari membaca pengguna internal yang sedang masuk.
func pelakuDari(c echo.Context) *uuid.UUID {
	if p := httpx.PrincipalFrom(c); p != nil {
		id := p.UserID
		return &id
	}
	return nil
}

// codeFor menerjemahkan galat domain menjadi kode galat API.
//
// Galat dari paket lain ikut diterjemahkan di sini karena checkout memang
// memanggil mereka: keranjang, pelanggan, katalog, dan penjadwalan. Pemanggil
// HTTP tidak perlu tahu galat itu berasal dari paket mana.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrCartEmpty):
		return "CART_EMPTY"
	case errors.Is(err, ErrItemsUnavailable), errors.Is(err, cart.ErrProductUnusable):
		return "PRODUCT_UNAVAILABLE"
	case errors.Is(err, ErrMinOrderNotMet):
		return "MIN_ORDER_NOT_MET"
	case errors.Is(err, ErrSlotAreaMismatch):
		return "SLOT_AREA_MISMATCH"
	case errors.Is(err, ErrInvalidTransition):
		return "INVALID_STATE_TRANSITION"
	case errors.Is(err, ErrReasonRequired):
		return "REASON_REQUIRED"

	case errors.Is(err, customer.ErrAddressNotFound), errors.Is(err, customer.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, customer.ErrAreaInactive):
		return "AREA_NOT_SERVED"
	case errors.Is(err, customer.ErrCreditLimit):
		return "CREDIT_LIMIT_EXCEEDED"

	case errors.Is(err, catalog.ErrProductNotFound):
		return "PRODUCT_NOT_FOUND"

	case errors.Is(err, scheduling.ErrSlotNotFound):
		return "NOT_FOUND"
	case errors.Is(err, scheduling.ErrSlotFull):
		return "SLOT_FULL"
	case errors.Is(err, scheduling.ErrSlotCutoffPassed):
		return "SLOT_CUTOFF_PASSED"
	case errors.Is(err, scheduling.ErrSlotUnavailable):
		return "SLOT_UNAVAILABLE"

	case errors.Is(err, cart.ErrItemNotFound):
		return "NOT_FOUND"
	case errors.Is(err, cart.ErrQtyNegative):
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrReasonRequired):
		return []httpx.Detail{{Field: "reason", Message: "Alasan wajib diisi."}}
	case errors.Is(err, ErrSlotAreaMismatch):
		return []httpx.Detail{{Field: "slot_id", Message: "Jadwal ini tidak melayani alamat tersebut."}}
	case errors.Is(err, cart.ErrQtyNegative):
		return []httpx.Detail{{Field: "qty", Message: "Jumlah tidak boleh negatif."}}
	case errors.Is(err, customer.ErrCreditLimit):
		return []httpx.Detail{{Field: "customer_id", Message: err.Error()}}
	default:
		return nil
	}
}
