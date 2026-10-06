package delivery_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/delivery"
	"github.com/iceman/backend/internal/notify"
)

// notifikasiPesanan membaca notifikasi yang tercatat untuk sebuah pesanan.
func (l *lingkungan) notifikasiPesanan(t *testing.T, orderID uuid.UUID) []string {
	t.Helper()
	rows, err := l.pool.Query(context.Background(), `
		SELECT event FROM notifications WHERE order_id = $1 ORDER BY created_at`, orderID)
	if err != nil {
		t.Fatalf("membaca notifikasi: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatalf("membaca baris notifikasi: %v", err)
		}
		out = append(out, e)
	}
	return out
}

// TestNotifikasi_DiantreSaatBerangkatDanSelesai menutup janji yang menggantung
// sejak domain pengiriman dikerjakan: tabel transisi SRS Bab 5.1 menyebut
// "Antre notifikasi" sebagai efek samping wajib pada perpindahan menuju
// OUT_FOR_DELIVERY dan COMPLETED, dan sampai sekarang tidak ada yang
// mengantrenya.
func TestNotifikasi_DiantreSaatBerangkatDanSelesai(t *testing.T) {
	l := siapkanDenganNotifikasi(t)
	ctx := context.Background()
	kirim, drv, _, o := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted)
	// Menerima tugas adalah kemajuan internal, tidak memberitahukan apa pun.
	if ev := l.notifikasiPesanan(t, o.ID); len(ev) != 0 {
		t.Fatalf("menerima tugas seharusnya tidak memicu notifikasi, dapat %v", ev)
	}

	l.majukan(t, kirim.ID, drv, delivery.StatusOnTheWay)
	ev := l.notifikasiPesanan(t, o.ID)
	if len(ev) != 1 || ev[0] != notify.EventOrderOutForDelivery {
		t.Fatalf("notifikasi berangkat salah: %v", ev)
	}

	l.majukan(t, kirim.ID, drv, delivery.StatusArrived)
	if ev := l.notifikasiPesanan(t, o.ID); len(ev) != 1 {
		t.Fatalf("tiba di lokasi seharusnya tidak memicu notifikasi, dapat %v", ev)
	}

	if _, err := l.kirim.SaveProof(ctx, kirim.ID, delivery.ProofInput{
		PhotoKey: "pod/x.jpg", ActorID: &drv,
	}); err != nil {
		t.Fatalf("menyimpan bukti: %v", err)
	}
	l.majukan(t, kirim.ID, drv, delivery.StatusDelivered)

	ev = l.notifikasiPesanan(t, o.ID)
	if len(ev) != 2 || ev[1] != notify.EventOrderCompleted {
		t.Fatalf("notifikasi selesai salah: %v", ev)
	}
}

// TestNotifikasi_GagalKirimMemberitahuPelanggan menjaga agar pelanggan tidak
// menunggu sia sia ketika pengirimannya gagal.
func TestNotifikasi_GagalKirimMemberitahuPelanggan(t *testing.T) {
	l := siapkanDenganNotifikasi(t)
	ctx := context.Background()
	kirim, drv, _, o := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)
	if _, err := l.kirim.ChangeStatus(ctx, kirim.ID, delivery.StatusFailed,
		delivery.ChangeInput{ActorID: &drv, Reason: "penerima tidak ada di tempat"}); err != nil {
		t.Fatalf("mencatat gagal kirim: %v", err)
	}

	ev := l.notifikasiPesanan(t, o.ID)
	if len(ev) != 2 || ev[1] != notify.EventDeliveryFailed {
		t.Fatalf("notifikasi gagal kirim salah: %v", ev)
	}

	// Alasannya diteruskan kepada pelanggan: tanpa itu ia akan menelepon untuk
	// menanyakannya.
	var isi string
	err := l.pool.QueryRow(ctx, `
		SELECT body FROM notifications
		WHERE  order_id = $1 AND event = $2`, o.ID, notify.EventDeliveryFailed).Scan(&isi)
	if err != nil {
		t.Fatalf("membaca isi notifikasi: %v", err)
	}
	if !strings.Contains(isi, "penerima tidak ada di tempat") {
		t.Fatalf("alasan gagal kirim tidak diteruskan: %q", isi)
	}
}

// TestNotifikasi_TanpaLayananTetapBerhasil menjaga BR-010: kegagalan layanan
// yang tidak kritis tidak boleh menggagalkan transaksi inti. Pengiriman tetap
// berjalan walau notifikasi tidak disetel sama sekali.
func TestNotifikasi_TanpaLayananTetapBerhasil(t *testing.T) {
	l := siapkan(t) // tanpa notifier
	ctx := context.Background()
	kirim, drv, _, o := l.tugas(t)

	l.majukan(t, kirim.ID, drv, delivery.StatusAccepted, delivery.StatusOnTheWay)
	lagi, err := l.kirim.Get(ctx, kirim.ID)
	if err != nil {
		t.Fatalf("membaca pengiriman: %v", err)
	}
	if lagi.Status != delivery.StatusOnTheWay {
		t.Fatalf("status %q, perubahan seharusnya tetap berhasil", lagi.Status)
	}
	if ev := l.notifikasiPesanan(t, o.ID); len(ev) != 0 {
		t.Fatalf("tanpa layanan notifikasi seharusnya tidak ada catatan, dapat %v", ev)
	}
}
