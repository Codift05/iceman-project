package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Jumlah yang sudah direfund dihitung dari baris refund, bukan dibaca dari
// kolom, agar tidak dapat menyimpang dari jumlah barisnya.
const kolom = `
	p.id, p.order_id, o.order_no, p.status::text, p.amount_cents,
	p.method, p.provider, coalesce(p.provider_ref, ''), p.qr_payload,
	p.expires_at, p.paid_at, p.provider_fee_cents,
	coalesce((SELECT sum(r.amount_cents) FROM refunds r WHERE r.payment_id = p.id), 0),
	p.created_at`

func pindai(row pgx.Row) (*Payment, error) {
	var x Payment
	err := row.Scan(&x.ID, &x.OrderID, &x.OrderNo, &x.Status, &x.AmountCents,
		&x.Method, &x.Provider, &x.ProviderRef, &x.QRPayload,
		&x.ExpiresAt, &x.PaidAt, &x.ProviderFeeCents, &x.RefundedCents, &x.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// Get membaca satu pembayaran.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Payment, error) {
	x, err := pindai(s.d.Pool.QueryRow(ctx, `
		SELECT `+kolom+`
		FROM   payments p JOIN orders o ON o.id = p.order_id
		WHERE  p.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pembayaran: %w", err)
	}
	return x, nil
}

// ByProviderRef membaca pembayaran menurut referensi penyedianya.
func (s *Service) ByProviderRef(ctx context.Context, ref string) (*Payment, error) {
	x, err := pindai(s.d.Pool.QueryRow(ctx, `
		SELECT `+kolom+`
		FROM   payments p JOIN orders o ON o.id = p.order_id
		WHERE  p.provider_ref = $1`, ref))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pembayaran: %w", err)
	}
	return x, nil
}

// ListForOrder mengembalikan seluruh pembayaran sebuah pesanan, terbaru dahulu.
//
// Satu pesanan dapat punya beberapa pembayaran berurutan ketika tagihan
// kedaluwarsa diganti atau pelanggan mencoba lagi, dan riwayatnya perlu
// terlihat utuh pada tampilan admin.
func (s *Service) ListForOrder(ctx context.Context, orderID uuid.UUID) ([]Payment, error) {
	rows, err := s.d.Pool.Query(ctx, `
		SELECT `+kolom+`
		FROM   payments p JOIN orders o ON o.id = p.order_id
		WHERE  p.order_id = $1
		ORDER  BY p.created_at DESC`, orderID)
	if err != nil {
		return nil, fmt.Errorf("membaca pembayaran pesanan: %w", err)
	}
	defer rows.Close()

	out := []Payment{}
	for rows.Next() {
		x, err := pindai(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris pembayaran: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// ListFilter menyaring daftar pembayaran.
type ListFilter struct {
	Status string
	// From dan Until menyaring menurut tanggal pembuatan, berbentuk YYYY-MM-DD.
	From  string
	Until string
	Limit int
}

// List mengembalikan daftar pembayaran untuk tampilan keuangan.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Payment, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT `+kolom+`
		FROM   payments p JOIN orders o ON o.id = p.order_id
		WHERE  ($1 = '' OR p.status::text = $1)
		  AND  ($2 = '' OR p.created_at >= $2::date)
		  AND  ($3 = '' OR p.created_at < ($3::date + 1))
		ORDER  BY p.created_at DESC
		LIMIT  $4`, f.Status, f.From, f.Until, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar pembayaran: %w", err)
	}
	defer rows.Close()

	out := []Payment{}
	for rows.Next() {
		x, err := pindai(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris pembayaran: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// Refunds mengembalikan refund atas sebuah pembayaran.
func (s *Service) Refunds(ctx context.Context, paymentID uuid.UUID) ([]Refund, error) {
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, payment_id, amount_cents, reason, coalesce(provider_ref, ''),
		       is_manual, actor_id, created_at
		FROM   refunds WHERE payment_id = $1 ORDER BY created_at`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("membaca refund: %w", err)
	}
	defer rows.Close()

	out := []Refund{}
	for rows.Next() {
		var r Refund
		err := rows.Scan(&r.ID, &r.PaymentID, &r.AmountCents, &r.Reason,
			&r.ProviderRef, &r.IsManual, &r.ActorID, &r.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris refund: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventsNeedingReview mengembalikan event yang menunggu tinjauan manusia.
//
// Inilah daftar yang dijanjikan SRS-PAY-003: status penyedia yang belum
// dikenal muncul di sini, bukan hilang.
func (s *Service) EventsNeedingReview(ctx context.Context, batas int) ([]Event, error) {
	if batas <= 0 || batas > 500 {
		batas = 100
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, event_id, payment_id, provider, provider_status,
		       mapped_status::text, amount_cents, needs_review, review_note,
		       processed_at, received_at
		FROM   payment_events
		WHERE  needs_review
		ORDER  BY received_at DESC
		LIMIT  $1`, batas)
	if err != nil {
		return nil, fmt.Errorf("membaca event yang perlu ditinjau: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		err := rows.Scan(&e.ID, &e.EventID, &e.PaymentID, &e.Provider, &e.ProviderStatus,
			&e.MappedStatus, &e.AmountCents, &e.NeedsReview, &e.ReviewNote,
			&e.ProcessedAt, &e.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
