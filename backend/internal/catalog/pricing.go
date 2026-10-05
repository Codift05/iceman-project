package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// Alasan sebuah produk tidak dapat dipesan.
const ReasonUnavailable = "UNAVAILABLE"

// CatalogItem adalah produk sebagaimana dilihat seorang pelanggan, lengkap
// dengan harga yang berlaku untuknya.
type CatalogItem struct {
	Product
	// PriceCents adalah harga yang berlaku: harga khusus pelanggan bila ada,
	// selain itu harga dasar produk.
	PriceCents int64 `json:"price_cents"`
	// IsContractPrice menandai harga ini berasal dari perjanjian khusus.
	// Pelanggan kontrak perlu tahu harga yang dilihatnya memang harga miliknya.
	IsContractPrice bool `json:"is_contract_price"`
	// Selectable menandai produk dapat ditambahkan ke keranjang.
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason,omitempty"`
}

// hargaBerlaku memilih harga khusus pelanggan yang masih dalam masa berlaku.
//
// Bila ada beberapa yang berlaku bersamaan, yang dipakai adalah yang masa
// berlakunya mulai paling akhir. Itu menjadikan baris baru menimpa baris lama
// tanpa perlu menonaktifkan yang lama lebih dahulu.
const hargaBerlaku = `
	LEFT JOIN LATERAL (
		SELECT cp.price_cents
		FROM   contract_prices cp
		WHERE  cp.product_id  = p.id
		  AND  cp.customer_id = $1
		  AND  cp.valid_from <= current_date
		  AND  (cp.valid_until IS NULL OR cp.valid_until >= current_date)
		ORDER  BY cp.valid_from DESC
		LIMIT  1
	) harga ON true`

// CatalogFor mengembalikan katalog untuk seorang pelanggan.
//
// Pelanggan tanpa akun boleh melewatkan customerID; yang terlihat adalah harga
// dasar. Produk tidak aktif tidak pernah muncul di sini, apa pun saringannya.
//
// Produk yang habis tetap muncul namun ditandai dan tidak dapat dipilih
// (SRS-CAT-001). Menghilangkannya membuat pelanggan mengira Iceman tidak
// menjual produk itu, padahal hanya sedang kosong.
func (p *Products) CatalogFor(ctx context.Context, customerID *uuid.UUID, f Filter) ([]CatalogItem, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT p.id, p.sku, p.name, p.category, p.packaging,
		       p.base_price_cents, p.min_order_qty, p.is_available, p.is_active,
		       p.photo_url, harga.price_cents
		FROM   products p`+hargaBerlaku+`
		WHERE  p.is_active
		  AND  ($2 = '' OR p.category = $2)
		  AND  ($3 = '' OR p.name ILIKE '%' || $3 || '%')
		ORDER  BY p.category, p.name`,
		customerID, f.Category, f.Search)
	if err != nil {
		return nil, fmt.Errorf("membaca katalog: %w", err)
	}
	defer rows.Close()

	out := []CatalogItem{}
	for rows.Next() {
		var (
			x      CatalogItem
			khusus *int64
		)
		err := rows.Scan(&x.ID, &x.SKU, &x.Name, &x.Category, &x.Packaging,
			&x.BasePriceCents, &x.MinOrderQty, &x.IsAvailable, &x.IsActive,
			&x.PhotoURL, &khusus)
		if err != nil {
			return nil, fmt.Errorf("membaca baris katalog: %w", err)
		}

		x.PriceCents = x.BasePriceCents
		if khusus != nil {
			x.PriceCents = *khusus
			x.IsContractPrice = true
		}
		if x.IsAvailable {
			x.Selectable = true
		} else {
			x.Reason = ReasonUnavailable
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// PricedProduct adalah produk beserta harga yang berlaku bagi satu pelanggan,
// dipakai saat menghitung keranjang dan membuat pesanan.
type PricedProduct struct {
	Product
	PriceCents      int64
	IsContractPrice bool
}

// PriceFor membaca satu produk beserta harga yang berlaku bagi pelanggan.
//
// Dipakai keranjang dan checkout, sehingga harga yang masuk ke pesanan selalu
// dihitung di sisi server dan tidak pernah dipercaya dari klien.
func (p *Products) PriceFor(ctx context.Context, customerID *uuid.UUID, productID uuid.UUID) (*PricedProduct, error) {
	var (
		x      PricedProduct
		khusus *int64
	)
	err := p.pool.QueryRow(ctx, `
		SELECT p.id, p.sku, p.name, p.category, p.packaging,
		       p.base_price_cents, p.min_order_qty, p.is_available, p.is_active,
		       p.photo_url, harga.price_cents
		FROM   products p`+hargaBerlaku+`
		WHERE  p.id = $2`, customerID, productID).
		Scan(&x.ID, &x.SKU, &x.Name, &x.Category, &x.Packaging,
			&x.BasePriceCents, &x.MinOrderQty, &x.IsAvailable, &x.IsActive,
			&x.PhotoURL, &khusus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca harga produk: %w", err)
	}

	x.PriceCents = x.BasePriceCents
	if khusus != nil {
		x.PriceCents = *khusus
		x.IsContractPrice = true
	}
	return &x, nil
}

// SetContractPrice menetapkan harga khusus seorang pelanggan untuk satu produk.
//
// Harga khusus tidak mengubah pesanan yang sudah dibuat, karena pesanan
// menyimpan salinan harganya sendiri (BR-007).
func (p *Products) SetContractPrice(ctx context.Context, customerID, productID uuid.UUID, priceCents int64, berlakuDari string) error {
	if priceCents < 0 {
		return ErrPriceNegative
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO contract_prices (customer_id, product_id, price_cents, valid_from)
		VALUES ($1, $2, $3, $4::date)
		ON CONFLICT (customer_id, product_id, valid_from)
		DO UPDATE SET price_cents = excluded.price_cents
		RETURNING id`, customerID, productID, priceCents, berlakuDari).Scan(&id)
	if err != nil {
		return fmt.Errorf("menyimpan harga khusus: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "contract_prices", EntityID: &id, Action: audit.ActionUpdate,
		After: map[string]any{
			"customer_id": customerID,
			"product_id":  productID,
			"price_cents": priceCents,
		},
		Detail: fmt.Sprintf("harga khusus ditetapkan %d sen berlaku dari %s",
			priceCents, berlakuDari),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
