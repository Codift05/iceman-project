package delivery

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
	"github.com/iceman/backend/internal/order"
)

// Handler memaparkan pengiriman sebagai endpoint HTTP.
type Handler struct{ deliveries *Deliveries }

// NewHandler membuat handler pengiriman.
func NewHandler(d *Deliveries) *Handler { return &Handler{deliveries: d} }

// --- sisi admin ---

// List mengembalikan daftar pengiriman untuk dashboard.
func (h *Handler) List(c echo.Context) error {
	f := ListFilter{
		Status: strings.ToUpper(c.QueryParam("status")),
		Date:   c.QueryParam("date"),
	}
	if raw := c.QueryParam("driver_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "driver_id", Message: "Pengenal driver tidak sah."})
		}
		f.DriverID = &id
	}
	if raw := c.QueryParam("depot_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "depot_id", Message: "Pengenal depo tidak sah."})
		}
		f.DepotID = &id
	}

	out, err := h.deliveries.List(c.Request().Context(), f)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"deliveries": out})
}

// Get mengembalikan satu pengiriman lengkap dengan bukti dan kendalanya.
func (h *Handler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	ctx := c.Request().Context()

	got, err := h.deliveries.Get(ctx, id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	bukti, err := h.deliveries.Proof(ctx, id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	kendala, err := h.deliveries.Issues(ctx, id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}

	// Tindakan yang mungkin dikirim bersama datanya, agar antarmuka tidak
	// perlu menyusun ulang matriks transisi di sisinya.
	return c.JSON(http.StatusOK, map[string]any{
		"delivery":         got,
		"proof":            bukti,
		"issues":           kendala,
		"allowed_statuses": AllowedFrom(got.Status),
	})
}

// History mengembalikan riwayat status dan penugasan.
func (h *Handler) History(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	ctx := c.Request().Context()

	status, err := h.deliveries.History(ctx, id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	tugas, err := h.deliveries.Assignments(ctx, id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"status_history":     status,
		"assignment_history": tugas,
	})
}

type assignRequest struct {
	OrderID    string `json:"order_id"`
	DriverID   string `json:"driver_id"`
	SequenceNo int32  `json:"sequence_no"`
	Reason     string `json:"reason"`
}

// Assign menugaskan driver pada sebuah pesanan.
func (h *Handler) Assign(c echo.Context) error {
	var req assignRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "order_id", Message: "Pengenal pesanan tidak sah."})
	}
	driverID, err := uuid.Parse(req.DriverID)
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "driver_id", Message: "Pengenal driver tidak sah."})
	}

	got, err := h.deliveries.Assign(c.Request().Context(), AssignInput{
		OrderID: orderID, DriverID: driverID,
		SequenceNo: req.SequenceNo, Reason: req.Reason,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, got)
}

type sequenceRequest struct {
	SequenceNo int32 `json:"sequence_no"`
}

// SetSequence mengubah urutan pengiriman.
func (h *Handler) SetSequence(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req sequenceRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.deliveries.SetSequence(c.Request().Context(), id, req.SequenceNo); err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.NoContent(http.StatusNoContent)
}

// LiveMap mengembalikan posisi driver yang sedang mengantar pada satu depo.
//
// Depo wajib disebutkan, bukan opsional, karena admin hanya boleh melihat
// driver dari depo yang menjadi kewenangannya (SRS-TRK-002). Membiarkannya
// kosong akan menampilkan seluruh depo.
func (h *Handler) LiveMap(c echo.Context) error {
	depotID, err := uuid.Parse(c.QueryParam("depot_id"))
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "depot_id", Message: "Pengenal depo wajib diisi."})
	}
	out, err := h.deliveries.LivePositions(c.Request().Context(), depotID, time.Now())
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"positions": out,
		// Batas usang dikirim agar antarmuka memakai ambang yang sama dengan
		// server, bukan menebaknya sendiri.
		"stale_after_seconds": int(StaleAfter.Seconds()),
	})
}

// Trail mengembalikan jejak posisi satu pengiriman.
func (h *Handler) Trail(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	batas := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			batas = n
		}
	}
	out, err := h.deliveries.Trail(c.Request().Context(), id, batas)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, map[string]any{"positions": out})
}

