// Package customer mengelola pelanggan, alamat pengiriman, dan termin
// kontraknya.
//
// Pelanggan dibedakan menjadi ritel dan kontrak. Pelanggan kontrak dengan
// termin yang masih berlaku tidak membayar di muka; pesanannya langsung masuk
// tahap pemrosesan (BR-002, SRS-ORD-002).
package customer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
)

// Galat domain pelanggan.
var (
	ErrNotFound        = errors.New("pelanggan tidak ditemukan")
	ErrPhoneExists     = errors.New("nomor telepon sudah terdaftar")
	ErrPhoneRequired   = errors.New("nomor telepon wajib diisi")
	ErrNameRequired    = errors.New("nama pelanggan wajib diisi")
	ErrAddressNotFound = errors.New("alamat tidak ditemukan")
	ErrAreaInactive    = errors.New("wilayah layanan tidak aktif")
	ErrCoordRange      = errors.New("koordinat di luar rentang yang sah")
	ErrTermInvalid     = errors.New("termin pembayaran tidak sah")
)

// Jenis pelanggan.
const (
	TypeRetail   = "RETAIL"
	TypeContract = "CONTRACT"
)

// Customer adalah satu pelanggan Iceman.
type Customer struct {
	ID       uuid.UUID `json:"id"`
	Phone    string    `json:"phone"`
	Name     string    `json:"name"`
	Email    string    `json:"email,omitempty"`
	Type     string    `json:"type"`
	IsActive bool      `json:"is_active"`
}

// Input adalah data yang diterima saat membuat atau mengubah pelanggan.
type Input struct {
	Phone    string
	Name     string
	Email    string
	Type     string
	IsActive *bool
}

func (in Input) validate(butuhTelepon bool) error {
	switch {
	case butuhTelepon && strings.TrimSpace(in.Phone) == "":
		return ErrPhoneRequired
	case strings.TrimSpace(in.Name) == "":
		return ErrNameRequired
	}
	return nil
}

// Customers menangani pengelolaan pelanggan.
type Customers struct{ pool *pgxpool.Pool }

// NewCustomers membuat pengelola pelanggan.
func NewCustomers(pool *pgxpool.Pool) *Customers { return &Customers{pool: pool} }

const kolom = `id, phone, name, coalesce(email, ''), type, is_active`

func pindai(row pgx.Row) (*Customer, error) {
	var x Customer
	if err := row.Scan(&x.ID, &x.Phone, &x.Name, &x.Email, &x.Type, &x.IsActive); err != nil {
		return nil, err
	}
	return &x, nil
}

// Get membaca satu pelanggan.
func (c *Customers) Get(ctx context.Context, id uuid.UUID) (*Customer, error) {
	x, err := pindai(c.pool.QueryRow(ctx, `SELECT `+kolom+` FROM customers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pelanggan: %w", err)
	}
	return x, nil
}

// ByPhone mencari pelanggan menurut nomor teleponnya, dipakai alur masuk dan
// pembuatan pesanan manual oleh admin.
func (c *Customers) ByPhone(ctx context.Context, phone string) (*Customer, error) {
	x, err := pindai(c.pool.QueryRow(ctx,
		`SELECT `+kolom+` FROM customers WHERE phone = $1`, strings.TrimSpace(phone)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mencari pelanggan: %w", err)
	}
	return x, nil
}

// Filter menyaring daftar pelanggan.
type Filter struct {
	// Search mencari pada nama dan nomor telepon.
	Search          string
	Type            string
	IncludeInactive bool
}

// List mengembalikan daftar pelanggan untuk tampilan admin.
func (c *Customers) List(ctx context.Context, f Filter) ([]Customer, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT `+kolom+`
		FROM   customers
		WHERE  ($1 OR is_active)
		  AND  ($2 = '' OR type::text = $2)
		  AND  ($3 = '' OR name ILIKE '%' || $3 || '%' OR phone LIKE '%' || $3 || '%')
		ORDER  BY name`,
		f.IncludeInactive, f.Type, f.Search)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar pelanggan: %w", err)
	}
	defer rows.Close()

	out := []Customer{}
	for rows.Next() {
		x, err := pindai(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris pelanggan: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// Create menambah pelanggan baru.
func (c *Customers) Create(ctx context.Context, in Input) (*Customer, error) {
	if err := in.validate(true); err != nil {
		return nil, err
	}
	jenis := in.Type
	if jenis == "" {
		jenis = TypeRetail
	}
	if jenis != TypeRetail && jenis != TypeContract {
		return nil, fmt.Errorf("jenis pelanggan %q tidak dikenali", in.Type)
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	aktif := true
	if in.IsActive != nil {
		aktif = *in.IsActive
	}

	x, err := pindai(tx.QueryRow(ctx, `
		INSERT INTO customers (phone, name, email, type, is_active)
		VALUES ($1, $2, nullif($3, ''), $4::customer_type, $5)
		RETURNING `+kolom,
		strings.TrimSpace(in.Phone), strings.TrimSpace(in.Name),
		strings.TrimSpace(in.Email), jenis, aktif))
	if isUniqueViolation(err) {
		return nil, ErrPhoneExists
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan pelanggan: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "customers", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan pelanggan: %w", err)
	}
	return x, nil
}

// Update mengubah pelanggan.
//
// Perubahan jenis pelanggan termasuk data kritis karena menentukan apakah
// pesanannya perlu dibayar di muka, sehingga ikut tercatat.
func (c *Customers) Update(ctx context.Context, id uuid.UUID, in Input) (*Customer, error) {
	if err := in.validate(false); err != nil {
		return nil, err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	before, err := pindai(tx.QueryRow(ctx,
		`SELECT `+kolom+` FROM customers WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pelanggan: %w", err)
	}

	jenis := before.Type
	if in.Type != "" {
		if in.Type != TypeRetail && in.Type != TypeContract {
			return nil, fmt.Errorf("jenis pelanggan %q tidak dikenali", in.Type)
		}
		jenis = in.Type
	}
	aktif := before.IsActive
	if in.IsActive != nil {
		aktif = *in.IsActive
	}

	after, err := pindai(tx.QueryRow(ctx, `
		UPDATE customers
		SET    name = $2, email = nullif($3, ''), type = $4::customer_type, is_active = $5
		WHERE  id = $1
		RETURNING `+kolom,
		id, strings.TrimSpace(in.Name), strings.TrimSpace(in.Email), jenis, aktif))
	if err != nil {
		return nil, fmt.Errorf("mengubah pelanggan: %w", err)
	}

	detail := ""
	if before.Type != after.Type {
		detail = fmt.Sprintf("jenis pelanggan %s menjadi %s", before.Type, after.Type)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "customers", EntityID: &id, Action: audit.ActionUpdate,
		Before: before, After: after, Detail: detail,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah pelanggan: %w", err)
	}
	return after, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
