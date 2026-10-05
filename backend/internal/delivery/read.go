package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Alamat tujuan dibaca dari pesanan, bukan disalin ulang ke pengiriman.
// Pesanan sudah menyimpan salinannya sendiri saat dibuat, dan menyalinnya dua
// kali membuat dua sumber kebenaran untuk satu alamat.
const kolomPengiriman = `
	d.id, d.order_id, o.order_no, d.driver_id, d.depot_id, d.status::text,
	d.sequence_no,
	o.recipient_name, o.recipient_phone, o.address_line, o.address_notes,
	o.latitude, o.longitude,
	d.last_latitude, d.last_longitude, d.last_position_at, d.eta_at,
	d.accepted_at, d.departed_at, d.arrived_at, d.completed_at,
	d.failure_reason, d.created_at`

func pindaiPengiriman(row pgx.Row) (*Delivery, error) {
	var x Delivery
	err := row.Scan(&x.ID, &x.OrderID, &x.OrderNo, &x.DriverID, &x.DepotID, &x.Status,
		&x.SequenceNo,
		&x.RecipientName, &x.RecipientPhone, &x.AddressLine, &x.AddressNotes,
		&x.Latitude, &x.Longitude,
		&x.LastLatitude, &x.LastLongitude, &x.LastPositionAt, &x.ETAAt,
		&x.AcceptedAt, &x.DepartedAt, &x.ArrivedAt, &x.CompletedAt,
		&x.FailureReason, &x.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// Get membaca satu pengiriman, untuk tampilan admin.
func (d *Deliveries) Get(ctx context.Context, id uuid.UUID) (*Delivery, error) {
	x, err := pindaiPengiriman(d.pool.QueryRow(ctx, `
		SELECT `+kolomPengiriman+`
		FROM   deliveries d JOIN orders o ON o.id = d.order_id
		WHERE  d.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengiriman: %w", err)
	}
	return x, nil
}

// GetForDriver membaca pengiriman milik driver tertentu.
//
// Kepemilikan menjadi bagian dari query, sama seperti pada alamat pelanggan dan
// pesanan. Membuka pengiriman milik driver lain menghasilkan ErrNotFound, bukan
// galat yang membedakannya dari pengiriman yang tidak ada, karena perbedaan itu
// sendiri memberi tahu bahwa pengiriman tersebut memang ada (SRS-DLV-002).
func (d *Deliveries) GetForDriver(ctx context.Context, driverID, id uuid.UUID) (*Delivery, error) {
	x, err := pindaiPengiriman(d.pool.QueryRow(ctx, `
		SELECT `+kolomPengiriman+`
		FROM   deliveries d JOIN orders o ON o.id = d.order_id
		WHERE  d.id = $1 AND d.driver_id = $2`, id, driverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengiriman: %w", err)
	}
	return x, nil
}

// ByOrder membaca pengiriman sebuah pesanan.
func (d *Deliveries) ByOrder(ctx context.Context, orderID uuid.UUID) (*Delivery, error) {
	x, err := pindaiPengiriman(d.pool.QueryRow(ctx, `
		SELECT `+kolomPengiriman+`
		FROM   deliveries d JOIN orders o ON o.id = d.order_id
		WHERE  d.order_id = $1`, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengiriman: %w", err)
	}
	return x, nil
}

// ListFilter menyaring daftar pengiriman.
type ListFilter struct {
	DriverID *uuid.UUID
	DepotID  *uuid.UUID
	Status   string
	// Date menyaring menurut tanggal pengiriman pesanan, berbentuk YYYY-MM-DD.
	Date string
}

// List mengembalikan daftar pengiriman.
//
// Diurutkan mengikuti urutan pengiriman yang ditetapkan admin, lalu waktu
// pembuatan sebagai pemutus (SRS-DLV-002). Urutan itu yang dilihat driver
// sebagai rencana rutenya, jadi daftar yang tidak terurut membuatnya
// menentukan sendiri dan rutenya menjadi tidak efisien.
func (d *Deliveries) List(ctx context.Context, f ListFilter) ([]Delivery, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT `+kolomPengiriman+`
		FROM   deliveries d JOIN orders o ON o.id = d.order_id
		WHERE  ($1::uuid IS NULL OR d.driver_id = $1)
		  AND  ($2::uuid IS NULL OR d.depot_id = $2)
		  AND  ($3 = '' OR d.status::text = $3)
		  AND  ($4 = '' OR o.scheduled_date = $4::date)
		ORDER  BY d.sequence_no, d.created_at`,
		f.DriverID, f.DepotID, f.Status, f.Date)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar pengiriman: %w", err)
	}
	defer rows.Close()

	out := []Delivery{}
	for rows.Next() {
		x, err := pindaiPengiriman(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris pengiriman: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// History mengembalikan riwayat perubahan status pengiriman.
func (d *Deliveries) History(ctx context.Context, deliveryID uuid.UUID) ([]StatusEvent, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT from_status::text, to_status::text, actor_id, device_time, reason, occurred_at
		FROM   delivery_status_history
		WHERE  delivery_id = $1
		ORDER  BY occurred_at`, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("membaca riwayat status: %w", err)
	}
	defer rows.Close()

	out := []StatusEvent{}
	for rows.Next() {
		var e StatusEvent
		err := rows.Scan(&e.FromStatus, &e.ToStatus, &e.ActorID, &e.DeviceTime,
			&e.Reason, &e.OccurredAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris riwayat: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Assignments mengembalikan riwayat penugasan sebuah pengiriman.
func (d *Deliveries) Assignments(ctx context.Context, deliveryID uuid.UUID) ([]Assignment, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT from_driver, to_driver, actor_id, reason, occurred_at
		FROM   delivery_assignments
		WHERE  delivery_id = $1
		ORDER  BY occurred_at`, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("membaca riwayat penugasan: %w", err)
	}
	defer rows.Close()

	out := []Assignment{}
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.FromDriver, &a.ToDriver, &a.ActorID, &a.Reason, &a.OccurredAt); err != nil {
			return nil, fmt.Errorf("membaca baris penugasan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Proof mengembalikan bukti serah terima, atau nil bila belum ada.
func (d *Deliveries) Proof(ctx context.Context, deliveryID uuid.UUID) (*Proof, error) {
	var p Proof
	err := d.pool.QueryRow(ctx, `
		SELECT photo_key, receiver_name, notes, device_time, created_at
		FROM   proof_of_delivery WHERE delivery_id = $1`, deliveryID).
		Scan(&p.PhotoKey, &p.ReceiverName, &p.Notes, &p.DeviceTime, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("membaca bukti serah terima: %w", err)
	}
	return &p, nil
}

// Issues mengembalikan kendala yang dilaporkan pada sebuah pengiriman.
func (d *Deliveries) Issues(ctx context.Context, deliveryID uuid.UUID) ([]Issue, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, category, note, photo_key, reported_by, device_time, created_at
		FROM   delivery_issues WHERE delivery_id = $1 ORDER BY created_at`, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("membaca kendala: %w", err)
	}
	defer rows.Close()

	out := []Issue{}
	for rows.Next() {
		var i Issue
		err := rows.Scan(&i.ID, &i.Category, &i.Note, &i.PhotoKey, &i.ReportedBy,
			&i.DeviceTime, &i.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris kendala: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// PendingSyncCount menghitung konflik sinkronisasi yang belum ditinjau untuk
// seorang driver.
//
// Ditampilkan pada daftar tugas agar driver tahu ada perintahnya yang ditolak
// server, bukan mengira semuanya sudah tersimpan (SRS-DLV-002).
func (d *Deliveries) PendingSyncCount(ctx context.Context, driverID uuid.UUID, sejak time.Time) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM sync_events
		WHERE  driver_id = $1 AND outcome = 'CONFLICT' AND processed_at >= $2`,
		driverID, sejak).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("menghitung konflik sinkronisasi: %w", err)
	}
	return n, nil
}