// --- sisi driver ---

// MyTasks mengembalikan daftar tugas driver yang sedang masuk.
func (h *Handler) MyTasks(c echo.Context) error {
	drv := pelakuDari(c)
	if drv == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	ctx := c.Request().Context()

	out, err := h.deliveries.List(ctx, ListFilter{
		DriverID: drv,
		Status:   strings.ToUpper(c.QueryParam("status")),
		Date:     c.QueryParam("date"),
	})
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}

	// Jumlah konflik sinkronisasi ditampilkan pada daftar agar driver tahu ada
	// perintahnya yang ditolak server, bukan mengira semuanya sudah tersimpan
	// (SRS-DLV-002).
	konflik, err := h.deliveries.PendingSyncCount(ctx, *drv, time.Now().AddDate(0, 0, -7))
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	setuju, err := h.deliveries.HasConsent(ctx, *drv)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"deliveries":       out,
		"sync_conflicts":   konflik,
		"tracking_consent": setuju,
	})
}

// MyTask mengembalikan satu tugas milik driver yang sedang masuk.
func (h *Handler) MyTask(c echo.Context) error {
	drv := pelakuDari(c)
	if drv == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}

	got, err := h.deliveries.GetForDriver(c.Request().Context(), *drv, id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"delivery":         got,
		"allowed_statuses": AllowedFrom(got.Status),
	})
}

type statusRequest struct {
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	DeviceTime *time.Time `json:"device_time"`
}

// ChangeStatus memindahkan pengiriman ke status lain.
//
// Dipakai driver maupun admin. Yang membedakan keduanya bukan endpointnya,
// melainkan matriks transisinya: perpindahan yang menandai ByAssignedDriver
// hanya berhasil bila pelakunya driver yang ditugaskan.
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

	got, err := h.deliveries.ChangeStatus(c.Request().Context(), id,
		strings.ToUpper(req.Status), ChangeInput{
			ActorID: pelakuDari(c), DeviceTime: req.DeviceTime, Reason: req.Reason,
		})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, got)
}

type proofRequest struct {
	PhotoKey     string     `json:"photo_key"`
	ReceiverName string     `json:"receiver_name"`
	Notes        string     `json:"notes"`
	DeviceTime   *time.Time `json:"device_time"`
}

// SaveProof menyimpan bukti serah terima.
func (h *Handler) SaveProof(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req proofRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	got, err := h.deliveries.SaveProof(c.Request().Context(), id, ProofInput{
		PhotoKey: req.PhotoKey, ReceiverName: req.ReceiverName, Notes: req.Notes,
		DeviceTime: req.DeviceTime, ActorID: pelakuDari(c),
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, got)
}

type issueRequest struct {
	Category   string     `json:"category"`
	Note       string     `json:"note"`
	PhotoKey   string     `json:"photo_key"`
	DeviceTime *time.Time `json:"device_time"`
}

// ReportIssue mencatat kendala pengiriman.
func (h *Handler) ReportIssue(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req issueRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	got, err := h.deliveries.ReportIssue(c.Request().Context(), id, IssueInput{
		Category: req.Category, Note: req.Note, PhotoKey: req.PhotoKey,
		DeviceTime: req.DeviceTime, ActorID: pelakuDari(c),
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, got)
}

type fixRequest struct {
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	AccuracyM  *float64  `json:"accuracy_m"`
	DeviceTime time.Time `json:"device_time"`
}

type positionsRequest struct {
	Positions []fixRequest `json:"positions"`
}

// RecordPositions menyimpan sekumpulan posisi driver.
func (h *Handler) RecordPositions(c echo.Context) error {
	drv := pelakuDari(c)
	if drv == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req positionsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	fixes := make([]Fix, 0, len(req.Positions))
	for _, p := range req.Positions {
		fixes = append(fixes, Fix{
			Latitude: p.Latitude, Longitude: p.Longitude,
			AccuracyM: p.AccuracyM, DeviceTime: p.DeviceTime,
		})
	}

	hasil, err := h.deliveries.RecordPositions(c.Request().Context(), id, *drv, fixes)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, hasil)
}

type consentRequest struct {
	Granted bool `json:"granted"`
}

// SetConsent mencatat atau mencabut persetujuan pelacakan.
//
// Hanya driver sendiri yang dapat mengubahnya. Persetujuan yang dapat diberikan
// orang lain atas namanya bukan persetujuan.
func (h *Handler) SetConsent(c echo.Context) error {
	drv := pelakuDari(c)
	if drv == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	var req consentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	ctx := c.Request().Context()
	var err error
	if req.Granted {
		err = h.deliveries.GrantConsent(ctx, *drv)
	} else {
		err = h.deliveries.RevokeConsent(ctx, *drv)
	}
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, map[string]any{"tracking_consent": req.Granted})
}

