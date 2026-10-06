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
	"AUTH_REQUIRED":        {http.StatusUnauthorized, "Sesi berakhir. Silakan masuk kembali."},
	"CREDENTIALS_INVALID":  {http.StatusUnauthorized, "Surel atau kata sandi salah."},
	"MFA_REQUIRED":         {http.StatusUnauthorized, "Masukkan kode faktor kedua untuk melanjutkan."},
	"MFA_CODE_INVALID":     {http.StatusUnauthorized, "Kode salah. Periksa aplikasi autentikator Anda."},
	"MFA_CODE_REPLAYED":    {http.StatusUnauthorized, "Kode itu sudah dipakai. Tunggu kode berikutnya."},
	"MFA_NOT_ENROLLED":     {http.StatusConflict, "Faktor kedua belum didaftarkan."},
	"MFA_ALREADY_ENABLED":  {http.StatusConflict, "Faktor kedua sudah aktif pada akun ini."},
	"ACCOUNT_LOCKED":       {http.StatusLocked, "Akun terkunci sementara. Coba lagi nanti."},
	"ACCOUNT_INACTIVE":     {http.StatusForbidden, "Akun ini tidak aktif. Hubungi administrator."},
	"REFRESH_INVALID":      {http.StatusUnauthorized, "Sesi tidak berlaku. Silakan masuk kembali."},
	"REFRESH_REUSED":       {http.StatusUnauthorized, "Sesi dihentikan demi keamanan. Silakan masuk kembali."},
	"FORBIDDEN":            {http.StatusForbidden, "Anda tidak memiliki akses ke halaman ini."},
	"NOT_FOUND":            {http.StatusNotFound, "Data yang diminta tidak ditemukan."},
	"CONFLICT":             {http.StatusConflict, "Data serupa sudah ada atau sedang dipakai."},
	"VALIDATION_FAILED":    {http.StatusUnprocessableEntity, "Periksa kembali isian yang ditandai."},
	"AREA_NOT_SERVED":      {http.StatusUnprocessableEntity, "Alamat ini belum masuk wilayah layanan."},
	"MIN_ORDER_NOT_MET":    {http.StatusUnprocessableEntity, "Jumlah pesanan belum memenuhi minimum."},
	"PRODUCT_UNAVAILABLE":  {http.StatusConflict, "Produk sedang tidak tersedia."},
	"PRODUCT_NOT_FOUND":    {http.StatusNotFound, "Produk tidak ditemukan."},
	"SKU_ALREADY_EXISTS":   {http.StatusConflict, "Kode produk ini sudah dipakai."},
	"PHONE_ALREADY_EXISTS": {http.StatusConflict, "Nomor telepon ini sudah terdaftar."},
	"CREDIT_LIMIT_EXCEEDED": {http.StatusUnprocessableEntity,
		"Pesanan ini melampaui batas kredit pelanggan. Lunasi tagihan terdahulu lebih dahulu."},
	"CART_EMPTY":      {http.StatusUnprocessableEntity, "Keranjang masih kosong."},
	"REASON_REQUIRED": {http.StatusUnprocessableEntity, "Alasan wajib diisi."},
	"INVALID_STATE_TRANSITION": {http.StatusConflict,
		"Perubahan status ini tidak diizinkan dari status pesanan sekarang."},
	"REORDER_ITEMS_UNAVAILABLE": {http.StatusConflict,
		"Tidak ada produk pada pesanan itu yang masih dapat dipesan."},
	"SLOT_AREA_MISMATCH": {http.StatusUnprocessableEntity,
		"Jadwal yang dipilih tidak melayani alamat ini."},
	"DELIVERY_ALREADY_ASSIGNED": {http.StatusConflict,
		"Pesanan ini sudah memiliki penugasan pengiriman."},
	"DRIVER_INACTIVE": {http.StatusUnprocessableEntity, "Driver tidak aktif."},
	"DRIVER_OTHER_DEPOT": {http.StatusUnprocessableEntity,
		"Driver ini bukan milik depo pesanan tersebut."},
	"ORDER_NOT_READY": {http.StatusConflict,
		"Pesanan belum siap ditugaskan kepada driver."},
	"PROOF_REQUIRED": {http.StatusUnprocessableEntity,
		"Bukti serah terima wajib diunggah sebelum pengiriman diselesaikan."},
	"TRACKING_CONSENT_REQUIRED": {http.StatusForbidden,
		"Persetujuan pelacakan belum diberikan."},
	"INVALID_COORDINATES": {http.StatusUnprocessableEntity,
		"Koordinat di luar rentang yang sah."},
	"TRACKING_NOT_ACTIVE": {http.StatusConflict,
		"Pelacakan baru aktif setelah Anda berangkat menuju lokasi."},
	"SYNC_CONFLICT": {http.StatusConflict,
		"Status di server sudah lebih baru. Perbarui data pada perangkat."},
	"SIGNATURE_INVALID": {http.StatusUnauthorized,
		"Tanda tangan permintaan tidak sah."},
	"PAYMENT_ALREADY_PAID": {http.StatusConflict, "Pesanan ini sudah dibayar."},
	"ORDER_NOT_PAYABLE": {http.StatusConflict,
		"Pesanan ini tidak dapat dibayar pada statusnya sekarang."},
	"PAYMENT_PROVIDER_ERROR": {http.StatusBadGateway,
		"Penyedia pembayaran sedang tidak dapat dihubungi. Silakan coba lagi."},
	"PAYMENT_NOT_REFUNDABLE": {http.StatusConflict,
		"Pembayaran ini tidak dapat dikembalikan."},
	"REFUND_EXCEEDS_PAYMENT": {http.StatusUnprocessableEntity,
		"Nominal refund melebihi sisa yang dapat dikembalikan."},
	"SLOT_FULL":          {http.StatusConflict, "Slot penuh. Pilih waktu lain."},
	"SLOT_CUTOFF_PASSED": {http.StatusConflict, "Batas pemesanan untuk slot ini sudah lewat."},
	"SLOT_UNAVAILABLE":   {http.StatusConflict, "Tidak ada pengiriman pada tanggal tersebut."},
	"INTERNAL":           {http.StatusInternalServerError, "Terjadi gangguan. Silakan coba lagi."},
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
