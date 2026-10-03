package scheduling

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// Handler memaparkan pengelolaan depo, area, dan slot sebagai endpoint HTTP.
type Handler struct {
	depots *Depots
	areas  *Areas
	slots  *Slots
}

// NewHandler membuat handler penjadwalan.
func NewHandler(d *Depots, a *Areas, s *Slots) *Handler {
	return &Handler{depots: d, areas: a, slots: s}
}

// --- Depo ---

// ListDepots mengembalikan daftar depo. Secara baku hanya depo aktif, karena
// itulah yang dipakai operasional sehari hari; depo lama tetap dapat dilihat
// dengan menyertakan include_inactive untuk keperluan riwayat.
func (h *Handler) ListDepots(c echo.Context) error {
	aktifSaja := c.QueryParam("include_inactive") != "true"
	out, err := h.depots.List(c.Request().Context(), aktifSaja)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"depots": out})
}

// GetDepot mengembalikan satu depo.
func (h *Handler) GetDepot(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	dep, err := h.depots.Get(c.Request().Context(), id)
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.JSON(http.StatusOK, dep)
}

type depotRequest struct {
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Address         string  `json:"address"`
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	ServiceRadiusKm float64 `json:"service_radius_km"`
	IsActive        *bool   `json:"is_active"`
}

func (r depotRequest) toInput() DepotInput {
	return DepotInput{
		Code: r.Code, Name: r.Name, Address: r.Address,
		Latitude: r.Latitude, Longitude: r.Longitude,
		ServiceRadiusKm: r.ServiceRadiusKm, IsActive: r.IsActive,
	}
}

// CreateDepot menambah depo baru.
func (h *Handler) CreateDepot(c echo.Context) error {
	var req depotRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if d := wajibDepot(req); len(d) > 0 {
		return httpx.Fail(c, "VALIDATION_FAILED", d...)
	}
	dep, err := h.depots.Create(c.Request().Context(), req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, dep)
}

// UpdateDepot mengubah depo yang ada.
func (h *Handler) UpdateDepot(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req depotRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if d := wajibDepot(req); len(d) > 0 {
		return httpx.Fail(c, "VALIDATION_FAILED", d...)
	}
	dep, err := h.depots.Update(c.Request().Context(), id, req.toInput())
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, dep)
}

func wajibDepot(r depotRequest) []httpx.Detail {
	var d []httpx.Detail
	if r.Code == "" {
		d = append(d, httpx.Detail{Field: "code", Message: "Kode depo wajib diisi."})
	}
	if r.Name == "" {
		d = append(d, httpx.Detail{Field: "name", Message: "Nama depo wajib diisi."})
	}
	if r.ServiceRadiusKm <= 0 {
		d = append(d, httpx.Detail{Field: "service_radius_km", Message: "Radius layanan harus lebih dari nol."})
	}
	return d
}

// NearestDepot menentukan depo yang melayani sebuah titik koordinat.
//
// Endpoint ini terbuka tanpa token karena dipakai aplikasi pelanggan sebelum
// masuk, saat memeriksa apakah alamatnya terjangkau. Yang dikembalikan hanya
// identitas depo dan jaraknya, bukan data operasional.
func (h *Handler) NearestDepot(c echo.Context) error {
	lat, errLat := strconv.ParseFloat(c.QueryParam("lat"), 64)
	lng, errLng := strconv.ParseFloat(c.QueryParam("lng"), 64)
	if errLat != nil || errLng != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "lat", Message: "Koordinat lat dan lng wajib diisi berupa angka."})
	}

	res, err := h.depots.Resolve(c.Request().Context(), lat, lng)
	if err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.JSON(http.StatusOK, res)
}

// --- Area layanan ---

// ListAreas mengembalikan area layanan, boleh disaring menurut depo.
func (h *Handler) ListAreas(c echo.Context) error {
	var depotID *uuid.UUID
	if raw := c.QueryParam("depot_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "depot_id", Message: "Pengenal depo tidak sah."})
		}
		depotID = &id
	}
	aktifSaja := c.QueryParam("include_inactive") != "true"

	out, err := h.areas.List(c.Request().Context(), depotID, aktifSaja)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"areas": out})
}

