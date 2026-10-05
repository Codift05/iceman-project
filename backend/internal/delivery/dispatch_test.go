package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/delivery"
	"github.com/iceman/backend/internal/order"
)

// TestPenugasan_MenaikkanPesananKeTerjadwal menutup celah yang sengaja
// ditinggalkan pada matriks transisi pesanan: PROCESSING ke SCHEDULED butuh
// driver, dan efek sampingnya membuat baris pengiriman.
//
// Keduanya harus terjadi bersama. Pesanan terjadwal tanpa baris pengiriman
// berarti tidak ada yang mengantarnya, dan baris pengiriman untuk pesanan yang
// belum terjadwal berarti driver menyiapkan muatan tanpa dasar.
func TestPenugasan_MenaikkanPesananKeTerjadwal(t *testing.T) {
	l := siapkan(t)
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)
	o := l.buatPesanan(t, d)

	got, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
		OrderID: o.ID, DriverID: drv, SequenceNo: 1,
	})
	if err != nil {
		t.Fatalf("menugaskan driver: %v", err)
	}
	if got.Status != delivery.StatusAssigned {
		t.Fatalf("status pengiriman %q, seharusnya ASSIGNED", got.Status)
	}
	if got.DriverID != drv {
		t.Fatal("driver pada pengiriman salah")
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusScheduled {
		t.Fatalf("status pesanan %q, seharusnya SCHEDULED", s)
	}

	// Alamat tujuan terbaca dari pesanan, tidak disalin ulang.
	if got.AddressLine != "Jl. Uji No. 1" || got.RecipientName != "Pak Budi" {
		t.Fatalf("alamat tujuan tidak terbaca: %+v", got)
	}
	if got.OrderNo != o.OrderNo {
		t.Fatalf("nomor pesanan %q, seharusnya %q", got.OrderNo, o.OrderNo)
	}
}

// TestPenugasan_PesananBelumSiapDitolak menjaga SRS-DLV-001. Pesanan yang
// masih menunggu pembayaran belum tentu jadi, dan menugaskannya berarti driver
// menyiapkan muatan untuk pesanan yang dapat hangus.
func TestPenugasan_PesananBelumSiapDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	o := l.buatPesanan(t, d)

	// Turunkan pesanan ke WAITING_PAYMENT untuk meniru pesanan ritel.
	if _, err := l.pool.Exec(ctx,
		`UPDATE orders SET status = 'WAITING_PAYMENT' WHERE id = $1`, o.ID); err != nil {
		t.Fatalf("mengubah status pesanan: %v", err)
	}

	_, err := l.kirim.Assign(ctx, delivery.AssignInput{OrderID: o.ID, DriverID: drv})
	if !errors.Is(err, delivery.ErrOrderNotReady) {
		t.Fatalf("galat %v, seharusnya ErrOrderNotReady", err)
	}
}

// TestPenugasan_DriverTidakAktifDitolak menjaga SRS-DLV-001.
func TestPenugasan_DriverTidakAktifDitolak(t *testing.T) {
	l := siapkan(t)
	d := l.buatDepo(t)
	mati := l.buatDriver(t, d.DepotID, false)
	o := l.buatPesanan(t, d)

	_, err := l.kirim.Assign(context.Background(), delivery.AssignInput{
		OrderID: o.ID, DriverID: mati,
	})
	if !errors.Is(err, delivery.ErrDriverInactive) {
		t.Fatalf("galat %v, seharusnya ErrDriverInactive", err)
	}
}

// TestPenugasan_DriverDepoLainDitolak menjaga DB-10 dari sisi kode, dengan
// galat yang dapat dibaca pengguna. Pemicu basis data tetap ada sebagai jaring
// pengaman, namun pesannya tidak untuk dibaca admin.
func TestPenugasan_DriverDepoLainDitolak(t *testing.T) {
	l := siapkan(t)
	depoA := l.buatDepo(t)
	depoB := l.buatDepo(t)
	drvB := l.buatDriver(t, depoB.DepotID, true)
	o := l.buatPesanan(t, depoA)

	_, err := l.kirim.Assign(context.Background(), delivery.AssignInput{
		OrderID: o.ID, DriverID: drvB,
	})
	if !errors.Is(err, delivery.ErrDriverOtherDepot) {
		t.Fatalf("galat %v, seharusnya ErrDriverOtherDepot", err)
	}
}

