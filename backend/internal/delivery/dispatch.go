package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/order"
)

// AssignInput adalah permintaan penugasan driver.
type AssignInput struct {
	OrderID  uuid.UUID
	DriverID uuid.UUID
	// SequenceNo mengatur urutan pengiriman dalam satu slot. Nol berarti belum
	// diurutkan, dan admin dapat mengubahnya kemudian.
	SequenceNo int32
	Reason     string
}

// Assign menugaskan driver pada sebuah pesanan.
//
// Penugasan hanya untuk pesanan yang sudah berstatus PROCESSING atau setelahnya
// (SRS-DLV-001). Pesanan yang masih menunggu pembayaran belum tentu jadi, dan
// menugaskannya berarti driver menyiapkan muatan untuk pesanan yang dapat
// hangus.
//
// Bila pesanan sudah punya penugasan, yang terjadi adalah penugasan ulang:
// baris pengiriman berpindah driver dan riwayatnya dicatat. Driver lama
// kehilangan akses ke pengiriman itu, karena akses dibaca dari baris yang sama
// dengan yang baru saja berubah.
func (d *Deliveries) Assign(ctx context.Context, in AssignInput) (*Delivery, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Pesanan dikunci agar penugasan tidak berlomba dengan perubahan status
	// pesanan. Tanpa kunci, pesanan dapat dibatalkan tepat setelah
	// statusnya diperiksa di sini.
	var (
		statusPesanan string
		depoPesanan   uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, depot_id FROM orders WHERE id = $1 FOR UPDATE`, in.OrderID).
		Scan(&statusPesanan, &depoPesanan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, order.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pesanan: %w", err)
	}
	if !siapDitugaskan(statusPesanan) {
		return nil, fmt.Errorf("%w: status pesanan %s", ErrOrderNotReady, statusPesanan)
	}

	// Driver wajib aktif dan berasal dari depo pesanan. Pemeriksaan di sini
	// menghasilkan galat yang dapat dibaca pengguna; pemicu DB-10 pada basis
	// data tetap ada sebagai jaring pengaman terakhir bila ada jalur kode lain.
	var (
		statusDriver string
		depoDriver   *uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT u.status, u.depot_id
		FROM   users u JOIN roles r ON r.id = u.role_id
		WHERE  u.id = $1 AND r.code = 'DRIVER'`, in.DriverID).
		Scan(&statusDriver, &depoDriver)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: bukan driver", ErrDriverInactive)
	}
	if err != nil {
		return nil, fmt.Errorf("membaca driver: %w", err)
	}
	if statusDriver != "ACTIVE" {
		return nil, ErrDriverInactive
	}
	if depoDriver == nil || *depoDriver != depoPesanan {
		return nil, ErrDriverOtherDepot
	}

	var (
		x          Delivery
		driverLama *uuid.UUID
	)
	err = tx.QueryRow(ctx,
		`SELECT id, driver_id FROM deliveries WHERE order_id = $1 FOR UPDATE`, in.OrderID).
		Scan(&x.ID, &driverLama)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Penugasan pertama.
		err = tx.QueryRow(ctx, `
			INSERT INTO deliveries (order_id, driver_id, depot_id, sequence_no)
			VALUES ($1, $2, $3, $4)
			RETURNING id`, in.OrderID, in.DriverID, depoPesanan, in.SequenceNo).Scan(&x.ID)
		if err != nil {
			return nil, fmt.Errorf("menyimpan penugasan: %w", err)
		}
		driverLama = nil

	case err != nil:
		return nil, fmt.Errorf("mengunci penugasan: %w", err)

	default:
		// Penugasan ulang. Status kembali ke ASSIGNED karena driver baru belum
		// menerima tugasnya, dan kemajuan driver lama tidak berlaku bagi
		// penggantinya.
		_, err = tx.Exec(ctx, `
			UPDATE deliveries
			SET    driver_id = $2, sequence_no = $3, status = 'ASSIGNED',
			       accepted_at = NULL, departed_at = NULL, arrived_at = NULL
			WHERE  id = $1`, x.ID, in.DriverID, in.SequenceNo)
		if err != nil {
			return nil, fmt.Errorf("mengubah penugasan: %w", err)
		}
	}

	pelaku := audit.ActorFrom(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO delivery_assignments
		       (delivery_id, from_driver, to_driver, actor_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		x.ID, driverLama, in.DriverID, pelaku, strings.TrimSpace(in.Reason))
	if err != nil {
		return nil, fmt.Errorf("mencatat riwayat penugasan: %w", err)
	}

	if err := catatStatus(ctx, tx, x.ID, nil, StatusAssigned, pelaku, nil,
		alasanPenugasan(driverLama, in.Reason)); err != nil {
		return nil, err
	}

	// Pesanan berpindah ke SCHEDULED begitu driver ditetapkan. Inilah efek
	// samping "buat baris pengiriman" pada matriks transisi pesanan, dan
	// keduanya memang harus terjadi bersama: pesanan terjadwal tanpa baris
	// pengiriman berarti tidak ada yang mengantarnya.
	if statusPesanan == order.StatusProcessing {
		if err := naikkanPesananKeScheduled(ctx, tx, in.OrderID, pelaku); err != nil {
			return nil, err
		}
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "deliveries", EntityID: &x.ID, Action: audit.ActionUpdate,
		After:  map[string]any{"driver_id": in.DriverID, "status": StatusAssigned},
		Detail: alasanPenugasan(driverLama, in.Reason),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan penugasan: %w", err)
	}
	return d.Get(ctx, x.ID)
}

