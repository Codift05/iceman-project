package delivery_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/delivery"
)

func perintah(id string, deliveryID uuid.UUID, status string, menitLalu int) delivery.Command {
	w := time.Now().Add(-time.Duration(menitLalu) * time.Minute).Truncate(time.Second)
	return delivery.Command{
		ClientEventID: id, DeliveryID: deliveryID, Status: status, DeviceTime: &w,
	}
}

// TestSync_PerintahSamaTigaKaliSatuPerubahan menjaga janji SRS-DLV-006 yang
// paling langsung: mengirim perintah yang sama tiga kali menghasilkan satu
// perubahan status.
func TestSync_PerintahSamaTigaKaliSatuPerubahan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	kunci := "evt-" + uuid.NewString()
	cmd := perintah(kunci, kirim.ID, delivery.StatusAccepted, 1)

	for i := 1; i <= 3; i++ {
		hasil, err := l.kirim.Sync(ctx, drv, []delivery.Command{cmd})
		if err != nil {
			t.Fatalf("kiriman ke-%d: %v", i, err)
		}
		if len(hasil) != 1 {
			t.Fatalf("hasil ke-%d berjumlah %d", i, len(hasil))
		}
		// Ketiganya dijawab berhasil, bukan galat. Dari sisi perangkat
		// kirimannya memang berhasil; ia mengulang karena belum menerima
		// konfirmasi.
		if hasil[0].Outcome != delivery.OutcomeApplied {
			t.Fatalf("kiriman ke-%d dijawab %q, seharusnya APPLIED", i, hasil[0].Outcome)
		}
		if hasil[0].ServerStatus != delivery.StatusAccepted {
			t.Fatalf("status server %q", hasil[0].ServerStatus)
		}
	}

	// Riwayat hanya memuat satu perpindahan ke ACCEPTED.
	riwayat, err := l.kirim.History(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	var n int
	for _, e := range riwayat {
		if e.ToStatus == delivery.StatusAccepted {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("perpindahan ke ACCEPTED tercatat %d kali, seharusnya 1", n)
	}
}

// TestSync_DiprosesMenurutWaktuPerangkat menjaga SRS-DLV-006. Antrean lokal
// dapat terkirim tidak berurutan ketika beberapa permintaan berjalan
// bersamaan, dan memproses "tiba" sebelum "berangkat" akan ditolak matriks
// padahal driver mengerjakannya dengan benar.
func TestSync_DiprosesMenurutWaktuPerangkat(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	// Sengaja dikirim dalam urutan terbalik.
	hasil, err := l.kirim.Sync(ctx, drv, []delivery.Command{
		perintah("evt-c-"+uuid.NewString(), kirim.ID, delivery.StatusArrived, 1),
		perintah("evt-a-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 3),
		perintah("evt-b-"+uuid.NewString(), kirim.ID, delivery.StatusOnTheWay, 2),
	})
	if err != nil {
		t.Fatalf("sinkronisasi: %v", err)
	}
	if len(hasil) != 3 {
		t.Fatalf("hasil %d, seharusnya 3", len(hasil))
	}
	for i, h := range hasil {
		if h.Outcome != delivery.OutcomeApplied {
			t.Fatalf("perintah ke-%d dijawab %q, seharusnya APPLIED: %s",
				i, h.Outcome, h.Reason)
		}
	}

	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.Status != delivery.StatusArrived {
		t.Fatalf("status akhir %q, seharusnya ARRIVED", lagi.Status)
	}
}

// TestSync_KonflikSaatServerLebihBaru menjaga SRS-DLV-006: bila status di
// server sudah lebih baru, perintah ditolak dan status server menjadi acuan.
func TestSync_KonflikSaatServerLebihBaru(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	// Driver sudah maju sampai tiba lewat jalur daring.
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay,
		delivery.StatusArrived)

	// Perangkat lain, atau antrean lama, mengirim perintah "terima tugas".
	hasil, err := l.kirim.Sync(ctx, drv, []delivery.Command{
		perintah("evt-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 1),
	})
	if err != nil {
		t.Fatalf("sinkronisasi: %v", err)
	}
	if hasil[0].Outcome != delivery.OutcomeConflict {
		t.Fatalf("dijawab %q, seharusnya CONFLICT", hasil[0].Outcome)
	}
	// Status server menjadi acuan, bukan status yang diminta perangkat.
	if hasil[0].ServerStatus != delivery.StatusArrived {
		t.Fatalf("status server %q, seharusnya ARRIVED", hasil[0].ServerStatus)
	}
	if hasil[0].Reason == "" {
		t.Fatal("alasan konflik seharusnya disertakan")
	}

	// Statusnya tidak berubah akibat perintah yang berkonflik.
	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.Status != delivery.StatusArrived {
		t.Fatalf("status %q berubah akibat perintah berkonflik", lagi.Status)
	}
}

// TestSync_KonflikTersimpanUntukPenelusuran menjaga SRS-DLV-006: alasan
// konflik tersimpan agar dapat ditemukan kembali.
func TestSync_KonflikTersimpanUntukPenelusuran(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	sejak := time.Now().Add(-time.Minute)
	if _, err := l.kirim.Sync(ctx, drv, []delivery.Command{
		perintah("evt-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 1),
	}); err != nil {
		t.Fatalf("sinkronisasi: %v", err)
	}

	n, err := l.kirim.PendingSyncCount(ctx, drv, sejak)
	if err != nil {
		t.Fatalf("menghitung konflik: %v", err)
	}
	if n != 1 {
		t.Fatalf("konflik tercatat %d, seharusnya 1", n)
	}

	var alasan string
	err = l.pool.QueryRow(ctx, `
		SELECT conflict_reason FROM sync_events
		WHERE  driver_id = $1 AND outcome = 'CONFLICT'`, drv).Scan(&alasan)
	if err != nil {
		t.Fatalf("membaca alasan konflik: %v", err)
	}
	if alasan == "" {
		t.Fatal("alasan konflik tidak tersimpan")
	}
}

// TestSync_KonflikDikirimUlangTetapKonflik menjaga agar pengiriman ulang
// perintah yang berkonflik tidak tiba tiba berhasil karena statusnya sudah
// berubah lagi.
func TestSync_KonflikDikirimUlangTetapKonflik(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)
	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)

	cmd := perintah("evt-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 1)
	pertama, err := l.kirim.Sync(ctx, drv, []delivery.Command{cmd})
	if err != nil {
		t.Fatalf("kiriman pertama: %v", err)
	}
	if pertama[0].Outcome != delivery.OutcomeConflict {
		t.Fatalf("kiriman pertama %q, seharusnya CONFLICT", pertama[0].Outcome)
	}

	kedua, err := l.kirim.Sync(ctx, drv, []delivery.Command{cmd})
	if err != nil {
		t.Fatalf("kiriman kedua: %v", err)
	}
	if kedua[0].Outcome != delivery.OutcomeConflict {
		t.Fatalf("kiriman kedua %q, seharusnya tetap CONFLICT", kedua[0].Outcome)
	}
}

// TestSync_GalatBukanKonflikDisampaikanApaAdanya menjaga pembedaan yang
// penting: tugas milik driver lain bukan konflik sinkronisasi, dan tidak boleh
// dijawab sebagai konflik yang seolah dapat diselesaikan dengan menyelaraskan
// status.
func TestSync_GalatBukanKonflikDisampaikanApaAdanya(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, _, _, _ := l.tugas(t)
	lain := l.buatDriver(t, kirim.DepotID, true)

	_, err := l.kirim.Sync(ctx, lain, []delivery.Command{
		perintah("evt-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 1),
	})
	if err == nil {
		t.Fatal("perintah atas tugas orang lain seharusnya menghasilkan galat")
	}
	// Dan tidak dicatat sebagai konflik, karena bukan itu masalahnya.
	n, err := l.kirim.PendingSyncCount(ctx, lain, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("menghitung konflik: %v", err)
	}
	if n != 0 {
		t.Fatalf("konflik tercatat %d, seharusnya 0", n)
	}
}

// TestSync_PerintahTanpaPengenalDitolak menjaga dasar idempotensinya. Tanpa
// pengenal perangkat, tidak ada cara mengenali pengiriman ulang.
func TestSync_PerintahTanpaPengenalDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	_, err := l.kirim.Sync(ctx, drv, []delivery.Command{
		{DeliveryID: kirim.ID, Status: delivery.StatusAccepted},
	})
	if err == nil {
		t.Fatal("perintah tanpa pengenal perangkat seharusnya ditolak")
	}
}

// TestSync_PerintahTanpaWaktuPerangkatDiProsesTerakhir menjaga aturan
// pengurutan: tidak ada dasar menempatkannya lebih awal.
func TestSync_PerintahTanpaWaktuPerangkatDiProsesTerakhir(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	kirim, drv, _, _ := l.tugas(t)

	tanpaWaktu := delivery.Command{
		ClientEventID: "evt-nowaktu-" + uuid.NewString(),
		DeliveryID:    kirim.ID,
		Status:        delivery.StatusOnTheWay,
	}
	denganWaktu := perintah("evt-"+uuid.NewString(), kirim.ID, delivery.StatusAccepted, 1)

	hasil, err := l.kirim.Sync(ctx, drv, []delivery.Command{tanpaWaktu, denganWaktu})
	if err != nil {
		t.Fatalf("sinkronisasi: %v", err)
	}
	// Yang berwaktu diproses lebih dahulu, sehingga keduanya berhasil.
	for i, h := range hasil {
		if h.Outcome != delivery.OutcomeApplied {
			t.Fatalf("perintah ke-%d dijawab %q: %s", i, h.Outcome, h.Reason)
		}
	}
	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.Status != delivery.StatusOnTheWay {
		t.Fatalf("status akhir %q, seharusnya ON_THE_WAY", lagi.Status)
	}
}
