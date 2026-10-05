package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iceman/backend/internal/delivery"
)

// siapJalan menyiapkan pengiriman yang sudah berstatus menuju lokasi, dengan
// persetujuan pelacakan sudah diberikan.
func (l *lingkungan) siapJalan(t *testing.T) (*delivery.Delivery, [16]byte) {
	t.Helper()
	kirim, drv, _, _ := l.tugas(t)
	if err := l.kirim.GrantConsent(context.Background(), drv); err != nil {
		t.Fatalf("memberi persetujuan: %v", err)
	}
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)
	return kirim, drv
}

func posisi(lat, lng float64, menitLalu int) delivery.Fix {
	return delivery.Fix{
		Latitude: lat, Longitude: lng,
		DeviceTime: time.Now().Add(-time.Duration(menitLalu) * time.Minute).Truncate(time.Second),
	}
}

// TestPosisi_TersimpanDanDisalinKePengiriman menjaga SRS-TRK-004: posisi
// terakhir disalin ke data pengiriman agar tetap tersedia setelah rekaman
// mentah dihapus karena masa simpan.
func TestPosisi_TersimpanDanDisalinKePengiriman(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	hasil, err := l.kirim.RecordPositions(ctx, kirim.ID, drv, []delivery.Fix{
		posisi(1.4750, 124.8430, 3),
		posisi(1.4760, 124.8440, 2),
		posisi(1.4770, 124.8450, 1),
	})
	if err != nil {
		t.Fatalf("menyimpan posisi: %v", err)
	}
	if hasil.Accepted != 3 {
		t.Fatalf("posisi diterima %d, seharusnya 3", hasil.Accepted)
	}

	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.LastLatitude == nil || *lagi.LastLatitude != 1.4770 {
		t.Fatalf("posisi terakhir salah: %+v", lagi.LastLatitude)
	}

	jejak, err := l.kirim.Trail(ctx, kirim.ID, 0)
	if err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}
	if len(jejak) != 3 {
		t.Fatalf("titik jejak %d, seharusnya 3", len(jejak))
	}
	// Jejak terurut menurut waktu perangkat, bukan urutan kedatangan.
	if !jejak[0].DeviceTime.Before(jejak[2].DeviceTime) {
		t.Fatal("jejak tidak terurut menurut waktu perangkat")
	}
}

// TestPosisi_KirimUlangTidakMenggandakan menjaga kunci alami posisi. Perangkat
// yang kehilangan sinyal mengirim ulang antreannya dari awal, dan antrean itu
// memuat posisi yang sebagian sudah diterima. Tanpa idempotensi, satu
// perjalanan terekam berkali kali dan jejaknya tidak dapat dipakai menghitung
// jarak.
func TestPosisi_KirimUlangTidakMenggandakan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	kumpulan := []delivery.Fix{
		posisi(1.4750, 124.8430, 3),
		posisi(1.4760, 124.8440, 2),
	}

	pertama, err := l.kirim.RecordPositions(ctx, kirim.ID, drv, kumpulan)
	if err != nil {
		t.Fatalf("kiriman pertama: %v", err)
	}
	if pertama.Accepted != 2 {
		t.Fatalf("diterima %d, seharusnya 2", pertama.Accepted)
	}

	// Perangkat mengirim ulang antrean yang sama, ditambah satu posisi baru.
	kedua, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		append(kumpulan, posisi(1.4770, 124.8450, 1)))
	if err != nil {
		t.Fatalf("kiriman kedua: %v", err)
	}
	if kedua.Accepted != 1 {
		t.Fatalf("diterima %d, seharusnya hanya 1 yang baru", kedua.Accepted)
	}
	if kedua.Duplicate != 2 {
		t.Fatalf("duplikat %d, seharusnya 2", kedua.Duplicate)
	}

	jejak, err := l.kirim.Trail(ctx, kirim.ID, 0)
	if err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}
	if len(jejak) != 3 {
		t.Fatalf("titik jejak %d, seharusnya 3 bukan 5", len(jejak))
	}
}

// TestPosisi_TanpaPersetujuanDitolak menjaga SEC-012. Pelacakan menyentuh
// urusan pemantauan karyawan, jadi perekaman tidak boleh berjalan sebelum
// driver menyetujuinya.
func TestPosisi_TanpaPersetujuanDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	_, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 1)})
	if !errors.Is(err, delivery.ErrConsentRequired) {
		t.Fatalf("galat %v, seharusnya ErrConsentRequired", err)
	}
}