// Sync memproses sekumpulan perintah yang dibuat perangkat tanpa jaringan.
func (h *Handler) Sync(c echo.Context) error {
	drv := pelakuDari(c)
	if drv == nil {
		return httpx.Fail(c, "AUTH_REQUIRED")
	}
	var req struct {
		Commands []Command `json:"commands"`
	}
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	hasil, err := h.deliveries.Sync(c.Request().Context(), *drv, req.Commands)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}

	// Konflik dilaporkan di dalam badan jawaban, bukan sebagai status HTTP
	// gagal, karena satu kumpulan dapat memuat perintah yang berhasil dan yang
	// berkonflik sekaligus. Satu status HTTP tidak dapat mewakili keduanya.
	return c.JSON(http.StatusOK, map[string]any{"results": hasil})
}

// pelakuDari membaca pengguna yang sedang masuk.
func pelakuDari(c echo.Context) *uuid.UUID {
	if p := httpx.PrincipalFrom(c); p != nil {
		id := p.UserID
		return &id
	}
	return nil
}

// codeFor menerjemahkan galat domain menjadi kode galat API.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, order.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrAlreadyAssigned):
		return "DELIVERY_ALREADY_ASSIGNED"
	case errors.Is(err, ErrDriverInactive):
		return "DRIVER_INACTIVE"
	case errors.Is(err, ErrDriverOtherDepot):
		return "DRIVER_OTHER_DEPOT"
	case errors.Is(err, ErrOrderNotReady):
		return "ORDER_NOT_READY"
	case errors.Is(err, ErrNotAssignedDriver):
		// Sengaja NOT_FOUND, bukan FORBIDDEN. Membedakan keduanya memberi tahu
		// driver bahwa pengiriman itu memang ada, hanya bukan tugasnya, dan
		// itu sudah informasi yang tidak perlu ia ketahui.
		return "NOT_FOUND"
	case errors.Is(err, ErrConsentRequired):
		return "TRACKING_CONSENT_REQUIRED"
	case errors.Is(err, ErrInvalidCoordinate):
		return "INVALID_COORDINATES"
	case errors.Is(err, ErrTrackingInactive):
		return "TRACKING_NOT_ACTIVE"
	case errors.Is(err, ErrProofRequired):
		return "PROOF_REQUIRED"
	case errors.Is(err, ErrReasonRequired):
		return "REASON_REQUIRED"
	case errors.Is(err, ErrCategoryRequired):
		return "VALIDATION_FAILED"
	case errors.Is(err, ErrInvalidTransition):
		return "INVALID_STATE_TRANSITION"
	case errors.Is(err, ErrSyncConflict):
		return "SYNC_CONFLICT"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrReasonRequired):
		return []httpx.Detail{{Field: "reason", Message: "Alasan wajib diisi."}}
	case errors.Is(err, ErrCategoryRequired):
		return []httpx.Detail{{Field: "category", Message: "Kategori kendala wajib dipilih."}}
	case errors.Is(err, ErrProofRequired):
		return []httpx.Detail{{Field: "photo_key", Message: "Foto bukti serah terima wajib diunggah."}}
	case errors.Is(err, ErrDriverOtherDepot):
		return []httpx.Detail{{Field: "driver_id", Message: "Driver ini bukan milik depo pesanan tersebut."}}
	case errors.Is(err, ErrDriverInactive):
		return []httpx.Detail{{Field: "driver_id", Message: "Driver tidak aktif atau bukan driver."}}
	default:
		return nil
	}
}
