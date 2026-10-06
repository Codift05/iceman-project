package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Enqueuer mengantre pengiriman satu notifikasi di dalam transaksi pemanggil.
//
// Dinyatakan sebagai antarmuka agar paket ini tidak mengimpor paket pekerja.
// Penyambungnya dipasang di titik masuk aplikasi, pola yang sama dengan
// penyambung pembayaran.
type Enqueuer interface {
	EnqueueSendTx(ctx context.Context, tx pgx.Tx, notificationID uuid.UUID) error
}

// Target adalah penerima notifikasi beserta rujukan pesanannya.
type Target struct {
	OrderID    uuid.UUID
	CustomerID uuid.UUID
	Recipient  string
	// OrderNo dipakai menyusun isi pesan, supaya pelanggan tahu pesanan mana
	// yang dimaksud tanpa membuka aplikasi.
	OrderNo string
}

// TargetForOrder membaca penerima notifikasi sebuah pesanan.
//
// Nomor telepon pelanggan dipakai sebagai penerima, karena itulah identitas
// yang dimiliki semua pelanggan Iceman. Ketika kanal sungguhan dipilih,
// penerimanya mungkin berubah menjadi token perangkat, dan yang berubah hanya
// fungsi ini.
func TargetForOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*Target, error) {
	var t Target
	t.OrderID = orderID
	err := tx.QueryRow(ctx, `
		SELECT o.order_no, o.customer_id, c.phone
		FROM   orders o JOIN customers c ON c.id = o.customer_id
		WHERE  o.id = $1`, orderID).
		Scan(&t.OrderNo, &t.CustomerID, &t.Recipient)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("pesanan tidak ditemukan saat menyiapkan notifikasi")
	}
	if err != nil {
		return nil, fmt.Errorf("membaca penerima notifikasi: %w", err)
	}
	return &t, nil
}

// EmitTx mencatat dan mengantre notifikasi di dalam transaksi pemanggil.
//
// Inilah satu satunya pintu yang dipakai domain lain. Mencatat dan mengantre
// disatukan karena keduanya harus ikut batal bersama transaksinya: catatan
// tanpa job berarti notifikasi tidak pernah terkirim, dan job tanpa catatan
// berarti pekerja mencari baris yang tidak ada.
//
// Mengembalikan galat hanya untuk kegagalan basis data. Event yang dimatikan
// atau tanpa penerima bukan galat; keduanya dicatat sebagai dilewati.
func (s *Service) EmitTx(ctx context.Context, tx pgx.Tx, in Request) error {
	ids, err := s.QueueTx(ctx, tx, in)
	if err != nil {
		return err
	}
	if s.d.Enqueuer == nil {
		// Tanpa penyambung antrean, barisnya tetap tercatat berstatus
		// menunggu dan akan diambil penyapu berkala. Itu keadaan pengembangan,
		// bukan kehilangan notifikasi.
		return nil
	}
	for _, id := range ids {
		if err := s.d.Enqueuer.EnqueueSendTx(ctx, tx, id); err != nil {
			return fmt.Errorf("mengantre notifikasi: %w", err)
		}
	}
	return nil
}

// EmitForOrderTx menyusun dan mengantre notifikasi sebuah pesanan.
//
// Pembungkus ini ada supaya domain pesanan, pengiriman, dan pembayaran tidak
// masing masing menyusun judul dan isi pesannya sendiri. Kalimat yang berbeda
// untuk kejadian yang sama membuat pelanggan mengira ada dua hal berbeda.
func (s *Service) EmitForOrderTx(ctx context.Context, tx pgx.Tx, event string, orderID uuid.UUID, tambahan string) error {
	t, err := TargetForOrder(ctx, tx, orderID)
	if err != nil {
		return err
	}
	judul, isi := Compose(event, t.OrderNo, tambahan)
	return s.EmitTx(ctx, tx, Request{
		Event:      event,
		Recipient:  t.Recipient,
		Title:      judul,
		Body:       isi,
		OrderID:    &t.OrderID,
		CustomerID: &t.CustomerID,
	})
}

// Compose menyusun judul dan isi pesan untuk sebuah event.
//
// Disimpan di satu tempat agar kalimatnya seragam, dan agar penggantiannya
// nanti, misalnya ketika Iceman ingin nada bahasa yang berbeda, cukup di satu
// berkas. Belum memakai templat berkas karena pesannya masih sedikit dan
// seluruhnya satu kalimat; menambahkan mesin templat sekarang hanya menambah
// lapisan tanpa manfaat.
func Compose(event, orderNo, tambahan string) (judul, isi string) {
	switch event {
	case EventOrderPaid:
		judul = "Pembayaran diterima"
		isi = fmt.Sprintf("Pembayaran pesanan %s sudah kami terima. "+
			"Pesanan Anda segera diproses.", orderNo)
	case EventOrderOutForDelivery:
		judul = "Pesanan sedang diantar"
		isi = fmt.Sprintf("Pesanan %s sedang diantar menuju alamat Anda.", orderNo)
	case EventOrderCompleted:
		judul = "Pesanan selesai"
		isi = fmt.Sprintf("Pesanan %s sudah diterima. Terima kasih.", orderNo)
	case EventOrderCancelled:
		judul = "Pesanan dibatalkan"
		isi = fmt.Sprintf("Pesanan %s dibatalkan.", orderNo)
	case EventDeliveryFailed:
		judul = "Pengiriman belum berhasil"
		isi = fmt.Sprintf("Pengiriman pesanan %s belum berhasil dan akan dijadwalkan ulang.",
			orderNo)
	case EventReorderReminder:
		judul = "Waktunya pesan lagi"
		isi = "Stok es Anda mungkin sudah menipis. Pesan ulang dengan sekali ketuk."
	default:
		judul = "Pemberitahuan"
		isi = fmt.Sprintf("Ada pembaruan pada pesanan %s.", orderNo)
	}
	// Alasan dari pemanggil, misalnya sebab pembatalan atau sebab gagal
	// kirim, ditambahkan di belakang. Pelanggan yang diberi tahu pesanannya
	// batal tanpa sebab akan menelepon untuk menanyakannya.
	if tambahan != "" {
		isi += " " + tambahan
	}
	return judul, isi
}