type areaRequest struct {
	DepotID          string `json:"depot_id"`
	Name             string `json:"name"`
	DeliveryFeeCents int64  `json:"delivery_fee_cents"`
	IsActive         *bool  `json:"is_active"`
}

// CreateArea menambah area layanan.
func (h *Handler) CreateArea(c echo.Context) error {
	var req areaRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	depotID, err := uuid.Parse(req.DepotID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "depot_id", Message: "Pengenal depo tidak sah."})
	}
	if req.Name == "" {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "name", Message: "Nama area wajib diisi."})
	}

	area, err := h.areas.Create(c.Request().Context(), AreaInput{
		DepotID: depotID, Name: req.Name,
		DeliveryFeeCents: req.DeliveryFeeCents, IsActive: req.IsActive,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, area)
}

// UpdateArea mengubah area layanan.
func (h *Handler) UpdateArea(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req areaRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	area, err := h.areas.Update(c.Request().Context(), id, AreaInput{
		Name: req.Name, DeliveryFeeCents: req.DeliveryFeeCents, IsActive: req.IsActive,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, area)
}

// --- Slot pengiriman ---

// ListSlots mengembalikan slot sebuah area untuk tampilan admin, lengkap
// dengan kapasitas dan jumlah terpakai.
func (h *Handler) ListSlots(c echo.Context) error {
	areaID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	from, err := tanggalAtauHariIni(c.QueryParam("from"))
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "from", Message: "Tanggal harus berbentuk YYYY-MM-DD."})
	}

	out, err := h.slots.ListForArea(c.Request().Context(), areaID, from, angka(c.QueryParam("days")))
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"slots": out})
}

type slotRequest struct {
	ServiceAreaID string `json:"service_area_id"`
	Date          string `json:"date"`
	WindowStart   string `json:"window_start"`
	WindowEnd     string `json:"window_end"`
	Capacity      int32  `json:"capacity"`
	CutoffAt      string `json:"cutoff_at"`
	IsHoliday     bool   `json:"is_holiday"`
}

// CreateSlot menambah satu slot secara manual, untuk jendela di luar pola
// harian, misalnya pengiriman tambahan menjelang hari besar.
func (h *Handler) CreateSlot(c echo.Context) error {
	var req slotRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	areaID, err := uuid.Parse(req.ServiceAreaID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "service_area_id", Message: "Pengenal area tidak sah."})
	}
	tanggal, err := time.ParseInLocation("2006-01-02", req.Date, time.Local)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "date", Message: "Tanggal harus berbentuk YYYY-MM-DD."})
	}
	cutoff, err := time.Parse(time.RFC3339, req.CutoffAt)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "cutoff_at", Message: "Batas pemesanan harus berbentuk waktu RFC3339."})
	}
	if d := wajibJendela(req); len(d) > 0 {
		return httpx.Fail(c, "VALIDATION_FAILED", d...)
	}

	slot, err := h.slots.Create(c.Request().Context(), SlotInput{
		ServiceAreaID: areaID, Date: tanggal,
		WindowStart: req.WindowStart, WindowEnd: req.WindowEnd,
		Capacity: req.Capacity, CutoffAt: cutoff, IsHoliday: req.IsHoliday,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, slot)
}

func wajibJendela(r slotRequest) []httpx.Detail {
	var d []httpx.Detail
	for _, f := range []struct{ nama, nilai string }{
		{"window_start", r.WindowStart}, {"window_end", r.WindowEnd},
	} {
		if _, err := time.Parse("15:04", f.nilai); err != nil {
			d = append(d, httpx.Detail{Field: f.nama, Message: "Jam harus berbentuk HH:MM."})
		}
	}
	if r.WindowStart >= r.WindowEnd && len(d) == 0 {
		d = append(d, httpx.Detail{Field: "window_end", Message: "Jam akhir harus setelah jam mulai."})
	}
	if r.Capacity < 0 {
		d = append(d, httpx.Detail{Field: "capacity", Message: "Kapasitas tidak boleh negatif."})
	}
	return d
}

type capacityRequest struct {
	Capacity int32 `json:"capacity"`
}

// SetSlotCapacity mengubah kuota sebuah slot.
func (h *Handler) SetSlotCapacity(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req capacityRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.slots.SetCapacity(c.Request().Context(), id, req.Capacity); err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.NoContent(http.StatusNoContent)
}

type holidayRequest struct {
	IsHoliday bool `json:"is_holiday"`
}