// siapDitugaskan menjawab apakah pesanan sudah boleh ditugaskan.
func siapDitugaskan(status string) bool {
	switch status {
	case order.StatusProcessing, order.StatusScheduled, order.StatusOutForDelivery:
		return true
	}
	return false
}

func alasanPenugasan(driverLama *uuid.UUID, alasan string) string {
	kata := "driver ditugaskan"
	if driverLama != nil {
		kata = "driver ditugaskan ulang"
	}
	if alasan = strings.TrimSpace(alasan); alasan != "" {
		return kata + ", alasan: " + alasan
	}
	return kata
}

// naikkanPesananKeScheduled memindahkan pesanan ke SCHEDULED dalam transaksi
// penugasan.
//
// Dikerjakan dengan SQL langsung, bukan memanggil order.ChangeStatus, karena
// fungsi itu membuka transaksinya sendiri. Memanggilnya dari sini berarti
// penugasan dan perubahan status pesanan berada di dua transaksi yang dapat
// berhasil sebagian.
func naikkanPesananKeScheduled(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, pelaku *uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = 'SCHEDULED' WHERE id = $1`, orderID); err != nil {
		return fmt.Errorf("menaikkan status pesanan: %w", err)
	}
	dari := order.StatusProcessing
	_, err := tx.Exec(ctx, `
		INSERT INTO order_status_history (order_id, from_status, to_status, actor_id, reason)
		VALUES ($1, $2::order_status, $3::order_status, $4, $5)`,
		orderID, dari, order.StatusScheduled, pelaku, "driver ditetapkan")
	if err != nil {
		return fmt.Errorf("mencatat riwayat status pesanan: %w", err)
	}
	return nil
}

// SetSequence mengubah urutan pengiriman. Urutan bersifat angka dan dapat
// diubah admin (SRS-DLV-001), misalnya ketika satu alamat perlu didahulukan.
func (d *Deliveries) SetSequence(ctx context.Context, deliveryID uuid.UUID, urutan int32) error {
	if urutan < 0 {
		return fmt.Errorf("urutan tidak boleh negatif")
	}
	tag, err := d.pool.Exec(ctx,
		`UPDATE deliveries SET sequence_no = $2 WHERE id = $1`, deliveryID, urutan)
	if err != nil {
		return fmt.Errorf("mengubah urutan pengiriman: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// catatStatus menulis satu baris riwayat status pengiriman.
func catatStatus(ctx context.Context, tx pgx.Tx, deliveryID uuid.UUID, dari *string, ke string, pelaku *uuid.UUID, waktuPerangkat *time.Time, alasan string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO delivery_status_history
		       (delivery_id, from_status, to_status, actor_id, device_time, reason)
		VALUES ($1, $2::delivery_status, $3::delivery_status, $4, $5, $6)`,
		deliveryID, dari, ke, pelaku, waktuPerangkat, alasan)
	if err != nil {
		return fmt.Errorf("mencatat riwayat status pengiriman: %w", err)
	}
	return nil
}
