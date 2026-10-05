package delivery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/iceman/backend/internal/delivery"
	"github.com/iceman/backend/internal/order"
)

// TestMatriksKirim_SesuaiTabelSRS mengunci daftar perpindahan yang diizinkan.
//
// Daftarnya ditulis ulang di sini dari tabel SRS Bab 5.3, bukan dibaca dari
// kode yang diujinya.
func TestMatriksKirim_SesuaiTabelSRS(t *testing.T) {
	mau := map[string][]string{
		delivery.StatusAssigned:    {delivery.StatusAccepted},
		delivery.StatusAccepted:    {delivery.StatusOnTheWay},
		delivery.StatusOnTheWay:    {delivery.StatusArrived, delivery.StatusFailed},
		delivery.StatusArrived:     {delivery.StatusDelivered, delivery.StatusFailed},
		delivery.StatusFailed:      {delivery.StatusRescheduled},
		delivery.StatusRescheduled: {delivery.StatusAssigned},
		delivery.StatusDelivered:   nil,
	}
	for dari, tujuan := range mau {
		got := delivery.AllowedFrom(dari)
		if len(got) != len(tujuan) {
			t.Fatalf("dari %s ada %d tujuan (%v), seharusnya %d (%v)",
				dari, len(got), got, len(tujuan), tujuan)
		}
		for _, ke := range tujuan {
			if _, ok := delivery.Allowed(dari, ke); !ok {
				t.Fatalf("%s ke %s seharusnya diizinkan", dari, ke)
			}
		}
	}
	if !delivery.Terminal(delivery.StatusDelivered) {
		t.Fatal("DELIVERED seharusnya status akhir")
	}
}

// TestTransisiKirim_YangTidakTerdaftarDitolak menjaga contoh yang disebut
// langsung pada SRS-DLV-003: transisi dari ARRIVED langsung ke ASSIGNED
// ditolak.
func TestTransisiKirim_YangTidakTerdaftarDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	_, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusAssigned,
		delivery.ChangeInput{ActorID: &drv})
	if !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("ARRIVED ke ASSIGNED seharusnya ditolak, dapat %v", err)
	}

	// Melompat juga ditolak.
	kirim2, drv2, _, _ := l.tugas(t)
	_, err = l.kirim.ChangeStatus(ctx, kirim2.ID, delivery.StatusDelivered,
		delivery.ChangeInput{ActorID: &drv2})
	if !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("ASSIGNED ke DELIVERED seharusnya ditolak, dapat %v", err)
	}
}

// TestTransisiKirim_HanyaDriverYangDitugaskan adalah uji keamanan. Seluruh
// driver memegang izin delivery.update_own, jadi izin saja tidak membedakan
// tugas siapa ini.
func TestTransisiKirim_HanyaDriverYangDitugaskan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, adm, _ := l.tugas(t)

	// Driver lain dari depo yang sama.
	lain := l.buatDriver(t, kirim.DepotID, true)
	_, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &lain})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("driver lain seharusnya ditolak, dapat %v", err)
	}

	// Admin pun tidak boleh menerima tugas atas nama driver.
	_, err = l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &adm})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("admin seharusnya ditolak, dapat %v", err)
	}

	// Tanpa pelaku juga ditolak.
	_, err = l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusAccepted,
		delivery.ChangeInput{})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("tanpa pelaku seharusnya ditolak, dapat %v", err)
	}

	// Driver yang ditugaskan berhasil.
	if _, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &drv}); err != nil {
		t.Fatalf("driver yang ditugaskan seharusnya berhasil: %v", err)
	}
}

// TestTransisiKirim_BerangkatMenaikkanPesanan menjaga agar pesanan dan
// pengirimannya tidak berbeda cerita.
func TestTransisiKirim_BerangkatMenaikkanPesanan(t *testing.T) {
	l := siapkan(t)
	kirim, drv, _, o := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted)
	if s := l.statusPesanan(t, o.ID); s != order.StatusScheduled {
		t.Fatalf("status pesanan %q, menerima tugas belum mengubah pesanan", s)
	}

	l.majukan(t, kirim.ID, drv, delivery.StatusOnTheWay)
	if s := l.statusPesanan(t, o.ID); s != order.StatusOutForDelivery {
		t.Fatalf("status pesanan %q, seharusnya OUT_FOR_DELIVERY", s)
	}
}

