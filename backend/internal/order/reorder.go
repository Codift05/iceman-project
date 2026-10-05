package order

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/cart"
)

// ReorderResult adalah hasil pesan ulang.
type ReorderResult struct {
	Cart *cart.Cart `json:"cart"`
	// Skipped memuat item pesanan lama yang tidak dapat dipesan lagi. Daftar
	// ini dilaporkan kepada pelanggan sebelum ia melanjutkan (SRS-ORD-005).
	Skipped []cart.Skipped `json:"skipped"`
}

// Reorder menyusun keranjang baru dari sebuah pesanan sebelumnya.
//
// Jumlah tiap item disalin, namun harganya tidak. Keranjang menghitung harga
// dari keadaan terkini, sehingga total pesanan baru mengikuti harga sekarang,
// bukan harga pesanan lama (SRS-ORD-005). Itu memang yang dikehendaki: harga
// lama bukan penawaran yang masih berlaku.
//
// Pesanan lama tidak berubah sama sekali. Yang dibaca hanya isinya.
//
// Item yang produknya sudah dihentikan dilewati dan dilaporkan. Pesan ulang
// tetap berhasil untuk item lainnya, karena pelanggan lebih baik mendapat
// sebagian isinya beserta pemberitahuan daripada tidak mendapat apa pun.
func (o *Orders) Reorder(ctx context.Context, customerID, orderID uuid.UUID) (*ReorderResult, error) {
	// Kepemilikan diperiksa lewat pembacaan pesanan, sehingga pelanggan tidak
	// dapat menyalin isi pesanan orang lain ke keranjangnya.
	lama, err := o.GetForCustomer(ctx, customerID, orderID)
	if err != nil {
		return nil, err
	}
	if len(lama.Items) == 0 {
		return nil, fmt.Errorf("%w: pesanan ini tidak memuat item", ErrNotFound)
	}

	lines := make([]cart.Line, 0, len(lama.Items))
	for _, it := range lama.Items {
		lines = append(lines, cart.Line{ProductID: it.ProductID, Qty: it.Qty})
	}

	keranjang, dilewati, err := o.d.Carts.Replace(ctx, customerID, lines)
	if err != nil {
		return nil, err
	}

	// Bila tidak ada satu pun item yang dapat dipesan lagi, pesan ulang gagal.
	// Keranjang kosong beserta daftar kegagalan membuat pelanggan mengira
	// sesuatu rusak, padahal yang terjadi seluruh produknya sudah dihentikan.
	if len(keranjang.Items) == 0 {
		return &ReorderResult{Cart: keranjang, Skipped: dilewati}, ErrItemsUnavailable
	}
	return &ReorderResult{Cart: keranjang, Skipped: dilewati}, nil
}
