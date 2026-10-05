package customer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// ContractTerm adalah termin pembayaran seorang pelanggan kontrak.
type ContractTerm struct {
	ID               uuid.UUID  `json:"id"`
	CustomerID       uuid.UUID  `json:"customer_id"`
	PaymentTermDays  int32      `json:"payment_term_days"`
	CreditLimitCents int64      `json:"credit_limit_cents"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidUntil       *time.Time `json:"valid_until,omitempty"`
	IsActive         bool       `json:"is_active"`
}

// TermInput adalah data penetapan termin.
type TermInput struct {
	PaymentTermDays  int32
	CreditLimitCents int64
	ValidFrom        time.Time
	ValidUntil       *time.Time
}

// SetContractTerm menetapkan termin pembayaran pelanggan.
//
// Termin lama dinonaktifkan lebih dahulu, karena indeks keunikan hanya
// mengizinkan satu termin aktif per pelanggan. Termin lama tidak dihapus agar
// pesanan historis tetap dapat dijelaskan dengan termin yang berlaku saat itu.
func (c *Customers) SetContractTerm(ctx context.Context, customerID uuid.UUID, in TermInput) (*ContractTerm, error) {
	if in.PaymentTermDays <= 0 {
		return nil, ErrTermInvalid
	}
	if in.CreditLimitCents < 0 {
		return nil, ErrTermInvalid
	}
	if in.ValidUntil != nil && in.ValidUntil.Before(in.ValidFrom) {
		return nil, ErrTermInvalid
	}
	if in.ValidFrom.IsZero() {
		n := time.Now()
		in.ValidFrom = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var jenis string
	err = tx.QueryRow(ctx,
		`SELECT type::text FROM customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&jenis)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pelanggan: %w", err)
	}
	// Termin hanya bermakna bagi pelanggan kontrak. Menetapkannya pada
	// pelanggan ritel akan membuat pesanannya melewati pembayaran di muka
	// tanpa dasar perjanjian.
	if jenis != TypeContract {
		return nil, fmt.Errorf("termin hanya untuk pelanggan kontrak, pelanggan ini %s", jenis)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE contract_terms SET is_active = false
		WHERE  customer_id = $1 AND is_active`, customerID); err != nil {
		return nil, fmt.Errorf("menonaktifkan termin lama: %w", err)
	}

	var x ContractTerm
	err = tx.QueryRow(ctx, `
		INSERT INTO contract_terms
		       (customer_id, payment_term_days, credit_limit_cents, valid_from, valid_until)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, customer_id, payment_term_days, credit_limit_cents,
		          valid_from, valid_until, is_active`,
		customerID, in.PaymentTermDays, in.CreditLimitCents, in.ValidFrom, in.ValidUntil).
		Scan(&x.ID, &x.CustomerID, &x.PaymentTermDays, &x.CreditLimitCents,
			&x.ValidFrom, &x.ValidUntil, &x.IsActive)
	if err != nil {
		return nil, fmt.Errorf("menyimpan termin: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "contract_terms", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
		Detail: fmt.Sprintf("termin %d hari, plafon %d sen",
			x.PaymentTermDays, x.CreditLimitCents),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan termin: %w", err)
	}
	return &x, nil
}

// ActiveTerm mengembalikan termin yang berlaku hari ini, atau nil bila tidak
// ada.
//
// Nil dikembalikan tanpa galat karena tidak punya termin bukan keadaan salah:
// itu keadaan normal bagi pelanggan ritel.
func (c *Customers) ActiveTerm(ctx context.Context, customerID uuid.UUID) (*ContractTerm, error) {
	var x ContractTerm
	err := c.pool.QueryRow(ctx, `
		SELECT id, customer_id, payment_term_days, credit_limit_cents,
		       valid_from, valid_until, is_active
		FROM   contract_terms
		WHERE  customer_id = $1
		  AND  is_active
		  AND  valid_from <= current_date
		  AND  (valid_until IS NULL OR valid_until >= current_date)`, customerID).
		Scan(&x.ID, &x.CustomerID, &x.PaymentTermDays, &x.CreditLimitCents,
			&x.ValidFrom, &x.ValidUntil, &x.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("membaca termin: %w", err)
	}
	return &x, nil
}

// PaysOnTerms menjawab apakah pesanan pelanggan ini boleh melewati pembayaran
// di muka (BR-002).
//
// Jawabannya butuh dua hal sekaligus: pelanggan berjenis kontrak dan punya
// termin yang masih berlaku. Pelanggan kontrak yang terminnya sudah berakhir
// kembali membayar di muka seperti pelanggan ritel, dan itu memang yang
// dikehendaki: perjanjian yang habis tidak boleh terus dipakai.
func (c *Customers) PaysOnTerms(ctx context.Context, customerID uuid.UUID) (bool, *ContractTerm, error) {
	cust, err := c.Get(ctx, customerID)
	if err != nil {
		return false, nil, err
	}
	if cust.Type != TypeContract {
		return false, nil, nil
	}
	term, err := c.ActiveTerm(ctx, customerID)
	if err != nil {
		return false, nil, err
	}
	return term != nil, term, nil
}
