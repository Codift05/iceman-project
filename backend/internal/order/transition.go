package order

import "fmt"

// Transition menjelaskan satu perpindahan status yang diizinkan beserta
// penjaga dan efek sampingnya (SRS Bab 5.1).
type Transition struct {
	From string
	To   string

	// RequiresReason mewajibkan alasan diisi. Dipakai pembatalan dan kegagalan
	// kirim, yaitu kejadian yang nanti perlu dijelaskan kepada pelanggan atau
	// ditinjau manajemen.
	RequiresReason bool

	// ReleasesSlot mengembalikan kuota slot agar pelanggan lain dapat
	// memakainya. Wajib untuk setiap perpindahan menuju CANCELLED: kuota yang
	// tidak dikembalikan berarti satu pengiriman hilang dari kapasitas harian
	// tanpa ada yang mengisinya.
	ReleasesSlot bool

	// MarksCompleted mencatat waktu selesai pada pesanan.
	MarksCompleted bool

	// Permission adalah izin yang dibutuhkan pelaku. Kosong berarti
	// perpindahan ini tidak dipicu manusia, melainkan oleh sistem, misalnya
	// webhook pembayaran atau kedaluwarsanya tagihan.
	Permission string
}

// matriks adalah satu satunya sumber kebenaran perpindahan status.
//
// Dibuat sebagai data, bukan rangkaian percabangan if, agar daftar lengkapnya
// dapat dibaca sekaligus dan dibandingkan langsung dengan tabel pada SRS Bab
// 5.1. Perpindahan yang tidak tercantum di sini ditolak, termasuk bila dipicu
// dari dashboard admin.
var matriks = []Transition{
	// Pembayaran berhasil. Dipicu webhook, bukan manusia, sehingga tanpa izin.
	{From: StatusWaitingPayment, To: StatusPaid},

	// Tagihan kedaluwarsa atau pesanan dibatalkan sebelum dibayar.
	{From: StatusWaitingPayment, To: StatusCancelled,
		RequiresReason: true, ReleasesSlot: true, Permission: "order.manage"},

	{From: StatusPaid, To: StatusProcessing, Permission: "order.manage"},
	{From: StatusPaid, To: StatusCancelled,
		RequiresReason: true, ReleasesSlot: true, Permission: "order.manage"},

	// Slot final dan driver ditetapkan. Pembuatan baris pengiriman menyusul
	// bersama domain pengiriman.
	{From: StatusProcessing, To: StatusScheduled, Permission: "dispatch.manage"},
	{From: StatusProcessing, To: StatusCancelled,
		RequiresReason: true, ReleasesSlot: true, Permission: "order.manage"},

	{From: StatusScheduled, To: StatusOutForDelivery, Permission: "delivery.update_own"},
	{From: StatusScheduled, To: StatusCancelled,
		RequiresReason: true, ReleasesSlot: true, Permission: "order.manage"},

	{From: StatusOutForDelivery, To: StatusCompleted,
		MarksCompleted: true, Permission: "delivery.update_own"},

	// Gagal kirim mengembalikan pesanan ke jadwal, bukan membatalkannya.
	// Kuota slot tidak dikembalikan karena pengirimannya masih akan diulang
	// pada slot yang sama.
	{From: StatusOutForDelivery, To: StatusScheduled,
		RequiresReason: true, Permission: "delivery.update_own"},
}

// penjadwalanUlang adalah perpindahan SCHEDULED ke SCHEDULED pada tabel SRS.
//
// Dipisahkan dari matriks karena bukan perubahan status: statusnya tetap, yang
// berpindah adalah slotnya. Menyatukannya membuat ChangeStatus harus tahu cara
// memindahkan kuota, padahal itu pekerjaan Reschedule.
var penjadwalanUlang = Transition{
	From: StatusScheduled, To: StatusScheduled, Permission: "order.manage",
}

// Allowed mencari perpindahan yang diizinkan dari satu status ke status lain.
func Allowed(from, to string) (Transition, bool) {
	for _, t := range matriks {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// AllowedFrom mengembalikan seluruh status tujuan yang sah dari satu status.
//
// Dipakai antarmuka admin untuk menampilkan hanya tindakan yang mungkin,
// sehingga tombol yang pasti ditolak tidak perlu ditampilkan lebih dahulu.
func AllowedFrom(from string) []string {
	var out []string
	for _, t := range matriks {
		if t.From == from {
			out = append(out, t.To)
		}
	}
	return out
}

// PermissionFor mengembalikan izin yang dibutuhkan sebuah perpindahan.
func PermissionFor(from, to string) (string, error) {
	t, ok := Allowed(from, to)
	if !ok {
		return "", fmt.Errorf("%w: %s ke %s", ErrInvalidTransition, from, to)
	}
	return t.Permission, nil
}

// ReschedulePermission adalah izin yang dibutuhkan untuk menjadwalkan ulang.
//
// Diambil dari entri penjadwalan ulang pada tabel SRS, bukan ditulis ulang di
// lapisan HTTP, agar hanya ada satu tempat yang menentukan izinnya.
func ReschedulePermission() string { return penjadwalanUlang.Permission }

// Terminal menjawab apakah sebuah status tidak punya kelanjutan.
func Terminal(status string) bool { return len(AllowedFrom(status)) == 0 }
