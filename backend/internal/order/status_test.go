package order_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/scheduling"
)

// pesananBaru membuat satu pesanan siap diuji transisinya.
func (l *lingkungan) pesananBaru(t *testing.T, w wilayah, slot uuid.UUID) *order.Order {
	t.Helper()
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	got, err := l.pesanan.Checkout(context.Background(), order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("menyiapkan pesanan: %v", err)
	}
	return got
}

// majukan memindahkan pesanan melalui rangkaian status.
func (l *lingkungan) majukan(t *testing.T, orderID uuid.UUID, urutan ...string) *order.Order {
	t.Helper()
	var got *order.Order
	for _, s := range urutan {
		var err error
		got, err = l.pesanan.ChangeStatus(context.Background(), orderID, s,
			order.ChangeInput{Reason: "uji"})
		if err != nil {
			t.Fatalf("memindahkan ke %s: %v", s, err)
		}
	}
	return got
}

// TestMatriks_SesuaiTabelSRS mengunci daftar perpindahan yang diizinkan.
//
// Daftarnya ditulis ulang di sini dari tabel SRS Bab 5.1, bukan dibaca dari
// kode yang diujinya. Kalau suatu saat ada yang menambah perpindahan baru
// tanpa memperbarui dokumen, uji ini yang menangkapnya.
func TestMatriks_SesuaiTabelSRS(t *testing.T) {
	mau := map[string][]string{
		order.StatusWaitingPayment: {order.StatusPaid, order.StatusCancelled},
		order.StatusPaid:           {order.StatusProcessing, order.StatusCancelled},
		order.StatusProcessing:     {order.StatusScheduled, order.StatusCancelled},
		order.StatusScheduled:      {order.StatusOutForDelivery, order.StatusCancelled},
		order.StatusOutForDelivery: {order.StatusCompleted, order.StatusScheduled},
		order.StatusCompleted:      nil,
		order.StatusCancelled:      nil,
	}

	for dari, tujuan := range mau {
		got := order.AllowedFrom(dari)
		if len(got) != len(tujuan) {
			t.Fatalf("dari %s ada %d tujuan (%v), seharusnya %d (%v)",
				dari, len(got), got, len(tujuan), tujuan)
		}
		for _, ke := range tujuan {
			if _, ok := order.Allowed(dari, ke); !ok {
				t.Fatalf("%s ke %s seharusnya diizinkan", dari, ke)
			}
		}
	}

	// COMPLETED dan CANCELLED tidak punya kelanjutan.
	for _, s := range []string{order.StatusCompleted, order.StatusCancelled} {
		if !order.Terminal(s) {
			t.Fatalf("%s seharusnya status akhir", s)
		}
	}
}

// TestTransisi_YangTidakTerdaftarDitolak menjaga contoh yang disebut langsung
// pada SRS-ORD-003: memindahkan pesanan dari COMPLETED ke PROCESSING ditolak.
func TestTransisi_YangTidakTerdaftarDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing,
		order.StatusScheduled, order.StatusOutForDelivery, order.StatusCompleted)

	_, err := l.pesanan.ChangeStatus(ctx, o.ID, order.StatusProcessing, order.ChangeInput{})
	if !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("COMPLETED ke PROCESSING seharusnya ditolak, dapat %v", err)
	}

	// Melompat beberapa langkah juga ditolak.
	o2 := l.pesananBaru(t, w, slot)
	_, err = l.pesanan.ChangeStatus(ctx, o2.ID, order.StatusCompleted, order.ChangeInput{})
	if !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("WAITING_PAYMENT ke COMPLETED seharusnya ditolak, dapat %v", err)
	}
}

// TestTransisi_PembatalanMengembalikanKuota menjaga janji SRS-ORD-003 yang
// paling berdampak: kuota kembali sehingga pelanggan lain dapat memakainya.
func TestTransisi_PembatalanMengembalikanKuota(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	// Kapasitas satu, supaya terlihat jelas slotnya kembali terbuka.
	slot := l.buatSlot(t, w.AreaID, 1)
	o := l.pesananBaru(t, w, slot)

	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}

	if _, err := l.pesanan.Cancel(ctx, o.ID, "pelanggan berubah pikiran"); err != nil {
		t.Fatalf("membatalkan pesanan: %v", err)
	}
	if n := l.kuotaTerpakai(t, slot); n != 0 {
		t.Fatalf("kuota terpakai %d sesudah pembatalan, seharusnya 0", n)
	}

	// Dan slot itu benar benar dapat dipakai pelanggan lain.
	lain := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok Lain", 1000000, 1)
	l.isiKeranjang(t, lain, balok, 1)
	if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: lain.ID, AddressID: lain.AddressID, SlotID: slot,
	}); err != nil {
		t.Fatalf("slot yang dibatalkan seharusnya dapat dipakai lagi: %v", err)
	}
}

