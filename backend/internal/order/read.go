package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const kolomPesanan = `
	o.id, o.order_no, o.status::text, o.channel::text,
	o.customer_id, o.depot_id, o.service_area_id,
	o.recipient_name, o.recipient_phone, o.address_line, o.address_notes,
	o.latitude, o.longitude, o.slot_id, o.scheduled_date,
	o.subtotal_cents, o.delivery_fee_cents, o.discount_cents, o.total_cents,
	o.payment_term_days, o.customer_notes, o.cancelled_reason, o.created_at`

func pindaiPesanan(row pgx.Row) (*Order, error) {
	var (
		x       Order
		tanggal time.Time
	)
	err := row.Scan(&x.ID, &x.OrderNo, &x.Status, &x.Channel,
		&x.CustomerID, &x.DepotID, &x.ServiceAreaID,
		&x.RecipientName, &x.RecipientPhone, &x.AddressLine, &x.AddressNotes,
		&x.Latitude, &x.Longitude, &x.SlotID, &tanggal,
		&x.SubtotalCents, &x.DeliveryFeeCents, &x.DiscountCents, &x.TotalCents,
		&x.PaymentTermDays, &x.CustomerNotes, &x.CancelledReason, &x.CreatedAt)
	if err != nil {
		return nil, err
	}
	x.ScheduledDate = tanggal.Format("2006-01-02")
	return &x, nil
}

// Get membaca satu pesanan beserta isinya, untuk tampilan admin.
func (o *Orders) Get(ctx context.Context, id uuid.UUID) (*Order, error) {
	x, err := pindaiPesanan(o.d.Pool.QueryRow(ctx,
		`SELECT `+kolomPesanan+` FROM orders o WHERE o.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pesanan: %w", err)
	}
	if err := o.muatItem(ctx, x); err != nil {
		return nil, err
	}
	return x, nil
}

// GetForCustomer membaca pesanan milik pelanggan tertentu.
//
// Kepemilikan menjadi bagian dari query, sama seperti pada alamat. Riwayat
// hanya menampilkan pesanan milik pelanggan yang sedang masuk (SRS-ORD-005),
// dan pesanan orang lain menghasilkan ErrNotFound tanpa membedakannya dari
// pesanan yang memang tidak ada.
func (o *Orders) GetForCustomer(ctx context.Context, customerID, id uuid.UUID) (*Order, error) {
	x, err := pindaiPesanan(o.d.Pool.QueryRow(ctx,
		`SELECT `+kolomPesanan+` FROM orders o
		 WHERE  o.id = $1 AND o.customer_id = $2`, id, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pesanan: %w", err)
	}
	if err := o.muatItem(ctx, x); err != nil {
		return nil, err
	}
	return x, nil
}

func (o *Orders) muatItem(ctx context.Context, x *Order) error {
	rows, err := o.d.Pool.Query(ctx, `
		SELECT id, product_id, sku, name, packaging, qty,
		       unit_price_cents, line_total_cents
		FROM   order_items WHERE order_id = $1 ORDER BY name`, x.ID)
	if err != nil {
		return fmt.Errorf("membaca item pesanan: %w", err)
	}
	defer rows.Close()

	x.Items = []Item{}
	for rows.Next() {
		var it Item
		err := rows.Scan(&it.ID, &it.ProductID, &it.SKU, &it.Name, &it.Packaging,
			&it.Qty, &it.UnitPriceCents, &it.LineTotalCents)
		if err != nil {
			return fmt.Errorf("membaca baris item: %w", err)
		}
		x.Items = append(x.Items, it)
	}
	return rows.Err()
}

// ListFilter menyaring daftar pesanan.
type ListFilter struct {
	CustomerID *uuid.UUID
	DepotID    *uuid.UUID
	Status     string
	// ScheduledDate menyaring menurut tanggal pengiriman, berbentuk YYYY-MM-DD.
	ScheduledDate string
	Limit         int
}

// List mengembalikan daftar pesanan tanpa isinya.
//
// Isi pesanan tidak diikutkan karena daftar pada dashboard tidak
// menampilkannya, dan memuat seluruh item untuk ratusan pesanan membuat satu
// permintaan daftar menjadi mahal tanpa ada yang membacanya.
func (o *Orders) List(ctx context.Context, f ListFilter) ([]Order, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := o.d.Pool.Query(ctx, `
		SELECT `+kolomPesanan+`
		FROM   orders o
		WHERE  ($1::uuid IS NULL OR o.customer_id = $1)
		  AND  ($2::uuid IS NULL OR o.depot_id = $2)
		  AND  ($3 = '' OR o.status::text = $3)
		  AND  ($4 = '' OR o.scheduled_date = $4::date)
		ORDER  BY o.created_at DESC
		LIMIT  $5`,
		f.CustomerID, f.DepotID, f.Status, f.ScheduledDate, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar pesanan: %w", err)
	}
	defer rows.Close()

	out := []Order{}
	for rows.Next() {
		x, err := pindaiPesanan(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris pesanan: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// History mengembalikan riwayat perubahan status sebuah pesanan.
func (o *Orders) History(ctx context.Context, orderID uuid.UUID) ([]StatusEvent, error) {
	rows, err := o.d.Pool.Query(ctx, `
		SELECT from_status::text, to_status::text, actor_id, reason, occurred_at
		FROM   order_status_history
		WHERE  order_id = $1
		ORDER  BY occurred_at`, orderID)
	if err != nil {
		return nil, fmt.Errorf("membaca riwayat status: %w", err)
	}
	defer rows.Close()

	out := []StatusEvent{}
	for rows.Next() {
		var e StatusEvent
		if err := rows.Scan(&e.FromStatus, &e.ToStatus, &e.ActorID, &e.Reason, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("membaca baris riwayat: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
