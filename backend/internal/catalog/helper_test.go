package catalog_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/store"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	if dsn() == "" {
		t.Skip("ICEMAN_TEST_DSN belum diisi, jalankan lewat make test")
	}
	if err := store.Migrate(ctx, dsn()); err != nil {
		t.Skipf("basis data uji tidak tersedia, lewati: %v", err)
	}
	pool, err := store.Connect(ctx, dsn())
	if err != nil {
		t.Skipf("basis data uji tidak tersedia, lewati: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// isolasiProduk menonaktifkan produk yang sudah ada sebelumnya.
//
// Katalog membaca seluruh produk aktif, sehingga produk sisa dari berkas uji
// lain ikut terbaca dan membuat hasil penghitungan tidak pasti. Menonaktifkan
// lebih murah daripada menghapus baris yang mungkin masih dirujuk tabel lain.
func isolasiProduk(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE products SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("mengisolasi produk: %v", err)
	}
}

func newProducts(t *testing.T) (*catalog.Products, *pgxpool.Pool) {
	t.Helper()
	pool := newPool(t)
	isolasiProduk(t, pool)
	return catalog.NewProducts(pool), pool
}

func buatProduk(t *testing.T, p *catalog.Products, nama string, harga int64, minOrder int32) *catalog.Product {
	t.Helper()
	prod, err := p.Create(context.Background(), catalog.ProductInput{
		SKU:            "UJI-" + uuid.NewString()[:8],
		Name:           nama,
		Category:       "Balok",
		Packaging:      "Balok 25 kg",
		BasePriceCents: harga,
		MinOrderQty:    minOrder,
	})
	if err != nil {
		t.Fatalf("membuat produk %s: %v", nama, err)
	}
	return prod
}

// buatPelanggan membuat satu pelanggan untuk menguji harga khusus.
func buatPelanggan(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO customers (phone, name, type)
		VALUES ($1, 'Pelanggan Uji', 'CONTRACT')
		RETURNING id`, "0811"+uuid.NewString()[:8]).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pelanggan: %v", err)
	}
	return id
}