// TestTransisiKirim_SelesaiTanpaBuktiDitolak menjaga SRS-DLV-004. Inilah
// penjaga yang mencegah pengiriman dinyatakan selesai tanpa bukti apa pun.
func TestTransisiKirim_SelesaiTanpaBuktiDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, o := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	_, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusDelivered,
		delivery.ChangeInput{ActorID: &drv})
	if !errors.Is(err, delivery.ErrProofRequired) {
		t.Fatalf("galat %v, seharusnya ErrProofRequired", err)
	}
	// Pesanan belum selesai karena pengirimannya belum selesai.
	if s := l.statusPesanan(t, o.ID); s == order.StatusCompleted {
		t.Fatal("pesanan selesai padahal bukti belum ada")
	}

	// Sesudah bukti diunggah, baru boleh selesai.
	if _, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey:     "pod/" + kirim.ID.String() + ".jpg",
		ReceiverName: "Pak Budi", ActorID: &drv,
	}); err != nil {
		t.Fatalf("menyimpan bukti: %v", err)
	}
	got, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusDelivered,
		delivery.ChangeInput{ActorID: &drv})
	if err != nil {
		t.Fatalf("menyelesaikan pengiriman: %v", err)
	}
	if got.CompletedAt == nil {
		t.Fatal("waktu penyelesaian tidak tercatat")
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusCompleted {
		t.Fatalf("status pesanan %q, seharusnya COMPLETED", s)
	}
}

// TestBukti_HanyaSetelahTiba menjaga agar bukti tidak dibuat di tempat lain.
func TestBukti_HanyaSetelahTiba(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	_, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/x.jpg", ActorID: &drv,
	})
	if !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("bukti sebelum tiba seharusnya ditolak, dapat %v", err)
	}
}

// TestBukti_DriverLainDitolak menjaga kepemilikan pada jalur pengunggahan.
func TestBukti_DriverLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	lain := l.buatDriver(t, kirim.DepotID, true)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	_, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/x.jpg", ActorID: &lain,
	})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("galat %v, seharusnya ErrNotAssignedDriver", err)
	}
}

// TestBukti_TanpaFotoDitolak menjaga agar bukti kosong tidak lolos.
func TestBukti_TanpaFotoDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	for _, kunci := range []string{"", "   "} {
		_, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
			PhotoKey: kunci, ActorID: &drv,
		})
		if !errors.Is(err, delivery.ErrProofRequired) {
			t.Fatalf("kunci foto %q seharusnya ditolak, dapat %v", kunci, err)
		}
	}
}

// TestBukti_UnggahUlangMemperbaruiBukanGagal menjaga agar perangkat yang
// jaringannya tersendat dapat mengirim ulang tanpa bertemu kekangan DB-06.
func TestBukti_UnggahUlangMemperbaruiBukanGagal(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	if _, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/pertama.jpg", ActorID: &drv,
	}); err != nil {
		t.Fatalf("unggah pertama: %v", err)
	}
	p, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/kedua.jpg", ReceiverName: "Bu Ani", ActorID: &drv,
	})
	if err != nil {
		t.Fatalf("unggah ulang seharusnya berhasil: %v", err)
	}
	if p.PhotoKey != "pod/kedua.jpg" || p.ReceiverName != "Bu Ani" {
		t.Fatalf("bukti tidak diperbarui: %+v", p)
	}
}

// TestTransisiKirim_GagalWajibBeralasan menjaga SRS-DLV-003.
func TestTransisiKirim_GagalWajibBeralasan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	_, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusFailed,
		delivery.ChangeInput{ActorID: &drv})
	if !errors.Is(err, delivery.ErrReasonRequired) {
		t.Fatalf("galat %v, seharusnya ErrReasonRequired", err)
	}
}

// TestTransisiKirim_GagalMengembalikanPesananKeJadwal menjaga agar pengiriman
// yang gagal tidak membatalkan pesanan: pengirimannya masih akan diulang,
// sehingga kuota slotnya tidak dilepas.
func TestTransisiKirim_GagalMengembalikanPesananKeJadwal(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, o := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	sebelum := l.kuotaSlotPesanan(t, o.ID)

	got, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusFailed,
		delivery.ChangeInput{ActorID: &drv, Reason: "penerima tidak ada di tempat"})
	if err != nil {
		t.Fatalf("mencatat gagal kirim: %v", err)
	}
	if got.FailureReason != "penerima tidak ada di tempat" {
		t.Fatalf("alasan gagal %q", got.FailureReason)
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusScheduled {
		t.Fatalf("status pesanan %q, seharusnya kembali SCHEDULED", s)
	}
	if n := l.kuotaSlotPesanan(t, o.ID); n != sebelum {
		t.Fatalf("kuota slot berubah dari %d menjadi %d, gagal kirim tidak boleh melepasnya",
			sebelum, n)
	}
}

