// Package cart mengelola keranjang belanja pelanggan.
//
// Keranjang disimpan di sisi server dan terikat pada akun pelanggan
// (SRS-ORD-001). Menyimpannya di perangkat membuat keranjang hilang saat
// pelanggan berpindah perangkat, dan membuat harga dapat diubah klien.
//
// Keranjang tidak menyimpan harga. Harga dan ketersediaan dihitung ulang
// setiap keranjang dibaca, sehingga perubahan harga oleh admin langsung
// tercermin. Harga baru disalin saat pesanan dibuat, karena sejak itu nilainya
// harus tetap (BR-007).
package cart

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/catalog"
)

// Galat domain keranjang.
var (
	ErrItemNotFound    = errors.New("item keranjang tidak ditemukan")
	ErrQtyNegative     = errors.New("jumlah tidak boleh negatif")
	ErrProductUnusable = errors.New("produk tidak dapat dipesan")
	ErrMinOrderNotMet  = errors.New("jumlah belum memenuhi minimum order")
	ErrCartEmpty       = errors.New("keranjang kosong")
)

// Alasan sebuah item tidak dapat diikutkan checkout.
const (
	ReasonInactive    = "PRODUCT_INACTIVE"
	ReasonUnavailable = "PRODUCT_UNAVAILABLE"
	ReasonMinOrder    = "MIN_ORDER_NOT_MET"
)

// Item adalah satu baris keranjang beserta harga yang berlaku saat dibaca.
type Item struct {
	ID        uuid.UUID `json:"id"`
	ProductID uuid.UUID `json:"product_id"`
	SKU       string    `json:"sku"`
	Name      string    `json:"name"`
	Packaging string    `json:"packaging,omitempty"`
	Qty       int32     `json:"qty"`
	// UnitPriceCents adalah harga yang berlaku bagi pelanggan ini saat
	// keranjang dibaca, bukan harga saat item ditambahkan.
	UnitPriceCents  int64 `json:"unit_price_cents"`
	LineTotalCents  int64 `json:"line_total_cents"`
	IsContractPrice bool  `json:"is_contract_price"`
	MinOrderQty     int32 `json:"min_order_qty"`
	// Usable menandai item ikut dihitung dan boleh dibawa ke checkout.
	Usable bool   `json:"usable"`
	Reason string `json:"reason,omitempty"`
}

// Cart adalah keranjang pelanggan beserta hasil penghitungannya.
type Cart struct {
	ID         uuid.UUID `json:"id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Items      []Item    `json:"items"`
	// SubtotalCents hanya menjumlahkan item yang dapat dipakai. Item yang
	// produknya tidak aktif atau habis tetap ditampilkan agar pelanggan tahu
	// apa yang gagal, namun tidak ikut dihitung (SRS-ORD-001).
	SubtotalCents int64 `json:"subtotal_cents"`
	// UsableCount dan BlockedCount memudahkan antarmuka memutuskan apakah
	// tombol checkout boleh aktif tanpa menelusuri seluruh item.
	UsableCount  int `json:"usable_count"`
	BlockedCount int `json:"blocked_count"`
}

// Checkoutable menjawab apakah keranjang ini boleh dibawa ke checkout.
func (c *Cart) Checkoutable() bool {
	return c.UsableCount > 0 && c.BlockedCount == 0
}

// Carts menangani pengelolaan keranjang.
type Carts struct {
	pool     *pgxpool.Pool
	products *catalog.Products
}

// NewCarts membuat pengelola keranjang.
func NewCarts(pool *pgxpool.Pool, products *catalog.Products) *Carts {
	return &Carts{pool: pool, products: products}
}

// ensureCart mengembalikan pengenal keranjang pelanggan, membuatnya bila belum
// ada.
//
// ON CONFLICT dipakai, bukan memeriksa lebih dahulu lalu menyisipkan, karena
// dua permintaan bersamaan dari satu pelanggan dapat sampai pada pemeriksaan
// yang sama lalu keduanya menyisipkan.
func (c *Carts) ensureCart(ctx context.Context, q querier, customerID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		INSERT INTO carts (customer_id) VALUES ($1)
		ON CONFLICT (customer_id) DO UPDATE SET customer_id = excluded.customer_id
		RETURNING id`, customerID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("menyiapkan keranjang: %w", err)
	}
	return id, nil
}

// querier mencakup pool maupun transaksi, agar pembantu di paket ini dapat
// dipakai di dalam dan di luar transaksi.
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}
