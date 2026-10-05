// Package catalog mengelola produk Iceman beserta harganya.
//
// Harga disimpan dalam sen sebagai bilangan bulat, bukan pecahan, agar tidak
// ada pembulatan yang menumpuk pada penjumlahan pesanan (Database Bab 3).
package catalog

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

// Galat domain katalog.
var (
	ErrProductNotFound = errors.New("produk tidak ditemukan")
	ErrSKUExists       = errors.New("kode produk sudah dipakai")
	ErrPriceNegative   = errors.New("harga tidak boleh negatif")
	ErrMinOrderTooLow  = errors.New("minimum order minimal satu")
	ErrNameRequired    = errors.New("nama produk wajib diisi")
	ErrSKURequired     = errors.New("kode produk wajib diisi")
)

// Product adalah satu produk pada katalog.
type Product struct {
	ID             uuid.UUID `json:"id"`
	SKU            string    `json:"sku"`
	Name           string    `json:"name"`
	Category       string    `json:"category"`
	Packaging      string    `json:"packaging"`
	BasePriceCents int64     `json:"base_price_cents"`
	MinOrderQty    int32     `json:"min_order_qty"`
	IsAvailable    bool      `json:"is_available"`
	IsActive       bool      `json:"is_active"`
	PhotoURL       string    `json:"photo_url"`
}

// ProductInput adalah data yang diterima saat membuat atau mengubah produk.
type ProductInput struct {
	SKU            string
	Name           string
	Category       string
	Packaging      string
	BasePriceCents int64
	MinOrderQty    int32
	IsAvailable    *bool
	IsActive       *bool
	PhotoURL       string
}

func (in ProductInput) validate(butuhSKU bool) error {
	switch {
	case butuhSKU && strings.TrimSpace(in.SKU) == "":
		return ErrSKURequired
	case strings.TrimSpace(in.Name) == "":
		return ErrNameRequired
	case in.BasePriceCents < 0:
		return ErrPriceNegative
	case in.MinOrderQty < 1:
		return ErrMinOrderTooLow
	}
	return nil
}

// Filter menyaring daftar produk.
type Filter struct {
	Category string
	// Search mencari pada nama produk, tidak membedakan huruf besar kecil.
	Search string
	// IncludeInactive hanya dipakai tampilan admin. Katalog pelanggan tidak
	// boleh memuat produk tidak aktif (SRS-CAT-001).
	IncludeInactive bool
}

// Products menangani pengelolaan produk.
type Products struct{ pool *pgxpool.Pool }

// NewProducts membuat pengelola produk.
func NewProducts(pool *pgxpool.Pool) *Products { return &Products{pool: pool} }

const kolomProduk = `id, sku, name, category, packaging,
	       base_price_cents, min_order_qty, is_available, is_active, photo_url`