// TestTransisi_PembatalanWajibBeralasan menjaga SRS-ORD-004.
func TestTransisi_PembatalanWajibBeralasan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	_, err := l.pesanan.ChangeStatus(ctx, o.ID, order.StatusCancelled, order.ChangeInput{})
	if !errors.Is(err, order.ErrReasonRequired) {
		t.Fatalf("pembatalan tanpa alasan seharusnya ditolak, dapat %v", err)
	}
	if _, err := l.pesanan.Cancel(ctx, o.ID, "   "); !errors.Is(err, order.ErrReasonRequired) {
		t.Fatalf("alasan berisi spasi saja seharusnya ditolak, dapat %v", err)
	}

	// Kuota belum kembali karena pembatalannya gagal.
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya masih 1", n)
	}
}

// TestTransisi_AlasanTersimpanPadaPesanan menjaga agar alasan pembatalan dapat
// dibaca kembali, bukan hanya diperiksa lalu dibuang.
func TestTransisi_AlasanTersimpanPadaPesanan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	const alasan = "stok es tidak cukup hari ini"
	got, err := l.pesanan.Cancel(ctx, o.ID, alasan)
	if err != nil {
		t.Fatalf("membatalkan pesanan: %v", err)
	}
	if got.CancelledReason != alasan {
		t.Fatalf("alasan tersimpan %q, seharusnya %q", got.CancelledReason, alasan)
	}

	riwayat, err := l.pesanan.History(ctx, o.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	akhir := riwayat[len(riwayat)-1]
	if akhir.ToStatus != order.StatusCancelled || akhir.Reason != alasan {
		t.Fatalf("riwayat pembatalan salah: %+v", akhir)
	}
	if akhir.FromStatus == nil || *akhir.FromStatus != order.StatusWaitingPayment {
		t.Fatalf("status asal pada riwayat salah: %+v", akhir.FromStatus)
	}
}

// TestTransisi_RiwayatMencatatSeluruhLangkah menjaga SRS-ORD-003: setiap
// transisi mencatat status asal, status tujuan, dan waktunya.
func TestTransisi_RiwayatMencatatSeluruhLangkah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

	riwayat, err := l.pesanan.History(ctx, o.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	// Satu baris saat pembuatan, tiga baris perpindahan.
	if len(riwayat) != 4 {
		t.Fatalf("baris riwayat %d, seharusnya 4", len(riwayat))
	}
	urutan := []string{
		order.StatusWaitingPayment, order.StatusPaid,
		order.StatusProcessing, order.StatusScheduled,
	}
	for i, mau := range urutan {
		if riwayat[i].ToStatus != mau {
			t.Fatalf("riwayat ke-%d adalah %s, seharusnya %s", i, riwayat[i].ToStatus, mau)
		}
	}
	// Setiap perpindahan menyebut status asalnya, kecuali yang pertama.
	for i := 1; i < len(riwayat); i++ {
		if riwayat[i].FromStatus == nil || *riwayat[i].FromStatus != urutan[i-1] {
			t.Fatalf("riwayat ke-%d tidak menyebut status asal yang benar", i)
		}
	}
}

// TestTransisi_GagalKirimKembaliTerjadwalTanpaMelepasKuota menjaga baris
// terakhir tabel SRS: pengiriman yang gagal diulang pada slot yang sama,
// sehingga kuotanya tidak dilepas.
func TestTransisi_GagalKirimKembaliTerjadwalTanpaMelepasKuota(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing,
		order.StatusScheduled, order.StatusOutForDelivery)
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}

	got, err := l.pesanan.ChangeStatus(ctx, o.ID, order.StatusScheduled,
		order.ChangeInput{Reason: "alamat tidak ditemukan"})
	if err != nil {
		t.Fatalf("mencatat gagal kirim: %v", err)
	}
	if got.Status != order.StatusScheduled {
		t.Fatalf("status %q, seharusnya SCHEDULED", got.Status)
	}
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya tetap 1 karena akan diulang", n)
	}
}

