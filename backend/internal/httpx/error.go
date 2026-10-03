// Package httpx menyediakan bentuk galat yang seragam untuk seluruh endpoint,
// sesuai API Specification Bab 3.1. Klien menangani galat lewat satu jalur dan
// tidak perlu menebak bentuknya per endpoint.
package httpx

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Detail menjelaskan satu kolom yang bermasalah pada galat validasi.
type Detail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Envelope adalah bungkus galat yang dikirim ke klien.
type Envelope struct {
	Error Body `json:"error"`
}

// Body memuat kode mesin, pesan untuk pengguna, dan penelusuran.
type Body struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id,omitempty"`
	Details   []Detail `json:"details,omitempty"`
}

// katalog memetakan kode galat ke status HTTP dan pesan bahasa Indonesia,
// mengikuti SRS Bab 8.
var katalog = map[string]struct {
	Status  int
	Message string
}{
	"AUTH_REQUIRED":       {http.StatusUnauthorized, "Sesi berakhir. Silakan masuk kembali."},
	"CREDENTIALS_INVALID": {http.StatusUnauthorized, "Surel atau kata sandi salah."},
	"MFA_REQUIRED":        {http.StatusUnauthorized, "Masukkan kode faktor kedua untuk melanjutkan."},
	"MFA_CODE_INVALID":    {http.StatusUnauthorized, "Kode salah. Periksa aplikasi autentikator Anda."},
	"MFA_CODE_REPLAYED":   {http.StatusUnauthorized, "Kode itu sudah dipakai. Tunggu kode berikutnya."},
	"MFA_NOT_ENROLLED":    {http.StatusConflict, "Faktor kedua belum didaftarkan."},
	"MFA_ALREADY_ENABLED": {http.StatusConflict, "Faktor kedua sudah aktif pada akun ini."},
	"ACCOUNT_LOCKED":      {http.StatusLocked, "Akun terkunci sementara. Coba lagi nanti."},
	"ACCOUNT_INACTIVE":    {http.StatusForbidden, "Akun ini tidak aktif. Hubungi administrator."},
	"REFRESH_INVALID":     {http.StatusUnauthorized, "Sesi tidak berlaku. Silakan masuk kembali."},
	"REFRESH_REUSED":      {http.StatusUnauthorized, "Sesi dihentikan demi keamanan. Silakan masuk kembali."},
	"FORBIDDEN":           {http.StatusForbidden, "Anda tidak memiliki akses ke halaman ini."},
	"NOT_FOUND":           {http.StatusNotFound, "Data yang diminta tidak ditemukan."},
	"CONFLICT":            {http.StatusConflict, "Data serupa sudah ada atau sedang dipakai."},
	"VALIDATION_FAILED":   {http.StatusUnprocessableEntity, "Periksa kembali isian yang ditandai."},
	"AREA_NOT_SERVED":     {http.StatusUnprocessableEntity, "Alamat ini belum masuk wilayah layanan."},
	"MIN_ORDER_NOT_MET":   {http.StatusUnprocessableEntity, "Jumlah pesanan belum memenuhi minimum."},
	"PRODUCT_UNAVAILABLE": {http.StatusConflict, "Produk sedang tidak tersedia."},
	"SLOT_FULL":           {http.StatusConflict, "Slot penuh. Pilih waktu lain."},
	"SLOT_CUTOFF_PASSED":  {http.StatusConflict, "Batas pemesanan untuk slot ini sudah lewat."},
	"SLOT_UNAVAILABLE":    {http.StatusConflict, "Tidak ada pengiriman pada tanggal tersebut."},
	"INTERNAL":            {http.StatusInternalServerError, "Terjadi gangguan. Silakan coba lagi."},
}

// Fail membalas dengan kode galat yang terdaftar pada katalog.
func Fail(c echo.Context, code string, details ...Detail) error {
	entry, ok := katalog[code]
	if !ok {
		entry = katalog["INTERNAL"]
		code = "INTERNAL"
	}
	return c.JSON(entry.Status, Envelope{Body{
		Code:      code,
		Message:   entry.Message,
		RequestID: RequestIDFrom(c),
		Details:   details,
	}})
}