// TestPosisi_PencabutanPersetujuanMenghentikanSeketika menjaga janji bahwa
// pencabutan berlaku langsung, bukan menunggu tugas berikutnya.
func TestPosisi_PencabutanPersetujuanMenghentikanSeketika(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 2)}); err != nil {
		t.Fatalf("menyimpan posisi: %v", err)
	}

	if err := l.kirim.RevokeConsent(ctx, drv); err != nil {
		t.Fatalf("mencabut persetujuan: %v", err)
	}
	ada, err := l.kirim.HasConsent(ctx, drv)
	if err != nil {
		t.Fatalf("membaca persetujuan: %v", err)
	}
	if ada {
		t.Fatal("persetujuan seharusnya sudah tercabut")
	}

	_, err = l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.476, 124.844, 1)})
	if !errors.Is(err, delivery.ErrConsentRequired) {
		t.Fatalf("galat %v, seharusnya ErrConsentRequired", err)
	}

	// Rekaman yang sudah ada tidak dihapus: itu bukti pengiriman yang sudah
	// berlangsung.
	jejak, err := l.kirim.Trail(ctx, kirim.ID, 0)
	if err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}
	if len(jejak) != 1 {
		t.Fatalf("titik jejak %d, rekaman lama seharusnya tetap ada", len(jejak))
	}
}

// TestPosisi_HanyaSaatMenujuAtauTiba menjaga SRS-TRK-001.
func TestPosisi_HanyaSaatMenujuAtauTiba(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	if err := l.kirim.GrantConsent(ctx, drv); err != nil {
		t.Fatalf("memberi persetujuan: %v", err)
	}

	// Belum berangkat.
	_, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 1)})
	if !errors.Is(err, delivery.ErrTrackingInactive) {
		t.Fatalf("sebelum berangkat seharusnya ditolak, dapat %v", err)
	}

	// Berangkat, lalu tiba, lalu selesai.
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)
	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 3)}); err != nil {
		t.Fatalf("saat menuju lokasi seharusnya boleh: %v", err)
	}
	l.majukan(t, kirim.ID, drv, delivery.StatusArrived)
	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.476, 124.844, 2)}); err != nil {
		t.Fatalf("saat tiba seharusnya boleh: %v", err)
	}

	if _, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/x.jpg", ActorID: &drv,
	}); err != nil {
		t.Fatalf("menyimpan bukti: %v", err)
	}
	l.majukan(t, kirim.ID, drv, delivery.StatusDelivered)

	// Sesudah selesai, perekaman berhenti seluruhnya.
	_, err = l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.477, 124.845, 1)})
	if !errors.Is(err, delivery.ErrTrackingInactive) {
		t.Fatalf("sesudah selesai seharusnya ditolak, dapat %v", err)
	}
}

// TestPosisi_DriverLainDitolak menjaga kepemilikan pada jalur pelacakan.
func TestPosisi_DriverLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, _ := l.siapJalan(t)
	lain := l.buatDriver(t, kirim.DepotID, true)
	if err := l.kirim.GrantConsent(ctx, lain); err != nil {
		t.Fatalf("memberi persetujuan: %v", err)
	}

	_, err := l.kirim.RecordPositions(ctx, kirim.ID, lain,
		[]delivery.Fix{posisi(1.475, 124.843, 1)})
	if !errors.Is(err, delivery.ErrNotAssignedDriver) {
		t.Fatalf("galat %v, seharusnya ErrNotAssignedDriver", err)
	}
}

// TestPosisi_KoordinatRusakDilewatiBukanMenggagalkanKumpulan menjaga agar satu
// koordinat buruk dari sensor tidak membuang perjalanan yang sudah terekam.
func TestPosisi_KoordinatRusakDilewatiBukanMenggagalkanKumpulan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	hasil, err := l.kirim.RecordPositions(ctx, kirim.ID, drv, []delivery.Fix{
		posisi(1.4750, 124.8430, 3),
		posisi(999, 124.8440, 2),
		posisi(1.4770, 124.8450, 1),
	})
	if err != nil {
		t.Fatalf("satu koordinat rusak tidak boleh menggagalkan kumpulan: %v", err)
	}
	if hasil.Accepted != 2 {
		t.Fatalf("diterima %d, seharusnya 2", hasil.Accepted)
	}
	if len(hasil.Rejected) != 1 {
		t.Fatalf("ditolak %d, seharusnya 1 beserta alasannya", len(hasil.Rejected))
	}
}