// TestPenugasan_BukanDriverDitolak menjaga agar pengguna kantor tidak dapat
// ditugaskan mengantar.
func TestPenugasan_BukanDriverDitolak(t *testing.T) {
	l := siapkan(t)
	d := l.buatDepo(t)
	adm := l.buatAdmin(t)
	o := l.buatPesanan(t, d)

	_, err := l.kirim.Assign(context.Background(), delivery.AssignInput{
		OrderID: o.ID, DriverID: adm,
	})
	if !errors.Is(err, delivery.ErrDriverInactive) {
		t.Fatalf("galat %v, seharusnya ErrDriverInactive", err)
	}
}

// TestPenugasanUlang_DriverLamaKehilanganAkses menjaga janji SRS-DLV-001:
// menugaskan ulang membuat driver lama kehilangan akses ke pengiriman itu.
func TestPenugasanUlang_DriverLamaKehilanganAkses(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	d := l.buatDepo(t)
	lama := l.buatDriver(t, d.DepotID, true)
	baru := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)
	o := l.buatPesanan(t, d)

	got, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
		OrderID: o.ID, DriverID: lama,
	})
	if err != nil {
		t.Fatalf("penugasan pertama: %v", err)
	}
	// Driver lama sempat menerima tugasnya.
	if _, err := l.kirim.ChangeStatus(ctx, got.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &lama}); err != nil {
		t.Fatalf("driver lama menerima tugas: %v", err)
	}

	ulang, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
		OrderID: o.ID, DriverID: baru, Reason: "driver lama sakit",
	})
	if err != nil {
		t.Fatalf("penugasan ulang: %v", err)
	}
	if ulang.ID != got.ID {
		t.Fatal("penugasan ulang seharusnya memakai baris yang sama, bukan baris baru")
	}
	if ulang.DriverID != baru {
		t.Fatal("driver belum berpindah")
	}
	// Status kembali ke ASSIGNED karena kemajuan driver lama tidak berlaku
	// bagi penggantinya.
	if ulang.Status != delivery.StatusAssigned {
		t.Fatalf("status %q, seharusnya kembali ASSIGNED", ulang.Status)
	}
	if ulang.AcceptedAt != nil {
		t.Fatal("waktu penerimaan driver lama seharusnya dibersihkan")
	}

	// Driver lama tidak dapat lagi membaca maupun mengubah pengiriman itu.
	if _, err := l.kirim.GetForDriver(ctx, lama, got.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("driver lama masih dapat membaca, galat %v", err)
	}
	_, err = l.kirim.ChangeStatus(ctx, got.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &lama})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("driver lama masih dapat mengubah, galat %v", err)
	}

	// Riwayat penugasan tetap tersimpan.
	riwayat, err := l.kirim.Assignments(ctx, got.ID)
	if err != nil {
		t.Fatalf("membaca riwayat penugasan: %v", err)
	}
	if len(riwayat) != 2 {
		t.Fatalf("baris riwayat penugasan %d, seharusnya 2", len(riwayat))
	}
	if riwayat[0].FromDriver != nil {
		t.Fatal("penugasan pertama seharusnya tanpa driver asal")
	}
	if riwayat[1].FromDriver == nil || *riwayat[1].FromDriver != lama {
		t.Fatal("penugasan ulang tidak menyebut driver asal")
	}
	if riwayat[1].Reason != "driver lama sakit" {
		t.Fatalf("alasan penugasan ulang %q", riwayat[1].Reason)
	}
	if riwayat[1].ActorID == nil || *riwayat[1].ActorID != adm {
		t.Fatal("pelaku penugasan ulang tidak tercatat")
	}
}

// TestDaftarTugas_DriverHanyaMelihatMiliknya menjaga SRS-DLV-002.
func TestDaftarTugas_DriverHanyaMelihatMiliknya(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	d := l.buatDepo(t)
	satu := l.buatDriver(t, d.DepotID, true)
	dua := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)

	o1 := l.buatPesanan(t, d)
	o2 := l.buatPesanan(t, d)
	if _, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
		OrderID: o1.ID, DriverID: satu,
	}); err != nil {
		t.Fatalf("penugasan pertama: %v", err)
	}
	if _, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
		OrderID: o2.ID, DriverID: dua,
	}); err != nil {
		t.Fatalf("penugasan kedua: %v", err)
	}

	daftar, err := l.kirim.List(ctx, delivery.ListFilter{DriverID: &satu})
	if err != nil {
		t.Fatalf("membaca daftar tugas: %v", err)
	}
	if len(daftar) != 1 || daftar[0].OrderID != o1.ID {
		t.Fatalf("driver melihat tugas yang bukan miliknya: %+v", daftar)
	}
}

