package catalog_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/catalog"
)

func TestProduk_BuatDanBaca(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok 25 kg", 2500000, 2)
	got, err := p.Get(ctx, prod.ID)
	if err != nil {
		t.Fatalf("membaca produk: %v", err)
	}
	if got.BasePriceCents != 2500000 || got.MinOrderQty != 2 || !got.IsActive {
		t.Fatalf("produk terbaca salah: %+v", got)
	}
}

func TestProduk_KodeGandaDitolak(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	sku := "UJI-" + uuid.NewString()[:8]
	in := catalog.ProductInput{SKU: sku, Name: "Es Kristal", BasePriceCents: 100000, MinOrderQty: 1}
	if _, err := p.Create(ctx, in); err != nil {
		t.Fatalf("membuat produk pertama: %v", err)
	}
	if _, err := p.Create(ctx, in); !errors.Is(err, catalog.ErrSKUExists) {
		t.Fatalf("kode ganda seharusnya ditolak, dapat %v", err)
	}
}

func TestProduk_ValidasiMasukan(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	kasus := []struct {
		nama string
		in   catalog.ProductInput
		mau  error
	}{
		{"tanpa kode", catalog.ProductInput{Name: "X", MinOrderQty: 1}, catalog.ErrSKURequired},
		{"tanpa nama", catalog.ProductInput{SKU: "A", MinOrderQty: 1}, catalog.ErrNameRequired},
		{"harga negatif", catalog.ProductInput{SKU: "A", Name: "X", BasePriceCents: -1, MinOrderQty: 1}, catalog.ErrPriceNegative},
		{"minimum order nol", catalog.ProductInput{SKU: "A", Name: "X", MinOrderQty: 0}, catalog.ErrMinOrderTooLow},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if _, err := p.Create(ctx, k.in); !errors.Is(err, k.mau) {
				t.Fatalf("dapat %v, seharusnya %v", err, k.mau)
			}
		})
	}
}

// TestProduk_PerubahanHargaTercatat menjaga BR-005: harga adalah data kritis,
// perubahannya wajib tercatat beserta nilai lama dan barunya.
func TestProduk_PerubahanHargaTercatat(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	after, err := p.Update(ctx, prod.ID, catalog.ProductInput{
		Name: prod.Name, Category: prod.Category, Packaging: prod.Packaging,
		BasePriceCents: 2750000, MinOrderQty: 1,
	})
	if err != nil {
		t.Fatalf("mengubah produk: %v", err)
	}
	if after.BasePriceCents != 2750000 {
		t.Fatalf("harga sesudah ubah %d, seharusnya 2750000", after.BasePriceCents)
	}

	var detail string
	err = pool.QueryRow(ctx, `
		SELECT detail FROM audit_trail
		WHERE  entity = 'products' AND entity_id = $1 AND action = 'UPDATE'`,
		prod.ID).Scan(&detail)
	if err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if detail != "harga 2500000 menjadi 2750000 sen" {
		t.Fatalf("rincian jejak audit %q tidak menyebut perubahan harga", detail)
	}
}

// TestProduk_KodeTidakBerubahSaatDiubah menjaga janji pada Update: kode produk
// tidak ikut berubah, karena pemeriksaan "sudah dipakai pada pesanan" belum
// dapat dilakukan sebelum tabel pesanan ada.
func TestProduk_KodeTidakBerubahSaatDiubah(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	after, err := p.Update(ctx, prod.ID, catalog.ProductInput{
		SKU: "COBA-GANTI", Name: prod.Name, BasePriceCents: prod.BasePriceCents, MinOrderQty: 1,
	})
	if err != nil {
		t.Fatalf("mengubah produk: %v", err)
	}
	if after.SKU != prod.SKU {
		t.Fatalf("kode produk berubah menjadi %q, seharusnya tetap %q", after.SKU, prod.SKU)
	}
}

func TestProduk_UbahProdukTidakAda(t *testing.T) {
	p, _ := newProducts(t)
	_, err := p.Update(context.Background(), uuid.New(), catalog.ProductInput{
		Name: "X", MinOrderQty: 1,
	})
	if !errors.Is(err, catalog.ErrProductNotFound) {
		t.Fatalf("produk tidak ada seharusnya ErrProductNotFound, dapat %v", err)
	}
}

func TestProduk_SaringKategoriDanCariNama(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	penanda := uuid.NewString()[:6]
	if _, err := p.Create(ctx, catalog.ProductInput{
		SKU: "UJI-" + uuid.NewString()[:8], Name: "Es Balok " + penanda,
		Category: "Balok", BasePriceCents: 100, MinOrderQty: 1,
	}); err != nil {
		t.Fatalf("membuat produk balok: %v", err)
	}
	if _, err := p.Create(ctx, catalog.ProductInput{
		SKU: "UJI-" + uuid.NewString()[:8], Name: "Es Kristal " + penanda,
		Category: "Kristal", BasePriceCents: 100, MinOrderQty: 1,
	}); err != nil {
		t.Fatalf("membuat produk kristal: %v", err)
	}

	hanyaBalok, err := p.List(ctx, catalog.Filter{Category: "Balok"})
	if err != nil {
		t.Fatalf("menyaring kategori: %v", err)
	}
	if len(hanyaBalok) != 1 || hanyaBalok[0].Category != "Balok" {
		t.Fatalf("saringan kategori salah, dapat %d baris", len(hanyaBalok))
	}

	// Pencarian tidak boleh membedakan huruf besar kecil.
	cari, err := p.List(ctx, catalog.Filter{Search: "kRiStAl"})
	if err != nil {
		t.Fatalf("mencari nama: %v", err)
	}
	if len(cari) != 1 {
		t.Fatalf("pencarian nama seharusnya menemukan 1, dapat %d", len(cari))
	}
}
