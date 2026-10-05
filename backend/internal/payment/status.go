package payment

import (
	"fmt"
	"strings"
)

// Transition menjelaskan satu perpindahan status pembayaran yang diizinkan
// (SRS Bab 5.2).
type Transition struct {
	From string
	To   string
	// RequiresAmountMatch mewajibkan nominal pada webhook sama dengan nominal
	// tagihan. Hanya berlaku pada perpindahan menuju berhasil: nominal yang
	// berbeda berarti yang dibayar bukan tagihan ini.
	RequiresAmountMatch bool
}

// matriks adalah satu satunya sumber kebenaran perpindahan status pembayaran.
var matriks = []Transition{
	{From: StatusPending, To: StatusSuccess, RequiresAmountMatch: true},
	{From: StatusPending, To: StatusExpired},
	{From: StatusPending, To: StatusFailed},
	{From: StatusPending, To: StatusCancelled},
	{From: StatusSuccess, To: StatusRefunded},
	// Dua yang terakhir adalah jalan kembali: pelanggan membuat tagihan baru.
	// Yang berpindah bukan baris pembayaran lama, melainkan baris baru yang
	// dibuat untuk pesanan yang sama, sehingga perpindahan ini hanya menyatakan
	// bahwa pesanannya masih dapat dibayar.
	{From: StatusExpired, To: StatusPending},
	{From: StatusFailed, To: StatusPending},
}

// Allowed mencari perpindahan status pembayaran yang diizinkan.
func Allowed(from, to string) (Transition, bool) {
	for _, t := range matriks {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// AllowedFrom mengembalikan status tujuan yang sah dari satu status.
func AllowedFrom(from string) []string {
	var out []string
	for _, t := range matriks {
		if t.From == from {
			out = append(out, t.To)
		}
	}
	return out
}

// pemetaan menerjemahkan status penyedia ke status internal.
//
// Nama status di sini mengikuti istilah yang umum dipakai penyedia pembayaran
// Indonesia. Ketika penyedia sudah dipilih, daftarnya diperiksa ulang terhadap
// dokumentasinya; yang tidak cocok akan muncul sebagai status tidak dikenal
// pada daftar tinjauan, bukan diproses salah.
//
// Kuncinya ditulis huruf kecil, dan masukan diubah ke huruf kecil sebelum
// dicari, karena penyedia tidak konsisten soal huruf besar kecil.
var pemetaan = map[string]string{
	// Berhasil.
	"settlement": StatusSuccess,
	"capture":    StatusSuccess,
	"paid":       StatusSuccess,
	"success":    StatusSuccess,
	"succeeded":  StatusSuccess,
	"completed":  StatusSuccess,

	// Masih menunggu.
	"pending":  StatusPending,
	"active":   StatusPending,
	"created":  StatusPending,
	"inactive": StatusPending,

	// Kedaluwarsa.
	"expire":  StatusExpired,
	"expired": StatusExpired,

	// Gagal.
	"deny":    StatusFailed,
	"denied":  StatusFailed,
	"failed":  StatusFailed,
	"failure": StatusFailed,

	// Dibatalkan.
	"cancel":    StatusCancelled,
	"cancelled": StatusCancelled,
	"canceled":  StatusCancelled,
	"void":      StatusCancelled,

	// Dikembalikan.
	"refund":         StatusRefunded,
	"refunded":       StatusRefunded,
	"partial_refund": StatusRefunded,
}

// MapStatus menerjemahkan status penyedia ke status internal.
//
// Status yang tidak dikenal mengembalikan ok bernilai false, bukan menebak.
// Menebaknya berarti pesanan dapat berpindah status karena status yang
// sebenarnya belum dipahami, dan itu menyentuh uang. Pemanggil menandainya
// untuk ditinjau manusia (SRS-PAY-003).
func MapStatus(providerStatus string) (string, bool) {
	s, ok := pemetaan[strings.ToLower(strings.TrimSpace(providerStatus))]
	return s, ok
}

// KnownProviderStatuses mengembalikan seluruh status penyedia yang dikenal.
//
// Dipakai uji untuk memastikan setiap status yang terdaftar memetakan ke status
// internal yang sah, sehingga salah tulis pada daftar pemetaan tertangkap.
func KnownProviderStatuses() []string {
	out := make([]string, 0, len(pemetaan))
	for k := range pemetaan {
		out = append(out, k)
	}
	return out
}

// ValidStatus menjawab apakah sebuah nilai adalah status pembayaran internal.
func ValidStatus(s string) bool {
	switch s {
	case StatusPending, StatusSuccess, StatusExpired,
		StatusFailed, StatusRefunded, StatusCancelled:
		return true
	}
	return false
}

// CheckTransition memeriksa perpindahan status beserta kecocokan nominalnya.
func CheckTransition(from, to string, nominalTagihan int64, nominalEvent *int64) error {
	t, ok := Allowed(from, to)
	if !ok {
		return fmt.Errorf("%w: %s ke %s", ErrInvalidStatus, from, to)
	}
	if t.RequiresAmountMatch {
		// Nominal yang tidak disebutkan penyedia tidak dianggap cocok. Lebih
		// baik ditinjau manusia daripada diterima begitu saja, karena yang
		// dipertaruhkan adalah pesanan dinyatakan lunas.
		if nominalEvent == nil || *nominalEvent != nominalTagihan {
			return ErrAmountMismatch
		}
	}
	return nil
}