func pindaiProduk(row pgx.Row) (*Product, error) {
	var x Product
	err := row.Scan(&x.ID, &x.SKU, &x.Name, &x.Category, &x.Packaging,
		&x.BasePriceCents, &x.MinOrderQty, &x.IsAvailable, &x.IsActive, &x.PhotoURL)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// Get membaca satu produk tanpa memandang statusnya, untuk keperluan admin.
func (p *Products) Get(ctx context.Context, id uuid.UUID) (*Product, error) {
	x, err := pindaiProduk(p.pool.QueryRow(ctx,
		`SELECT `+kolomProduk+` FROM products WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca produk: %w", err)
	}
	return x, nil
}

// List mengembalikan produk sesuai saringan.
func (p *Products) List(ctx context.Context, f Filter) ([]Product, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT `+kolomProduk+`
		FROM   products
		WHERE  ($1 OR is_active)
		  AND  ($2 = '' OR category = $2)
		  AND  ($3 = '' OR name ILIKE '%' || $3 || '%')
		ORDER  BY category, name`,
		f.IncludeInactive, f.Category, f.Search)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar produk: %w", err)
	}
	defer rows.Close()

	out := []Product{}
	for rows.Next() {
		x, err := pindaiProduk(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris produk: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// Create menambah produk baru.
func (p *Products) Create(ctx context.Context, in ProductInput) (*Product, error) {
	if err := in.validate(true); err != nil {
		return nil, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	tersedia, aktif := true, true
	if in.IsAvailable != nil {
		tersedia = *in.IsAvailable
	}
	if in.IsActive != nil {
		aktif = *in.IsActive
	}

	x, err := pindaiProduk(tx.QueryRow(ctx, `
		INSERT INTO products
		       (sku, name, category, packaging, base_price_cents,
		        min_order_qty, is_available, is_active, photo_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+kolomProduk,
		strings.TrimSpace(in.SKU), strings.TrimSpace(in.Name), in.Category, in.Packaging,
		in.BasePriceCents, in.MinOrderQty, tersedia, aktif, in.PhotoURL))
	if isUniqueViolation(err) {
		return nil, ErrSKUExists
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan produk: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "products", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan produk: %w", err)
	}
	return x, nil
}

// Update mengubah produk beserta harganya.
//
// Perubahan harga, minimum order, dan status aktif termasuk data kritis
// menurut BR-005, sehingga tercatat beserta nilai lama dan barunya.
// Menonaktifkan produk tidak mengubah pesanan yang sudah dibuat, karena
// pesanan menyimpan salinan harga dan kemasannya sendiri.
//
// Kode produk tidak diubah di sini. Menurut SRS-CAT-002 kode boleh diubah
// selama belum dipakai pada pesanan, dan pemeriksaan itu baru dapat dilakukan
// setelah tabel pesanan ada. Sampai saat itu, menolak perubahan kode lebih
// aman daripada mengizinkannya tanpa pemeriksaan.
func (p *Products) Update(ctx context.Context, id uuid.UUID, in ProductInput) (*Product, error) {
	if err := in.validate(false); err != nil {
		return nil, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	before, err := pindaiProduk(tx.QueryRow(ctx,
		`SELECT `+kolomProduk+` FROM products WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci produk: %w", err)
	}

	tersedia, aktif := before.IsAvailable, before.IsActive
	if in.IsAvailable != nil {
		tersedia = *in.IsAvailable
	}
	if in.IsActive != nil {
		aktif = *in.IsActive
	}

	after, err := pindaiProduk(tx.QueryRow(ctx, `
		UPDATE products
		SET    name = $2, category = $3, packaging = $4, base_price_cents = $5,
		       min_order_qty = $6, is_available = $7, is_active = $8, photo_url = $9
		WHERE  id = $1
		RETURNING `+kolomProduk,
		id, strings.TrimSpace(in.Name), in.Category, in.Packaging, in.BasePriceCents,
		in.MinOrderQty, tersedia, aktif, in.PhotoURL))
	if err != nil {
		return nil, fmt.Errorf("mengubah produk: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "products", EntityID: &id, Action: audit.ActionUpdate,
		Before: before, After: after,
		Detail: rinciPerubahan(before, after),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah produk: %w", err)
	}
	return after, nil
}

// rinciPerubahan menyebut perubahan yang berdampak pada harga yang dibayar
// pelanggan, agar jejak audit dapat dibaca tanpa membandingkan dua JSON.
func rinciPerubahan(before, after *Product) string {
	var bagian []string
	if before.BasePriceCents != after.BasePriceCents {
		bagian = append(bagian, fmt.Sprintf("harga %d menjadi %d sen",
			before.BasePriceCents, after.BasePriceCents))
	}
	if before.MinOrderQty != after.MinOrderQty {
		bagian = append(bagian, fmt.Sprintf("minimum order %d menjadi %d",
			before.MinOrderQty, after.MinOrderQty))
	}
	if before.IsActive != after.IsActive {
		kata := map[bool]string{true: "diaktifkan", false: "dinonaktifkan"}
		bagian = append(bagian, "produk "+kata[after.IsActive])
	}
	if before.IsAvailable != after.IsAvailable {
		kata := map[bool]string{true: "tersedia", false: "habis"}
		bagian = append(bagian, "ketersediaan menjadi "+kata[after.IsAvailable])
	}
	return strings.Join(bagian, ", ")
}

// isUniqueViolation mengenali pelanggaran kekangan keunikan dari PostgreSQL.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