// SetSlotHoliday menandai atau membatalkan hari libur pada sebuah slot.
func (h *Handler) SetSlotHoliday(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req holidayRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.slots.SetHoliday(c.Request().Context(), id, req.IsHoliday); err != nil {
		return httpx.Fail(c, codeFor(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// Availability mengembalikan slot yang dilihat pelanggan.
//
// Slot yang tidak dapat dipilih tetap dikirim beserta alasannya, bukan
// disembunyikan (UI/UX Bab 10.1).
func (h *Handler) Availability(c echo.Context) error {
	areaID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	from, err := tanggalAtauHariIni(c.QueryParam("from"))
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "from", Message: "Tanggal harus berbentuk YYYY-MM-DD."})
	}

	out, err := h.slots.Availability(c.Request().Context(), areaID,
		from, angka(c.QueryParam("days")), time.Now())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"slots": out})
}

// NextAvailable menawarkan slot terdekat yang masih dapat dipilih. Dipakai
// untuk memberi jalan keluar ketika pilihan pelanggan ternyata sudah penuh.
func (h *Handler) NextAvailable(c echo.Context) error {
	areaID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	got, err := h.slots.NextAvailable(c.Request().Context(), areaID, time.Now())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	if got == nil {
		return httpx.Fail(c, "SLOT_UNAVAILABLE")
	}
	return c.JSON(http.StatusOK, got)
}

// --- Penerjemah galat ---

// codeFor menerjemahkan galat domain menjadi kode galat API.
//
// Galat yang tidak dikenali menjadi INTERNAL, bukan dibocorkan apa adanya,
// supaya pesan basis data tidak sampai ke klien.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrDepotNotFound), errors.Is(err, ErrAreaNotFound), errors.Is(err, ErrSlotNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrNoDepotServes):
		return "AREA_NOT_SERVED"
	case errors.Is(err, ErrDepotCodeExists), errors.Is(err, ErrAreaNameExists), errors.Is(err, ErrSlotExists):
		return "CONFLICT"
	case errors.Is(err, ErrDepotInUse), errors.Is(err, ErrAreaDepotOff):
		return "CONFLICT"
	case errors.Is(err, ErrCapacityBelowUsed), errors.Is(err, ErrCapacityNegative),
		errors.Is(err, ErrFeeNegative), errors.Is(err, ErrCoordinateRange):
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

// detailFor menjelaskan galat domain pada tingkat kolom, supaya antarmuka dapat
// menandai isian yang salah alih alih hanya menampilkan pesan umum.
func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrDepotCodeExists):
		return []httpx.Detail{{Field: "code", Message: "Kode depo ini sudah dipakai."}}
	case errors.Is(err, ErrAreaNameExists):
		return []httpx.Detail{{Field: "name", Message: "Nama area ini sudah dipakai pada depo tersebut."}}
	case errors.Is(err, ErrAreaDepotOff):
		return []httpx.Detail{{Field: "depot_id", Message: "Depo ini tidak aktif."}}
	case errors.Is(err, ErrFeeNegative):
		return []httpx.Detail{{Field: "delivery_fee_cents", Message: "Ongkos kirim tidak boleh negatif."}}
	case errors.Is(err, ErrCoordinateRange):
		return []httpx.Detail{{Field: "latitude", Message: "Koordinat di luar rentang yang sah."}}
	case errors.Is(err, ErrCapacityBelowUsed):
		return []httpx.Detail{{Field: "capacity", Message: "Kapasitas tidak boleh di bawah jumlah yang sudah terpakai."}}
	case errors.Is(err, ErrCapacityNegative):
		return []httpx.Detail{{Field: "capacity", Message: "Kapasitas tidak boleh negatif."}}
	default:
		return nil
	}
}

// --- Pembantu penguraian query ---

// tanggalAtauHariIni menguraikan parameter tanggal, kosong berarti hari ini.
func tanggalAtauHariIni(raw string) (time.Time, error) {
	if raw == "" {
		n := time.Now()
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local), nil
	}
	return time.ParseInLocation("2006-01-02", raw, time.Local)
}

// angka menguraikan parameter jumlah hari. Nilai yang tidak sah dikembalikan
// sebagai nol agar lapisan domain memakai nilai bawaannya sendiri.
func angka(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}