// TestTransisi_GagalKirimWajibBeralasan menjaga agar kegagalan kirim dapat
// ditinjau manajemen.
func TestTransisi_GagalKirimWajibBeralasan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing,
		order.StatusScheduled, order.StatusOutForDelivery)

	_, err := l.pesanan.ChangeStatus(ctx, o.ID, order.StatusScheduled, order.ChangeInput{})
	if !errors.Is(err, order.ErrReasonRequired) {
		t.Fatalf("gagal kirim tanpa alasan seharusnya ditolak, dapat %v", err)
	}
}

// TestTransisi_PenyelesaianMencatatWaktu menjaga efek samping COMPLETED.
func TestTransisi_PenyelesaianMencatatWaktu(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	o := l.pesananBaru(t, w, slot)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing,
		order.StatusScheduled, order.StatusOutForDelivery, order.StatusCompleted)

	var ada bool
	err := l.pool.QueryRow(ctx,
		`SELECT completed_at IS NOT NULL FROM orders WHERE id = $1`, o.ID).Scan(&ada)
	if err != nil {
		t.Fatalf("membaca waktu selesai: %v", err)
	}
	if !ada {
		t.Fatal("waktu selesai seharusnya tercatat")
	}
}

// TestTransisi_IzinDiambilDariMatriks menjaga agar lapisan HTTP tidak perlu
// menuliskan ulang izin tiap transisi.
func TestTransisi_IzinDiambilDariMatriks(t *testing.T) {
	kasus := []struct{ dari, ke, izin string }{
		{order.StatusPaid, order.StatusProcessing, "order.manage"},
		{order.StatusProcessing, order.StatusScheduled, "dispatch.manage"},
		{order.StatusScheduled, order.StatusOutForDelivery, "delivery.update_own"},
		{order.StatusOutForDelivery, order.StatusCompleted, "delivery.update_own"},
		{order.StatusScheduled, order.StatusCancelled, "order.manage"},
		// Perpindahan yang dipicu webhook pembayaran tidak lewat manusia.
		{order.StatusWaitingPayment, order.StatusPaid, ""},
	}
	for _, k := range kasus {
		got, err := order.PermissionFor(k.dari, k.ke)
		if err != nil {
			t.Fatalf("%s ke %s: %v", k.dari, k.ke, err)
		}
		if got != k.izin {
			t.Fatalf("%s ke %s butuh izin %q, seharusnya %q", k.dari, k.ke, got, k.izin)
		}
	}
	if _, err := order.PermissionFor(order.StatusCompleted, order.StatusPaid); !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatal("transisi tidak sah seharusnya menghasilkan ErrInvalidTransition")
	}
}

func TestTransisi_PesananTidakAda(t *testing.T) {
	l := siapkan(t)
	_, err := l.pesanan.ChangeStatus(context.Background(), uuid.New(),
		order.StatusPaid, order.ChangeInput{})
	if !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("pesanan tidak ada seharusnya ErrNotFound, dapat %v", err)
	}
}

// --- penjadwalan ulang ---

// TestJadwalUlang_KuotaBerpindahUtuh menjaga SRS-ORD-004: kuota slot lama
// bertambah kembali tepat sebanyak yang dipindahkan.
func TestJadwalUlang_KuotaBerpindahUtuh(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slotLama := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "08:00")
	slotBaru := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "11:00")
	o := l.pesananBaru(t, w, slotLama)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

	got, err := l.pesanan.Reschedule(ctx, o.ID, slotBaru, "pelanggan minta digeser")
	if err != nil {
		t.Fatalf("menjadwalkan ulang: %v", err)
	}
	if got.SlotID != slotBaru {
		t.Fatalf("slot pesanan %s, seharusnya %s", got.SlotID, slotBaru)
	}
	if got.Status != order.StatusScheduled {
		t.Fatalf("status %q, penjadwalan ulang tidak boleh mengubah status", got.Status)
	}
	if n := l.kuotaTerpakai(t, slotLama); n != 0 {
		t.Fatalf("kuota slot lama %d, seharusnya 0", n)
	}
	if n := l.kuotaTerpakai(t, slotBaru); n != 1 {
		t.Fatalf("kuota slot baru %d, seharusnya 1", n)
	}
}

