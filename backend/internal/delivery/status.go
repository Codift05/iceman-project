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

// ChangeInput adalah permintaan perubahan status pengiriman.
type ChangeInput struct {
	// ActorID adalah pelaku. Untuk perpindahan oleh driver, nilainya wajib dan
	// dibandingkan dengan driver yang ditugaskan.
	ActorID *uuid.UUID
	// DeviceTime adalah waktu menurut jam perangkat driver. Disimpan di samping
	// waktu server, bukan menggantikannya, karena jam perangkat dapat meleset
	// atau diubah (SRS-DLV-003).
	DeviceTime *time.Time
	Reason     string
}

// ChangeStatus memindahkan pengiriman ke status lain.
//
// Perpindahan yang tidak terdaftar pada matriks ditolak (SRS-DLV-003). Efek
// sampingnya dikerjakan dalam transaksi yang sama, termasuk perubahan status
// pesanan yang mengikutinya, sehingga pesanan dan pengirimannya tidak pernah
// berbeda cerita.
func (d *Deliveries) ChangeStatus(ctx context.Context, deliveryID uuid.UUID, to string, in ChangeInput) (*Delivery, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Baris pengiriman dikunci. Tanpa kunci, perintah yang sama dikirim dua
	// kali dari perangkat yang jaringannya tersendat dapat sama sama membaca
	// status lama lalu keduanya merasa perpindahannya sah.
	var (
		dari     string
		driverID uuid.UUID
		orderID  uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, driver_id, order_id
		FROM   deliveries WHERE id = $1 FOR UPDATE`, deliveryID).
		Scan(&dari, &driverID, &orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pengiriman: %w", err)
	}

	t, ok := Allowed(dari, to)
	if !ok {
		return nil, fmt.Errorf("%w: %s ke %s", ErrInvalidTransition, dari, to)
	}

	// Perpindahan oleh driver hanya boleh dilakukan driver yang ditugaskan.
	// Izin delivery.update_own saja tidak cukup, karena seluruh driver
	// memegangnya; yang membedakan adalah tugas siapa ini.
	if t.ByAssignedDriver {
		if in.ActorID == nil || *in.ActorID != driverID {
			return nil, ErrNotAssignedDriver
		}
	}

	alasan := strings.TrimSpace(in.Reason)
	if t.RequiresReason && alasan == "" {
		return nil, ErrReasonRequired
	}

	// Bukti serah terima wajib sudah ada sebelum pengiriman dinyatakan selesai
	// (SRS-DLV-004). Diperiksa di dalam transaksi yang sama agar bukti tidak
	// dapat dihapus di antara pemeriksaan dan perubahan status.
	if t.RequiresProof {
		var adaBukti bool
		err := tx.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM proof_of_delivery WHERE delivery_id = $1)`,
			deliveryID).Scan(&adaBukti)
		if err != nil {
			return nil, fmt.Errorf("memeriksa bukti serah terima: %w", err)
		}
		if !adaBukti {
			return nil, ErrProofRequired
		}
	}

	// Setiap status mencatat waktunya sendiri, bukan hanya status terakhir.
	// Tanpa itu, lama perjalanan dan lama di lokasi tidak dapat dihitung untuk
	// laporan operasional.
	kolomWaktu := map[string]string{
		StatusAccepted:  "accepted_at",
		StatusOnTheWay:  "departed_at",
		StatusArrived:   "arrived_at",
		StatusDelivered: "completed_at",
	}

	sql := `UPDATE deliveries SET status = $2::delivery_status,
	        failure_reason = CASE WHEN $2 = 'FAILED_DELIVERY' THEN $3 ELSE failure_reason END,
	        last_device_time = coalesce($4, last_device_time)`
	if kolom, ada := kolomWaktu[to]; ada {
		sql += fmt.Sprintf(", %s = coalesce(%s, now())", kolom, kolom)
	}
	sql += ` WHERE id = $1`

	if _, err := tx.Exec(ctx, sql, deliveryID, to, alasan, in.DeviceTime); err != nil {
		return nil, fmt.Errorf("mengubah status pengiriman: %w", err)
	}

	if err := catatStatus(ctx, tx, deliveryID, &dari, to, in.ActorID, in.DeviceTime, alasan); err != nil {
		return nil, err
	}

	// Status pesanan mengikuti status pengiriman pada dua titik: saat driver
	// berangkat, dan saat pengiriman selesai atau gagal. Keduanya diurus di
	// sini supaya pesanan dan pengirimannya tidak pernah berbeda cerita.
	if err := ikutkanPesanan(ctx, tx, orderID, to, in.ActorID, alasan); err != nil {
		return nil, err
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "deliveries", EntityID: &deliveryID, Action: audit.ActionUpdate,
		Before: map[string]any{"status": dari},
		After:  map[string]any{"status": to},
		Detail: rinciTransisi(dari, to, alasan),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah status pengiriman: %w", err)
	}
	return d.Get(ctx, deliveryID)
}

