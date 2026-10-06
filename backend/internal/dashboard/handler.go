package dashboard

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// FinancePermission adalah izin yang membuka indikator keuangan pada dashboard.
const FinancePermission = "report.view_financial"

// ViewPermissions adalah izin yang masing masing cukup untuk membuka dashboard.
//
// Daftarnya ada di sini, bukan di pemasangan rute, agar perubahannya tidak
// terpisah dari indikator yang dipaparkan paket ini, dan agar dapat diadu
// dengan matriks peran lewat uji.
//
// Dibuat "salah satu dari" karena matriks peran memecah kewenangan laporan
// menurut isinya: Admin Operasional memegang report.view_operational, Keuangan
// memegang report.view_financial, dan Manajemen memegang report.view_summary.
// Menuntut satu izin tertentu akan menutup dashboard bagi dua peran yang
// justru paling sering membukanya.
func ViewPermissions() []string {
	return []string{
		"report.view_operational",
		FinancePermission,
		"report.view_summary",
	}
}

// CheckPermission memeriksa izin seorang peran.
//
// Dinyatakan sebagai fungsi, bukan dengan mengimpor paket identitas, dengan
// alasan yang sama seperti middleware izin: paket ini tidak perlu tahu
// bagaimana izin disimpan.
type CheckPermission func(c echo.Context, role, permission string) (bool, error)

// Handler memaparkan dashboard sebagai endpoint HTTP.
type Handler struct {
	svc   *Service
	check CheckPermission
	// financePermission adalah izin yang menentukan apakah indikator keuangan
	// ikut dikirim.
	financePermission string
}

// NewHandler membuat handler dashboard.
func NewHandler(s *Service, check CheckPermission, financePermission string) *Handler {
	return &Handler{svc: s, check: check, financePermission: financePermission}
}

// Indicators mengembalikan indikator operasional.
//
// Indikator keuangan hanya ikut bila pemanggil memegang izinnya. Yang tidak
// berwenang menerima jawaban berhasil tanpa bagian keuangan, bukan 403.
//
// Dipilih begitu karena dashboard memuat banyak indikator sekaligus: menolak
// seluruh halaman karena satu bagian di luar kewenangan akan membuat peran
// seperti Admin Operasional tidak dapat melihat apa pun. Peran yang memang
// hanya butuh angka keuangan memakai laporan keuangan, yang memang berpagar
// izin.
func (h *Handler) Indicators(c echo.Context) error {
	f := Filter{From: c.QueryParam("from"), Until: c.QueryParam("until")}
	if raw := c.QueryParam("depot_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "depot_id", Message: "Pengenal depo tidak sah."})
		}
		f.DepotID = &id
	}

	boleh := false
	if h.check != nil {
		p := httpx.PrincipalFrom(c)
		if p == nil {
			return httpx.Fail(c, "AUTH_REQUIRED")
		}
		ok, err := h.check(c, p.Role, h.financePermission)
		if err != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		boleh = ok
	}

	out, err := h.svc.Compute(c.Request().Context(), f, boleh)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"indicators":       out,
		"includes_finance": boleh,
	})
}

// Definitions mengembalikan definisi setiap indikator.
func (h *Handler) Definitions(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"definitions": Definitions()})
}

func codeFor(err error) string {
	if errors.Is(err, ErrPeriodInvalid) {
		return "VALIDATION_FAILED"
	}
	return "INTERNAL"
}

func detailFor(err error) []httpx.Detail {
	if errors.Is(err, ErrPeriodInvalid) {
		return []httpx.Detail{{Field: "from", Message: err.Error()}}
	}
	return nil
}