// TestJadwalUlang_SlotTujuanPenuhTidakMelepasSlotLama menjaga urutan di dalam
// Move: kuota tujuan diambil lebih dahulu. Bila tujuan penuh, pesanan tidak
// boleh kehilangan jadwalnya.
func TestJadwalUlang_SlotTujuanPenuhTidakMelepasSlotLama(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slotLama := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "08:00")
	slotPenuh := l.buatSlotKhusus(t, w.AreaID, 1, 1, timeDepan(), false, "11:00")
	o := l.pesananBaru(t, w, slotLama)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

	_, err := l.pesanan.Reschedule(ctx, o.ID, slotPenuh, "coba geser")
	if !errors.Is(err, scheduling.ErrSlotFull) {
		t.Fatalf("galat %v, seharusnya ErrSlotFull", err)
	}
	if n := l.kuotaTerpakai(t, slotLama); n != 1 {
		t.Fatalf("kuota slot lama %d, seharusnya tetap 1", n)
	}
	lagi, err := l.pesanan.Get(ctx, o.ID)
	if err != nil {
		t.Fatalf("membaca pesanan: %v", err)
	}
	if lagi.SlotID != slotLama {
		t.Fatal("pesanan kehilangan jadwalnya padahal penjadwalan ulang gagal")
	}
}

// TestJadwalUlang_SlotTujuanWajibMemenuhiSyaratSama menjaga SRS-ORD-004: slot
// baru harus memenuhi seluruh syarat yang sama seperti pemesanan awal.
func TestJadwalUlang_SlotTujuanWajibMemenuhiSyaratSama(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)

	// Setiap kasus memakai jendela jam yang berbeda, karena DB-09 melarang dua
	// slot pada wilayah, tanggal, dan jam mulai yang sama.
	kasus := []struct {
		nama    string
		jamLama string
		slot    func() uuid.UUID
		mau     error
	}{
		{"hari libur", "08:00", func() uuid.UUID {
			return l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), true, "14:00")
		}, scheduling.ErrSlotUnavailable},
		{"batas pesan terlewat", "09:00", func() uuid.UUID {
			return l.buatSlotKhusus(t, w.AreaID, 5, 0, timeLewat(), false, "17:00")
		}, scheduling.ErrSlotCutoffPassed},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			slotLama := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, k.jamLama)
			o := l.pesananBaru(t, w, slotLama)
			l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

			if _, err := l.pesanan.Reschedule(ctx, o.ID, k.slot(), "coba"); !errors.Is(err, k.mau) {
				t.Fatalf("galat %v, seharusnya %v", err, k.mau)
			}
			if n := l.kuotaTerpakai(t, slotLama); n != 1 {
				t.Fatalf("kuota slot lama %d, seharusnya tetap 1", n)
			}
		})
	}
}

// TestJadwalUlang_WilayahLainDitolak menjaga agar penjadwalan ulang tidak
// memindahkan pesanan ke depo lain. Itu bukan penjadwalan ulang melainkan
// pesanan yang berbeda.
func TestJadwalUlang_WilayahLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	wA := l.buatWilayah(t)
	wB := l.buatWilayah(t)
	slotLama := l.buatSlot(t, wA.AreaID, 5)
	slotLain := l.buatSlot(t, wB.AreaID, 5)
	o := l.pesananBaru(t, wA, slotLama)

	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

	if _, err := l.pesanan.Reschedule(ctx, o.ID, slotLain, "coba"); !errors.Is(err, order.ErrSlotAreaMismatch) {
		t.Fatalf("galat %v, seharusnya ErrSlotAreaMismatch", err)
	}
}

// TestJadwalUlang_StatusYangTidakBolehDijadwalkanUlang menjaga agar pesanan
// yang sudah selesai atau batal tidak dapat diubah jadwalnya.
func TestJadwalUlang_StatusYangTidakBolehDijadwalkanUlang(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slotLama := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "08:00")
	slotBaru := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "11:00")

	// Masih menunggu pembayaran, belum punya jadwal untuk dipindahkan.
	o := l.pesananBaru(t, w, slotLama)
	if _, err := l.pesanan.Reschedule(ctx, o.ID, slotBaru, "coba"); !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("pesanan menunggu pembayaran seharusnya ditolak, dapat %v", err)
	}

	// Sudah dibatalkan.
	o2 := l.pesananBaru(t, w, slotLama)
	if _, err := l.pesanan.Cancel(ctx, o2.ID, "batal"); err != nil {
		t.Fatalf("membatalkan: %v", err)
	}
	if _, err := l.pesanan.Reschedule(ctx, o2.ID, slotBaru, "coba"); !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("pesanan batal seharusnya ditolak, dapat %v", err)
	}
}

func TestJadwalUlang_SlotSamaDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 5)
	o := l.pesananBaru(t, w, slot)
	l.majukan(t, o.ID, order.StatusPaid, order.StatusProcessing, order.StatusScheduled)

	if _, err := l.pesanan.Reschedule(ctx, o.ID, slot, "coba"); !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("slot yang sama seharusnya ditolak, dapat %v", err)
	}
}