func rinciTransisi(dari, ke, alasan string) string {
	s := fmt.Sprintf("status pengiriman %s menjadi %s", dari, ke)
	if alasan != "" {
		s += ", alasan: " + alasan
	}
	return s
}

// ikutkanPesanan menyesuaikan status pesanan dengan status pengiriman.
//
// Hanya tiga perpindahan pengiriman yang berdampak pada pesanan. Sisanya
// adalah kemajuan di dalam perjalanan yang tidak mengubah arti pesanan bagi
// pelanggan.
func ikutkanPesanan(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, statusPengiriman string, pelaku *uuid.UUID, alasan string) error {
	var tujuan, catatan string
	switch statusPengiriman {
	case StatusOnTheWay:
		tujuan, catatan = order.StatusOutForDelivery, "driver berangkat"
	case StatusDelivered:
		tujuan, catatan = order.StatusCompleted, "bukti serah terima diterima"
	case StatusFailed:
		// Gagal kirim mengembalikan pesanan ke jadwal, bukan membatalkannya.
		// Kuota slotnya tidak dilepas karena pengirimannya masih akan diulang.
		tujuan, catatan = order.StatusScheduled, "gagal kirim: "+alasan
	default:
		return nil
	}

	var dari string
	err := tx.QueryRow(ctx,
		`SELECT status::text FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&dari)
	if err != nil {
		return fmt.Errorf("membaca status pesanan: %w", err)
	}
	// Bila perpindahannya tidak sah menurut matriks pesanan, status pesanan
	// dibiarkan. Itu terjadi misalnya saat pengiriman gagal padahal pesanan
	// sudah dibatalkan admin; memaksakannya justru merusak riwayat pesanan.
	if _, ok := order.Allowed(dari, tujuan); !ok {
		return nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders
		SET    status = $2::order_status,
		       completed_at = CASE WHEN $2 = 'COMPLETED' THEN now() ELSE completed_at END
		WHERE  id = $1`, orderID, tujuan); err != nil {
		return fmt.Errorf("menyesuaikan status pesanan: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO order_status_history (order_id, from_status, to_status, actor_id, reason)
		VALUES ($1, $2::order_status, $3::order_status, $4, $5)`,
		orderID, dari, tujuan, pelaku, catatan)
	if err != nil {
		return fmt.Errorf("mencatat riwayat status pesanan: %w", err)
	}
	return nil
}

// ProofInput adalah bukti serah terima yang diunggah driver.
type ProofInput struct {
	// PhotoKey adalah kunci berkas pada object storage. Berkasnya diunggah
	// langsung oleh perangkat memakai URL berbatas waktu, sehingga foto
	// beresolusi penuh tidak melewati server aplikasi (SRS-DLV-004).
	PhotoKey     string
	ReceiverName string
	Notes        string
	DeviceTime   *time.Time
	ActorID      *uuid.UUID
}

// SaveProof menyimpan bukti serah terima.
//
// Hanya driver yang ditugaskan boleh mengunggahnya, dan hanya saat pengiriman
// sudah berstatus tiba. Mengunggah bukti sebelum tiba berarti bukti itu dibuat
// di tempat lain.
func (d *Deliveries) SaveProof(ctx context.Context, deliveryID uuid.UUID, in ProofInput) (*Proof, error) {
	if strings.TrimSpace(in.PhotoKey) == "" {
		return nil, ErrProofRequired
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		status   string
		driverID uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, driver_id FROM deliveries WHERE id = $1 FOR UPDATE`, deliveryID).
		Scan(&status, &driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pengiriman: %w", err)
	}
	if in.ActorID == nil || *in.ActorID != driverID {
		return nil, ErrNotAssignedDriver
	}
	if status != StatusArrived {
		return nil, fmt.Errorf("%w: bukti hanya dapat diunggah setelah tiba, status sekarang %s",
			ErrInvalidTransition, status)
	}

	// ON CONFLICT dipakai agar pengiriman ulang dari perangkat yang jaringannya
	// tersendat memperbarui bukti, bukan gagal pada kekangan keunikan DB-06.
	var p Proof
	err = tx.QueryRow(ctx, `
		INSERT INTO proof_of_delivery
		       (delivery_id, photo_key, receiver_name, notes, device_time)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (delivery_id) DO UPDATE
		SET    photo_key = excluded.photo_key,
		       receiver_name = excluded.receiver_name,
		       notes = excluded.notes,
		       device_time = excluded.device_time
		RETURNING photo_key, receiver_name, notes, device_time, created_at`,
		deliveryID, strings.TrimSpace(in.PhotoKey),
		strings.TrimSpace(in.ReceiverName), in.Notes, in.DeviceTime).
		Scan(&p.PhotoKey, &p.ReceiverName, &p.Notes, &p.DeviceTime, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("menyimpan bukti serah terima: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "proof_of_delivery", EntityID: &deliveryID, Action: audit.ActionCreate,
		After:  map[string]any{"photo_key": p.PhotoKey, "receiver_name": p.ReceiverName},
		Detail: "bukti serah terima diunggah",
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan bukti serah terima: %w", err)
	}
	return &p, nil
}

// IssueInput adalah laporan kendala dari driver.
type IssueInput struct {
	Category   string
	Note       string
	PhotoKey   string
	DeviceTime *time.Time
	ActorID    *uuid.UUID
}

// ReportIssue mencatat kendala pengiriman.
//
// Kendala dicatat terpisah dari perubahan status, karena tidak semua kendala
// menggagalkan pengiriman. Driver yang terlambat karena jalan rusak perlu
// melaporkannya tanpa menandai pengirimannya gagal (SRS-DLV-005).
func (d *Deliveries) ReportIssue(ctx context.Context, deliveryID uuid.UUID, in IssueInput) (*Issue, error) {
	if strings.TrimSpace(in.Category) == "" {
		return nil, ErrCategoryRequired
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var driverID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT driver_id FROM deliveries WHERE id = $1`, deliveryID).
		Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengiriman: %w", err)
	}
	if in.ActorID == nil || *in.ActorID != driverID {
		return nil, ErrNotAssignedDriver
	}

	var x Issue
	err = tx.QueryRow(ctx, `
		INSERT INTO delivery_issues
		       (delivery_id, category, note, photo_key, reported_by, device_time)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, category, note, photo_key, reported_by, device_time, created_at`,
		deliveryID, strings.TrimSpace(in.Category), in.Note, in.PhotoKey,
		in.ActorID, in.DeviceTime).
		Scan(&x.ID, &x.Category, &x.Note, &x.PhotoKey, &x.ReportedBy,
			&x.DeviceTime, &x.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("menyimpan kendala: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "delivery_issues", EntityID: &x.ID, Action: audit.ActionCreate,
		After:  map[string]any{"category": x.Category, "note": x.Note},
		Detail: "kendala dilaporkan: " + x.Category,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan kendala: %w", err)
	}
	return &x, nil
}