// TestDaftarTugas_TerurutMengikutiUrutanAdmin menjaga SRS-DLV-002. Urutan itu
// yang dilihat driver sebagai rencana rutenya.
func TestDaftarTugas_TerurutMengikutiUrutanAdmin(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)

	o1 := l.buatPesanan(t, d)
	o2 := l.buatPesanan(t, d)
	o3 := l.buatPesanan(t, d)

	// Sengaja ditugaskan tidak berurutan.
	for _, p := range []struct {
		order  uuid.UUID
		urutan int32
	}{{o1.ID, 3}, {o2.ID, 1}, {o3.ID, 2}} {
		if _, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{
			OrderID: p.order, DriverID: drv, SequenceNo: p.urutan,
		}); err != nil {
			t.Fatalf("menugaskan: %v", err)
		}
	}

	daftar, err := l.kirim.List(ctx, delivery.ListFilter{DriverID: &drv})
	if err != nil {
		t.Fatalf("membaca daftar tugas: %v", err)
	}
	mau := []uuid.UUID{o2.ID, o3.ID, o1.ID}
	for i, id := range mau {
		if daftar[i].OrderID != id {
			t.Fatalf("urutan ke-%d salah: %+v", i, daftar[i])
		}
	}

	// Admin dapat mengubah urutannya.
	if err := l.kirim.SetSequence(ctx, daftar[2].ID, 0); err != nil {
		t.Fatalf("mengubah urutan: %v", err)
	}
	lagi, err := l.kirim.List(ctx, delivery.ListFilter{DriverID: &drv})
	if err != nil {
		t.Fatalf("membaca daftar tugas: %v", err)
	}
	if lagi[0].OrderID != o1.ID {
		t.Fatal("urutan tidak berubah setelah diatur admin")
	}
}

func TestPenugasan_PengirimanTidakAda(t *testing.T) {
	l := siapkan(t)
	if err := l.kirim.SetSequence(context.Background(), uuid.New(), 1); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
	if _, err := l.kirim.Get(context.Background(), uuid.New()); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
}

func TestPenugasan_PesananTidakAda(t *testing.T) {
	l := siapkan(t)
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	_, err := l.kirim.Assign(context.Background(), delivery.AssignInput{
		OrderID: uuid.New(), DriverID: drv,
	})
	if !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya order.ErrNotFound", err)
	}
}

// TestPenugasan_WaktuPerangkatTersimpan menjaga SRS-DLV-003: waktu perangkat
// dan waktu server keduanya tersimpan untuk penelusuran.
func TestPenugasan_WaktuPerangkatTersimpan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)
	o := l.buatPesanan(t, d)

	got, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{OrderID: o.ID, DriverID: drv})
	if err != nil {
		t.Fatalf("menugaskan: %v", err)
	}

	// Jam perangkat sengaja dibuat meleset lima menit ke belakang.
	waktuPerangkat := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	if _, err := l.kirim.ChangeStatus(ctx, got.ID, delivery.StatusAccepted,
		delivery.ChangeInput{ActorID: &drv, DeviceTime: ptrWaktu(waktuPerangkat)}); err != nil {
		t.Fatalf("menerima tugas: %v", err)
	}

	riwayat, err := l.kirim.History(ctx, got.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	akhir := riwayat[len(riwayat)-1]
	if akhir.DeviceTime == nil {
		t.Fatal("waktu perangkat tidak tersimpan")
	}
	if !akhir.DeviceTime.Equal(waktuPerangkat) {
		t.Fatalf("waktu perangkat %v, seharusnya %v", akhir.DeviceTime, waktuPerangkat)
	}
	// Waktu server tetap dipakai sebagai urutan resmi, jadi tidak boleh ikut
	// bergeser ke belakang mengikuti jam perangkat.
	if akhir.OccurredAt.Before(waktuPerangkat.Add(time.Minute)) {
		t.Fatalf("waktu server %v ikut bergeser mengikuti jam perangkat", akhir.OccurredAt)
	}
}
