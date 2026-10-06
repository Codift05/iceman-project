package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
)

// pemegang membuat pemeriksa izin yang hanya mengakui izin yang didaftarkan,
// serta mencatat izin apa saja yang ditanyakan kepadanya.
func pemegang(punya ...string) (httpx.CheckPermission, *[]string) {
	ditanya := new([]string)
	return func(_ echo.Context, _, permission string) (bool, error) {
		*ditanya = append(*ditanya, permission)
		for _, p := range punya {
			if p == permission {
				return true, nil
			}
		}
		return false, nil
	}, ditanya
}

// jalankan memasang middleware izin pada satu rute dan mengembalikan status,
// badan jawaban, serta apakah handlernya sampai dijalankan.
//
// Identitas dipasang lewat RequireAuth dengan parser palsu, yaitu jalur yang
// sama seperti di produksi, supaya uji ini tidak menuntut jalan masuk khusus
// ke dalam konteks. Dengan adaPrincipal bernilai salah, RequireAuth sengaja
// tidak dipasang: itu menguji penjagaan terakhir pada middleware izin, kalau
// suatu saat ada rute yang lupa dipasangi pemeriksaan token.
func jalankan(t *testing.T, mw echo.MiddlewareFunc, adaPrincipal bool) (int, string, bool) {
	t.Helper()
	e := echo.New()

	sampai := false
	handler := func(c echo.Context) error {
		sampai = true
		return c.NoContent(http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if adaPrincipal {
		parse := func(raw string) (*httpx.Principal, error) {
			if raw != "token-uji" {
				return nil, errors.New("token tidak dikenal")
			}
			return &httpx.Principal{UserID: uuid.New(), Role: "PERAN_UJI"}, nil
		}
		e.GET("/x", handler, httpx.RequireAuth(parse), mw)
		req.Header.Set("Authorization", "Bearer token-uji")
	} else {
		e.GET("/x", handler, mw)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), sampai
}

func TestRequirePermission_PemegangIzinDiteruskan(t *testing.T) {
	check, _ := pemegang("order.view")
	kode, _, sampai := jalankan(t, httpx.RequirePermission(check, "order.view", nil), true)
	if kode != http.StatusOK || !sampai {
		t.Fatalf("pemegang izin seharusnya diteruskan, dapat HTTP %d sampai=%v", kode, sampai)
	}
}

func TestRequirePermission_BukanPemegangDitolak(t *testing.T) {
	check, _ := pemegang("order.view")
	var dicatat string
	deny := func(_ echo.Context, butuh string) { dicatat = butuh }

	kode, body, sampai := jalankan(t, httpx.RequirePermission(check, "order.manage", deny), true)
	if kode != http.StatusForbidden {
		t.Fatalf("seharusnya 403, dapat HTTP %d", kode)
	}
	if sampai {
		t.Fatal("handler tidak boleh dijalankan saat izin ditolak")
	}
	if !strings.Contains(body, "FORBIDDEN") {
		t.Fatalf("badan jawaban seharusnya memuat kode FORBIDDEN: %s", body)
	}
	if dicatat != "order.manage" {
		t.Fatalf("jejak audit seharusnya menyebut izin yang dituntut, dapat %q", dicatat)
	}
}

// Tanpa identitas, jawabannya 401 dan bukan 403: pemanggilnya belum dikenali,
// jadi belum ada peran yang dapat dinilai berwenang atau tidak.
func TestRequirePermission_TanpaIdentitasMintaMasuk(t *testing.T) {
	check, ditanya := pemegang("order.view")
	kode, body, _ := jalankan(t, httpx.RequirePermission(check, "order.view", nil), false)
	if kode != http.StatusUnauthorized {
		t.Fatalf("seharusnya 401, dapat HTTP %d", kode)
	}
	if !strings.Contains(body, "AUTH_REQUIRED") {
		t.Fatalf("badan jawaban seharusnya AUTH_REQUIRED: %s", body)
	}
	if len(*ditanya) != 0 {
		t.Fatalf("izin tidak perlu diperiksa saat identitas belum ada, ditanya %v", *ditanya)
	}
}

// Gagal memeriksa izin tidak boleh ditafsirkan sebagai berwenang.
func TestRequirePermission_GalatPemeriksaanMenutupPintu(t *testing.T) {
	check := func(_ echo.Context, _, _ string) (bool, error) {
		return false, errors.New("basis data sedang tidak dapat dihubungi")
	}
	kode, _, sampai := jalankan(t, httpx.RequirePermission(check, "order.view", nil), true)
	if kode != http.StatusInternalServerError {
		t.Fatalf("seharusnya 500, dapat HTTP %d", kode)
	}
	if sampai {
		t.Fatal("handler tidak boleh dijalankan saat pemeriksaan izin gagal")
	}
}

func TestRequireAnyPermission_CukupSatuIzin(t *testing.T) {
	daftar := []string{"report.view_operational", "report.view_financial", "report.view_summary"}

	for _, punya := range daftar {
		t.Run(punya, func(t *testing.T) {
			check, _ := pemegang(punya)
			kode, _, sampai := jalankan(t, httpx.RequireAnyPermission(check, daftar, nil), true)
			if kode != http.StatusOK || !sampai {
				t.Fatalf("pemegang %s seharusnya diteruskan, dapat HTTP %d sampai=%v",
					punya, kode, sampai)
			}
		})
	}
}

func TestRequireAnyPermission_TanpaSatuPunDitolak(t *testing.T) {
	daftar := []string{"report.view_operational", "report.view_financial", "report.view_summary"}
	check, ditanya := pemegang("tracking.view")
	var dicatat string
	deny := func(_ echo.Context, butuh string) { dicatat = butuh }

	kode, _, sampai := jalankan(t, httpx.RequireAnyPermission(check, daftar, deny), true)
	if kode != http.StatusForbidden || sampai {
		t.Fatalf("seharusnya 403 tanpa menjalankan handler, dapat HTTP %d sampai=%v", kode, sampai)
	}
	if len(*ditanya) != len(daftar) {
		t.Fatalf("seluruh izin seharusnya dicoba sebelum menolak, ditanya %v", *ditanya)
	}
	// Jejak audit harus menyebut semua izin yang dapat membuka pintu, bukan
	// hanya yang pertama, agar penolakannya dapat ditelusuri dengan benar.
	for _, p := range daftar {
		if !strings.Contains(dicatat, p) {
			t.Fatalf("jejak audit %q seharusnya menyebut %s", dicatat, p)
		}
	}
}

// Begitu satu izin cocok, sisanya tidak perlu ditanyakan lagi.
func TestRequireAnyPermission_BerhentiPadaIzinPertamaYangCocok(t *testing.T) {
	check, ditanya := pemegang("report.view_operational")
	kode, _, _ := jalankan(t, httpx.RequireAnyPermission(check,
		[]string{"report.view_operational", "report.view_financial"}, nil), true)
	if kode != http.StatusOK {
		t.Fatalf("seharusnya 200, dapat HTTP %d", kode)
	}
	if len(*ditanya) != 1 {
		t.Fatalf("hanya izin pertama yang perlu ditanyakan, ditanya %v", *ditanya)
	}
}

// Rute yang terpasang tanpa izin menutup pintu, bukan membukanya.
func TestRequireAnyPermission_DaftarKosongMenolak(t *testing.T) {
	check, _ := pemegang("report.view_summary")
	kode, _, sampai := jalankan(t, httpx.RequireAnyPermission(check, nil, nil), true)
	if kode != http.StatusForbidden || sampai {
		t.Fatalf("daftar izin kosong seharusnya menolak, dapat HTTP %d sampai=%v", kode, sampai)
	}
}
