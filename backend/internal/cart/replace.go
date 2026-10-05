package cart

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Line adalah permintaan satu baris keranjang.
type Line struct {
	ProductID uuid.UUID
	Qty       int32
}

// Skipped adalah baris yang tidak dapat dimasukkan ke keranjang, beserta
// alasannya.
//
// Nama produk ikut dibawa agar pemanggil dapat memberi tahu pelanggan produk
// apa yang gagal tanpa perlu membaca katalog lagi.
type Skipped struct {
	ProductID uuid.UUID `json:"product_id"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"`
}

// Replace mengganti seluruh isi keranjang dengan baris yang diberikan.
//
// Baris yang produknya tidak aktif, habis, atau tidak ada lagi dilewati dan
// dilaporkan, bukan membuat seluruh penggantian gagal. Itu yang dibutuhkan
// pesan ulang: pesanan lama dapat memuat produk yang sejak itu dihentikan, dan
// pelanggan tetap lebih baik mendapat sebagian isinya beserta pemberitahuan
// daripada tidak mendapat apa pun (SRS-ORD-005).
//
// Penggantian dan pelewatan terjadi dalam satu transaksi, sehingga keranjang
// tidak pernah terlihat setengah terisi.
func (c *Carts) Replace(ctx context.Context, customerID uuid.UUID, lines []Line) (*Cart, []Skipped, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	cartID, err := c.ensureCart(ctx, tx, customerID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cart_items WHERE cart_id = $1`, cartID); err != nil {
		return nil, nil, fmt.Errorf("mengosongkan keranjang: %w", err)
	}

	dilewati := []Skipped{}
	for _, l := range lines {
		if l.Qty <= 0 {
			continue
		}

		// Keadaan produk dibaca di dalam transaksi yang sama dengan
		// penyisipannya, agar produk yang dinonaktifkan di tengah jalan tidak
		// lolos karena pemeriksaannya sudah lewat.
		var (
			nama            string
			aktif, tersedia bool
		)
		err := tx.QueryRow(ctx, `
			SELECT name, is_active, is_available FROM products WHERE id = $1`,
			l.ProductID).Scan(&nama, &aktif, &tersedia)
		if err != nil {
			// Produk yang barisnya sudah tidak ada sama sekali. Namanya tidak
			// dapat dibaca, jadi pengenalnya saja yang dilaporkan.
			dilewati = append(dilewati, Skipped{
				ProductID: l.ProductID, Reason: ReasonInactive,
			})
			continue
		}
		switch {
		case !aktif:
			dilewati = append(dilewati, Skipped{
				ProductID: l.ProductID, Name: nama, Reason: ReasonInactive,
			})
			continue
		case !tersedia:
			dilewati = append(dilewati, Skipped{
				ProductID: l.ProductID, Name: nama, Reason: ReasonUnavailable,
			})
			continue
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO cart_items (cart_id, product_id, qty)
			VALUES ($1, $2, $3)
			ON CONFLICT (cart_id, product_id)
			DO UPDATE SET qty = cart_items.qty + excluded.qty`,
			cartID, l.ProductID, l.Qty)
		if err != nil {
			return nil, nil, fmt.Errorf("menyisipkan item keranjang: %w", err)
		}
	}

	hasil, err := c.read(ctx, tx, cartID, customerID)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("menyimpan keranjang: %w", err)
	}
	return hasil, dilewati, nil
}
