package cart

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/iceman/backend/internal/catalog"
)

// Get membaca keranjang pelanggan beserta harga yang berlaku saat ini.
//
// Setiap pembacaan menghitung ulang harga dan memeriksa ketersediaan. Itulah
// cara perubahan harga oleh admin tercermin tanpa pekerjaan tambahan, dan cara
// produk yang baru dinonaktifkan langsung tertandai (SRS-ORD-001).
func (c *Carts) Get(ctx context.Context, customerID uuid.UUID) (*Cart, error) {
	cartID, err := c.ensureCart(ctx, c.pool, customerID)
	if err != nil {
		return nil, err
	}
	return c.read(ctx, c.pool, cartID, customerID)
}

// read menyusun keranjang beserta perhitungannya.
//
// Harga dibaca lewat satu query yang menggabungkan isi keranjang dengan produk
// dan harga khusus pelanggan, bukan dengan memanggil katalog per item. Satu
// keranjang berisi sepuluh produk akan menjadi sepuluh perjalanan ke basis
// data, dan pada jaringan yang lambat itu terasa.
func (c *Carts) read(ctx context.Context, q querier, cartID, customerID uuid.UUID) (*Cart, error) {
	rows, err := q.Query(ctx, `
		SELECT ci.id, ci.product_id, p.sku, p.name, p.packaging, ci.qty,
		       p.base_price_cents, p.min_order_qty, p.is_available, p.is_active,
		       harga.price_cents
		FROM   cart_items ci
		JOIN   products p ON p.id = ci.product_id
		LEFT JOIN LATERAL (
			SELECT cp.price_cents
			FROM   contract_prices cp
			WHERE  cp.product_id  = p.id
			  AND  cp.customer_id = $2
			  AND  cp.valid_from <= current_date
			  AND  (cp.valid_until IS NULL OR cp.valid_until >= current_date)
			ORDER  BY cp.valid_from DESC
			LIMIT  1
		) harga ON true
		WHERE  ci.cart_id = $1
		ORDER  BY p.category, p.name`, cartID, customerID)
	if err != nil {
		return nil, fmt.Errorf("membaca isi keranjang: %w", err)
	}
	defer rows.Close()

	out := &Cart{ID: cartID, CustomerID: customerID, Items: []Item{}}
	for rows.Next() {
		var (
			it              Item
			hargaDasar      int64
			tersedia, aktif bool
			khusus          *int64
		)
		err := rows.Scan(&it.ID, &it.ProductID, &it.SKU, &it.Name, &it.Packaging, &it.Qty,
			&hargaDasar, &it.MinOrderQty, &tersedia, &aktif, &khusus)
		if err != nil {
			return nil, fmt.Errorf("membaca baris keranjang: %w", err)
		}

		it.UnitPriceCents = hargaDasar
		if khusus != nil {
			it.UnitPriceCents = *khusus
			it.IsContractPrice = true
		}
		it.LineTotalCents = it.UnitPriceCents * int64(it.Qty)

		// Urutan pemeriksaan menentukan alasan yang dilaporkan. Produk yang
		// tidak aktif disebut lebih dahulu karena itu alasan yang paling
		// menentukan: pelanggan tidak dapat memperbaikinya dengan mengubah
		// jumlah.
		switch {
		case !aktif:
			it.Reason = ReasonInactive
		case !tersedia:
			it.Reason = ReasonUnavailable
		case it.Qty < it.MinOrderQty:
			it.Reason = ReasonMinOrder
		default:
			it.Usable = true
		}

		if it.Usable {
			out.SubtotalCents += it.LineTotalCents
			out.UsableCount++
		} else {
			out.BlockedCount++
		}
		out.Items = append(out.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membaca isi keranjang: %w", err)
	}
	return out, nil
}

// Add menambah produk ke keranjang.
//
// Menambah produk yang sudah ada menambah jumlahnya, bukan membuat baris baru
// (SRS-ORD-001). Itu dijaga indeks keunikan pada (cart_id, product_id) dan
// dikerjakan lewat ON CONFLICT, sehingga dua permintaan bersamaan untuk produk
// yang sama tetap menghasilkan satu baris dengan jumlah yang benar.
func (c *Carts) Add(ctx context.Context, customerID, productID uuid.UUID, qty int32) (*Cart, error) {
	if qty <= 0 {
		return nil, ErrQtyNegative
	}

	// Produk yang tidak aktif tidak boleh masuk keranjang sama sekali. Produk
	// habis juga ditolak di sini, karena SRS-CAT-001 menyatakan produk habis
	// tidak dapat ditambahkan walau tetap tampil di katalog.
	prod, err := c.products.PriceFor(ctx, &customerID, productID)
	if err != nil {
		if errors.Is(err, catalog.ErrProductNotFound) {
			return nil, catalog.ErrProductNotFound
		}
		return nil, err
	}
	if !prod.IsActive || !prod.IsAvailable {
		return nil, ErrProductUnusable
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	cartID, err := c.ensureCart(ctx, tx, customerID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (cart_id, product_id, qty)
		VALUES ($1, $2, $3)
		ON CONFLICT (cart_id, product_id)
		DO UPDATE SET qty = cart_items.qty + excluded.qty`,
		cartID, productID, qty); err != nil {
		return nil, fmt.Errorf("menambah item keranjang: %w", err)
	}

	hasil, err := c.read(ctx, tx, cartID, customerID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan keranjang: %w", err)
	}
	return hasil, nil
}

// SetQty menetapkan jumlah sebuah item.
//
// Jumlah nol menghapus baris dari keranjang (SRS-ORD-001). Itu membuat
// antarmuka tidak perlu memanggil endpoint lain hanya untuk menghapus, dan
// tombol kurangi pada jumlah satu bekerja apa adanya.
func (c *Carts) SetQty(ctx context.Context, customerID, productID uuid.UUID, qty int32) (*Cart, error) {
	if qty < 0 {
		return nil, ErrQtyNegative
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	cartID, err := c.ensureCart(ctx, tx, customerID)
	if err != nil {
		return nil, err
	}

	// Kepemilikan dijaga lewat cart_id yang berasal dari pelanggan pemanggil,
	// jadi item milik keranjang lain tidak pernah tersentuh.
	const hapus = `DELETE FROM cart_items WHERE cart_id = $1 AND product_id = $2`
	const ubah = `UPDATE cart_items SET qty = $3 WHERE cart_id = $1 AND product_id = $2`

	var (
		tag  pgconn.CommandTag
		err2 error
	)
	if qty == 0 {
		tag, err2 = tx.Exec(ctx, hapus, cartID, productID)
	} else {
		tag, err2 = tx.Exec(ctx, ubah, cartID, productID, qty)
	}
	if err2 != nil {
		return nil, fmt.Errorf("mengubah isi keranjang: %w", err2)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrItemNotFound
	}

	hasil, err := c.read(ctx, tx, cartID, customerID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan keranjang: %w", err)
	}
	return hasil, nil
}

// Clear mengosongkan keranjang. Dipakai setelah pesanan berhasil dibuat.
func (c *Carts) Clear(ctx context.Context, customerID uuid.UUID) error {
	_, err := c.pool.Exec(ctx, `
		DELETE FROM cart_items
		WHERE  cart_id IN (SELECT id FROM carts WHERE customer_id = $1)`, customerID)
	if err != nil {
		return fmt.Errorf("mengosongkan keranjang: %w", err)
	}
	return nil
}

// ClearTx mengosongkan keranjang di dalam transaksi pemanggil.
//
// Checkout memakainya agar pengosongan keranjang ikut batal bila pesanan gagal
// disimpan. Keranjang yang terkosongkan padahal pesanannya batal berarti
// pelanggan kehilangan pilihannya tanpa mendapat apa pun.
func (c *Carts) ClearTx(ctx context.Context, tx pgx.Tx, customerID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM cart_items
		WHERE  cart_id IN (SELECT id FROM carts WHERE customer_id = $1)`, customerID)
	if err != nil {
		return fmt.Errorf("mengosongkan keranjang: %w", err)
	}
	return nil
}
