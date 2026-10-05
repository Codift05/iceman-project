package cart_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/cart"
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

type lingkungan struct {
	pool      *pgxpool.Pool
	produk    *catalog.Products
	keranjang *cart.Carts
}

func siapkan(t *testing.T) *lingkungan {
	t.Helper()
	pool := newPool(t)
	produk := catalog.NewProducts(pool)
	return &lingkungan{pool: pool, produk: produk, keranjang: cart.NewCarts(pool, produk)}
}

func (l *lingkungan) buatProduk(t *testing.T, nama string, harga int64, minOrder int32) *catalog.Product {
	t.Helper()
	prod, err := l.produk.Create(context.Background(), catalog.ProductInput{
		SKU: "UJI-" + uuid.NewString()[:8], Name: nama,
		Category: "Balok", Packaging: "Balok 25 kg",
		BasePriceCents: harga, MinOrderQty: minOrder,
	})
	if err != nil {
		t.Fatalf("membuat produk %s: %v", nama, err)
	}
	return prod
}

func (l *lingkungan) buatPelanggan(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO customers (phone, name, type)
		VALUES ($1, 'Pelanggan Keranjang', 'CONTRACT')
		RETURNING id`, "0811"+uuid.NewString()[:8]).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pelanggan: %v", err)
	}
	return id
}

// ubahHarga mengubah harga dasar produk, meniru admin yang menyunting katalog.
func (l *lingkungan) ubahHarga(t *testing.T, prod *catalog.Product, harga int64) {
	t.Helper()
	_, err := l.produk.Update(context.Background(), prod.ID, catalog.ProductInput{
		Name: prod.Name, Category: prod.Category, Packaging: prod.Packaging,
		BasePriceCents: harga, MinOrderQty: prod.MinOrderQty,
	})
	if err != nil {
		t.Fatalf("mengubah harga: %v", err)
	}
}

func (l *lingkungan) setPenanda(t *testing.T, prod *catalog.Product, aktif, tersedia bool) {
	t.Helper()
	_, err := l.produk.Update(context.Background(), prod.ID, catalog.ProductInput{
		Name: prod.Name, Category: prod.Category, Packaging: prod.Packaging,
		BasePriceCents: prod.BasePriceCents, MinOrderQty: prod.MinOrderQty,
		IsActive: &aktif, IsAvailable: &tersedia,
	})
	if err != nil {
		t.Fatalf("mengubah penanda produk: %v", err)
	}
}