// TestKendala_DilaporkanTanpaMenggagalkanPengiriman menjaga SRS-DLV-005.
// Driver yang terlambat karena jalan rusak perlu melaporkannya tanpa menandai
// pengirimannya gagal.
func TestKendala_DilaporkanTanpaMenggagalkanPengiriman(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	got, err := l.kirim.ReportIssue(ctx, kirim.ID, delivery.IssueInput{
		Category: "JALAN_RUSAK", Note: "jalan ditutup, cari jalan lain", ActorID: &drv,
	})
	if err != nil {
		t.Fatalf("melaporkan kendala: %v", err)
	}
	if got.Category != "JALAN_RUSAK" {
		t.Fatalf("kategori %q", got.Category)
	}

	// Status pengiriman tidak berubah.
	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.Status != delivery.StatusOnTheWay {
		t.Fatalf("status %q, laporan kendala tidak boleh mengubahnya", lagi.Status)
	}

	// Dan kendalanya terlihat pada detail pengiriman.
	daftar, err := l.kirim.Issues(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca kendala: %v", err)
	}
	if len(daftar) != 1 {
		t.Fatalf("kendala tercatat %d, seharusnya 1", len(daftar))
	}
}

func TestKendala_TanpaKategoriDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	_, err := l.kirim.ReportIssue(ctx, kirim.ID, delivery.IssueInput{
		Category: "  ", ActorID: &drv,
	})
	if !errors.Is(err, delivery.ErrCategoryRequired) {
		t.Fatalf("galat %v, seharusnya ErrCategoryRequired", err)
	}
}

func TestKendala_DriverLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, _, _, _ := l.tugas(t)
	lain := l.buatDriver(t, kirim.DepotID, true)

	_, err := l.kirim.ReportIssue(ctx, kirim.ID, delivery.IssueInput{
		Category: "LAIN", ActorID: &lain,
	})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("galat %v, seharusnya ErrNotAssignedDriver", err)
	}
}

// TestIzinKirim_DiambilDariMatriks menjaga agar lapisan HTTP tidak menuliskan
// ulang izin tiap transisi.
func TestIzinKirim_DiambilDariMatriks(t *testing.T) {
	kasus := []struct{ dari, ke, izin string }{
		{delivery.StatusAssigned, delivery.StatusAccepted, "delivery.update_own"},
		{delivery.StatusArrived, delivery.StatusDelivered, "delivery.update_own"},
		{delivery.StatusFailed, delivery.StatusRescheduled, "dispatch.manage"},
		{delivery.StatusRescheduled, delivery.StatusAssigned, "dispatch.manage"},
	}
	for _, k := range kasus {
		got, err := delivery.PermissionFor(k.dari, k.ke)
		if err != nil {
			t.Fatalf("%s ke %s: %v", k.dari, k.ke, err)
		}
		if got != k.izin {
			t.Fatalf("%s ke %s butuh %q, seharusnya %q", k.dari, k.ke, got, k.izin)
		}
	}
	if _, err := delivery.PermissionFor(delivery.StatusDelivered, delivery.StatusArrived); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatal("transisi tidak sah seharusnya ErrInvalidTransition")
	}
}

// TestPelacakanAktif_HanyaSaatMenujuAtauTiba menjaga SRS-TRK-001. Merekam di
// luar itu berarti memantau driver saat ia belum berangkat atau sudah selesai.
func TestPelacakanAktif_HanyaSaatMenujuAtauTiba(t *testing.T) {
	aktif := map[string]bool{
		delivery.StatusAssigned:    false,
		delivery.StatusAccepted:    false,
		delivery.StatusOnTheWay:    true,
		delivery.StatusArrived:     true,
		delivery.StatusDelivered:   false,
		delivery.StatusFailed:      false,
		delivery.StatusRescheduled: false,
	}
	for status, mau := range aktif {
		if got := delivery.TrackingActive(status); got != mau {
			t.Fatalf("status %s pelacakan %v, seharusnya %v", status, got, mau)
		}
	}
}