// TestPosisi_KumpulanTerlambatTidakMenarikPosisiMundur menjaga agar posisi
// terakhir tidak berpindah ke belakang ketika kumpulan lama baru tiba setelah
// kumpulan baru.
func TestPosisi_KumpulanTerlambatTidakMenarikPosisiMundur(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	// Kumpulan yang baru tiba lebih dahulu.
	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.4770, 124.8450, 1)}); err != nil {
		t.Fatalf("kumpulan baru: %v", err)
	}
	// Kumpulan lama menyusul.
	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.4750, 124.8430, 10)}); err != nil {
		t.Fatalf("kumpulan lama: %v", err)
	}

	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.LastLatitude == nil || *lagi.LastLatitude != 1.4770 {
		t.Fatalf("posisi terakhir tertarik mundur: %+v", lagi.LastLatitude)
	}
}

// TestPetaLangsung_PosisiUsangDitandai menjaga SRS-TRK-002. Menampilkan posisi
// lama seolah terkini membuat admin menelepon driver yang disangka berhenti,
// padahal yang hilang hanya sinyalnya.
func TestPetaLangsung_PosisiUsangDitandai(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)

	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 1)}); err != nil {
		t.Fatalf("menyimpan posisi: %v", err)
	}

	segar, err := l.kirim.LivePositions(ctx, kirim.DepotID, time.Now())
	if err != nil {
		t.Fatalf("membaca peta: %v", err)
	}
	if len(segar) != 1 {
		t.Fatalf("driver di peta %d, seharusnya 1", len(segar))
	}
	if segar[0].Stale {
		t.Fatal("posisi semenit lalu seharusnya belum usang")
	}
	if segar[0].OrderNo == "" || segar[0].DriverName == "" {
		t.Fatalf("peta harus menyebut nomor pesanan dan nama driver: %+v", segar[0])
	}

	// Dilihat dari masa depan, posisi yang sama menjadi usang.
	nanti, err := l.kirim.LivePositions(ctx, kirim.DepotID,
		time.Now().Add(delivery.StaleAfter+time.Minute))
	if err != nil {
		t.Fatalf("membaca peta: %v", err)
	}
	if !nanti[0].Stale {
		t.Fatal("posisi lebih dari lima belas menit seharusnya ditandai usang")
	}
}

// TestPetaLangsung_HanyaDepoSendiri menjaga SRS-TRK-002: admin hanya melihat
// driver dari depo yang menjadi kewenangannya.
func TestPetaLangsung_HanyaDepoSendiri(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirimA, _ := l.siapJalan(t)
	depoLain := l.buatDepo(t)

	daftar, err := l.kirim.LivePositions(ctx, depoLain.DepotID, time.Now())
	if err != nil {
		t.Fatalf("membaca peta depo lain: %v", err)
	}
	for _, p := range daftar {
		if p.DeliveryID == kirimA.ID {
			t.Fatal("pengiriman depo lain terlihat pada peta")
		}
	}
}

// TestPetaLangsung_HanyaYangSedangMengantar menjaga agar peta tidak penuh
// driver yang belum berangkat atau sudah selesai.
func TestPetaLangsung_HanyaYangSedangMengantar(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	kosong, err := l.kirim.LivePositions(ctx, kirim.DepotID, time.Now())
	if err != nil {
		t.Fatalf("membaca peta: %v", err)
	}
	if len(kosong) != 0 {
		t.Fatalf("driver belum berangkat seharusnya tidak di peta, dapat %d", len(kosong))
	}

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)
	ada, err := l.kirim.LivePositions(ctx, kirim.DepotID, time.Now())
	if err != nil {
		t.Fatalf("membaca peta: %v", err)
	}
	if len(ada) != 1 {
		t.Fatalf("driver yang berangkat seharusnya di peta, dapat %d", len(ada))
	}
}

// TestJejak_PembacaanTercatatPadaAudit menjaga SRS-TRK-004. Posisi driver
// adalah data pribadi, jadi siapa yang membacanya harus dapat ditelusuri.
func TestJejak_PembacaanTercatatPadaAudit(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv := l.siapJalan(t)
	adm := l.buatAdmin(t)

	if _, err := l.kirim.RecordPositions(ctx, kirim.ID, drv,
		[]delivery.Fix{posisi(1.475, 124.843, 1)}); err != nil {
		t.Fatalf("menyimpan posisi: %v", err)
	}

	if _, err := l.kirim.Trail(sebagai(adm), kirim.ID, 0); err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}

	var jumlah int
	err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_trail
		WHERE  entity = 'driver_locations' AND entity_id = $1 AND actor_id = $2`,
		kirim.ID, adm).Scan(&jumlah)
	if err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("catatan akses jejak posisi %d, seharusnya 1", jumlah)
	}
}
