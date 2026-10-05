package delivery

import "fmt"

// Transition menjelaskan satu perpindahan status pengiriman yang diizinkan
// beserta penjaga dan efek sampingnya (SRS Bab 5.3).
type Transition struct {
	From string
	To   string

	// ByAssignedDriver menandai perpindahan yang hanya boleh dilakukan driver
	// yang ditugaskan, bukan admin. Driver lain, walau sesama driver, tidak
	// boleh menerima atau menyelesaikan tugas orang lain.
	ByAssignedDriver bool

	// RequiresReason mewajibkan alasan diisi, untuk kejadian yang nanti perlu
	// dijelaskan kepada pelanggan atau ditinjau manajemen.
	RequiresReason bool

	// RequiresProof mewajibkan bukti serah terima sudah terunggah. Pengiriman
	// tidak dapat berstatus selesai tanpa bukti (SRS-DLV-004).
	RequiresProof bool

	// Permission adalah izin yang dibutuhkan. Perpindahan oleh driver memakai
	// delivery.update_own, yang dipasangkan dengan pemeriksaan kepemilikan
	// tugas; izin saja tidak cukup.
	Permission string
}

// matriks adalah satu satunya sumber kebenaran perpindahan status pengiriman.
//
// Sama seperti pada pesanan, dibuat sebagai data agar daftar lengkapnya dapat
// dibandingkan langsung dengan tabel SRS Bab 5.3.
var matriks = []Transition{
	{From: StatusAssigned, To: StatusAccepted,
		ByAssignedDriver: true, Permission: "delivery.update_own"},

	{From: StatusAccepted, To: StatusOnTheWay,
		ByAssignedDriver: true, Permission: "delivery.update_own"},

	{From: StatusOnTheWay, To: StatusArrived,
		ByAssignedDriver: true, Permission: "delivery.update_own"},

	{From: StatusOnTheWay, To: StatusFailed,
		ByAssignedDriver: true, RequiresReason: true, Permission: "delivery.update_own"},

	{From: StatusArrived, To: StatusDelivered,
		ByAssignedDriver: true, RequiresProof: true, Permission: "delivery.update_own"},

	{From: StatusArrived, To: StatusFailed,
		ByAssignedDriver: true, RequiresReason: true, Permission: "delivery.update_own"},

	// Dua yang terakhir dilakukan admin, bukan driver.
	{From: StatusFailed, To: StatusRescheduled, Permission: "dispatch.manage"},
	{From: StatusRescheduled, To: StatusAssigned, Permission: "dispatch.manage"},
}

// Allowed mencari perpindahan yang diizinkan.
func Allowed(from, to string) (Transition, bool) {
	for _, t := range matriks {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// AllowedFrom mengembalikan seluruh status tujuan yang sah dari satu status.
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

// Terminal menjawab apakah sebuah status tidak punya kelanjutan.
func Terminal(status string) bool { return len(AllowedFrom(status)) == 0 }

// TrackingActive menjawab apakah posisi driver boleh direkam pada status ini.
//
// Perekaman hanya aktif ketika driver sedang menuju lokasi atau sudah tiba
// (SRS-TRK-001). Merekam di luar itu berarti memantau driver saat ia belum
// berangkat atau sudah selesai, dan itu melampaui keperluan operasional.
func TrackingActive(status string) bool {
	return status == StatusOnTheWay || status == StatusArrived
}
