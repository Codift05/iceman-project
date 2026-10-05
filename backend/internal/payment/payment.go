// Package payment mengelola tagihan, webhook penyedia, refund, dan
// rekonsiliasi.
//
// Penyedia pembayaran belum diputuskan (OQ pada Decision Log), sehingga paket
// ini dirancang agar seluruh aturannya berlaku tanpa bergantung pada penyedia
// tertentu: status penyedia dipetakan ke status internal, tanda tangan
// diverifikasi lewat antarmuka, dan pembuatan tagihan memanggil antarmuka
// penyedia. Yang menunggu pilihan penyedia hanya satu implementasi antarmuka
// itu, bukan logikanya.
package payment

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status pembayaran internal (SRS-PAY-003, SRS Bab 5.2).
const (
	StatusPending   = "PENDING"
	StatusSuccess   = "SUCCESS"
	StatusExpired   = "EXPIRED"
	StatusFailed    = "FAILED"
	StatusRefunded  = "REFUNDED"
	StatusCancelled = "CANCELLED"
)

// Galat domain pembayaran.
var (
	ErrNotFound         = errors.New("pembayaran tidak ditemukan")
	ErrOrderNotPayable  = errors.New("pesanan tidak dapat dibayar")
	ErrAlreadyPaid      = errors.New("pesanan sudah dibayar")
	ErrProvider         = errors.New("penyedia pembayaran gagal dihubungi")
	ErrSignatureInvalid = errors.New("tanda tangan webhook tidak sah")
	ErrNotRefundable    = errors.New("pembayaran tidak dapat direfund")
	ErrRefundExceeds    = errors.New("nominal refund melebihi sisa yang dapat dikembalikan")
	ErrReasonRequired   = errors.New("alasan wajib diisi")
	ErrAmountMismatch   = errors.New("nominal pada webhook tidak sama dengan tagihan")
	ErrInvalidStatus    = errors.New("perubahan status pembayaran tidak diizinkan")
)

// Payment adalah satu tagihan untuk sebuah pesanan.
type Payment struct {
	ID      uuid.UUID `json:"id"`
	OrderID uuid.UUID `json:"order_id"`
	OrderNo string    `json:"order_no,omitempty"`
	Status  string    `json:"status"`

	AmountCents int64  `json:"amount_cents"`
	Method      string `json:"method"`
	Provider    string `json:"provider,omitempty"`
	ProviderRef string `json:"provider_ref,omitempty"`
	QRPayload   string `json:"qr_payload,omitempty"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`

	ProviderFeeCents int64 `json:"provider_fee_cents"`

	// RefundedCents adalah jumlah yang sudah dikembalikan, dihitung dari
	// baris refund. Tidak disimpan sebagai kolom agar tidak dapat menyimpang
	// dari jumlah barisnya.
	RefundedCents int64 `json:"refunded_cents"`

	CreatedAt time.Time `json:"created_at"`
}

// Refundable menjawab berapa yang masih dapat dikembalikan.
func (p *Payment) Refundable() int64 {
	if p.Status != StatusSuccess && p.Status != StatusRefunded {
		return 0
	}
	sisa := p.AmountCents - p.RefundedCents
	if sisa < 0 {
		return 0
	}
	return sisa
}

// Expired menjawab apakah masa berlaku tagihan sudah lewat.
//
// Tagihan kedaluwarsa tidak dapat dipakai menandai pesanan sebagai dibayar
// (SRS-PAY-001), sehingga pemeriksaannya dipakai juga saat memproses webhook.
func (p *Payment) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
}

// Refund adalah satu pengembalian dana.
type Refund struct {
	ID          uuid.UUID  `json:"id"`
	PaymentID   uuid.UUID  `json:"payment_id"`
	AmountCents int64      `json:"amount_cents"`
	Reason      string     `json:"reason"`
	ProviderRef string     `json:"provider_ref,omitempty"`
	IsManual    bool       `json:"is_manual"`
	ActorID     *uuid.UUID `json:"actor_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Event adalah satu notifikasi dari penyedia yang sudah tersimpan.
type Event struct {
	ID             uuid.UUID  `json:"id"`
	EventID        string     `json:"event_id"`
	PaymentID      *uuid.UUID `json:"payment_id,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	ProviderStatus string     `json:"provider_status"`
	MappedStatus   *string    `json:"mapped_status,omitempty"`
	AmountCents    *int64     `json:"amount_cents,omitempty"`
	NeedsReview    bool       `json:"needs_review"`
	ReviewNote     string     `json:"review_note,omitempty"`
	ProcessedAt    *time.Time `json:"processed_at,omitempty"`
	ReceivedAt     time.Time  `json:"received_at"`
}
