// Package order membuat dan mengelola pesanan.
//
// Pembuatan pesanan adalah bagian dengan aturan terbanyak di sistem ini, dan
// urutan pemeriksaannya mengikuti dua diagram alur checkout pada SRS Bab 4.3.
// Urutannya bukan selera: pemeriksaan yang murah dan tidak memerlukan kunci
// dikerjakan lebih dahulu, sehingga permintaan yang jelas salah tidak pernah
// sampai mengunci baris slot dan menahan pelanggan lain.
package order

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status pesanan (SRS Bab 5.1).
const (
	StatusWaitingPayment = "WAITING_PAYMENT"
	StatusPaid           = "PAID"
	StatusProcessing     = "PROCESSING"
	StatusScheduled      = "SCHEDULED"
	StatusOutForDelivery = "OUT_FOR_DELIVERY"
	StatusCompleted      = "COMPLETED"
	StatusCancelled      = "CANCELLED"
)

// Kanal pesanan.
const (
	ChannelApp      = "APP"
	ChannelWhatsApp = "WHATSAPP"
)

// Galat domain pesanan.
var (
	ErrNotFound          = errors.New("pesanan tidak ditemukan")
	ErrCartEmpty         = errors.New("keranjang kosong")
	ErrItemsUnavailable  = errors.New("ada item yang tidak dapat dipesan")
	ErrMinOrderNotMet    = errors.New("jumlah belum memenuhi minimum order")
	ErrSlotAreaMismatch  = errors.New("slot tidak melayani wilayah alamat ini")
	ErrInvalidTransition = errors.New("perubahan status tidak diizinkan")
	ErrReasonRequired    = errors.New("alasan wajib diisi")
)

// Item adalah satu baris pesanan, dengan nilai yang sudah tetap.
type Item struct {
	ID             uuid.UUID `json:"id"`
	ProductID      uuid.UUID `json:"product_id"`
	SKU            string    `json:"sku"`
	Name           string    `json:"name"`
	Packaging      string    `json:"packaging,omitempty"`
	Qty            int32     `json:"qty"`
	UnitPriceCents int64     `json:"unit_price_cents"`
	LineTotalCents int64     `json:"line_total_cents"`
}

// Order adalah satu pesanan.
type Order struct {
	ID      uuid.UUID `json:"id"`
	OrderNo string    `json:"order_no"`
	Status  string    `json:"status"`
	Channel string    `json:"channel"`

	CustomerID    uuid.UUID `json:"customer_id"`
	DepotID       uuid.UUID `json:"depot_id"`
	ServiceAreaID uuid.UUID `json:"service_area_id"`

	RecipientName  string  `json:"recipient_name"`
	RecipientPhone string  `json:"recipient_phone"`
	AddressLine    string  `json:"address_line"`
	AddressNotes   string  `json:"address_notes,omitempty"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`

	SlotID        uuid.UUID `json:"slot_id"`
	ScheduledDate string    `json:"scheduled_date"`

	SubtotalCents    int64 `json:"subtotal_cents"`
	DeliveryFeeCents int64 `json:"delivery_fee_cents"`
	DiscountCents    int64 `json:"discount_cents"`
	TotalCents       int64 `json:"total_cents"`

	// PaymentTermDays terisi hanya bila pesanan ini dibayar belakangan.
	PaymentTermDays *int32 `json:"payment_term_days,omitempty"`

	CustomerNotes   string `json:"customer_notes,omitempty"`
	CancelledReason string `json:"cancelled_reason,omitempty"`

	Items     []Item    `json:"items"`
	CreatedAt time.Time `json:"created_at"`
}

// StatusEvent adalah satu catatan perubahan status.
type StatusEvent struct {
	FromStatus *string    `json:"from_status,omitempty"`
	ToStatus   string     `json:"to_status"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
}
