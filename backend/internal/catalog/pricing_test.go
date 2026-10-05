package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/catalog"
)

func hariIni() string { return time.Now().Format("2006-01-02") }

// TestKatalog_ProdukTidakAktifTidakMuncul menjaga SRS-CAT-001. Produk yang
// dinonaktifkan harus hilang dari katalog pelanggan, bukan hanya ditandai.
func TestKatalog_ProdukTidakAktifTidakMuncul(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	aktif := buatProduk(t, p, "Es Balok Aktif", 2500000, 1)
	mati := buatProduk(t, p, "Es Balok Lama", 2000000, 1)

	nonaktif := false
	if _, err := p.Update(ctx, mati.ID, catalog.ProductInput{
		Name: mati.Name, BasePriceCents: mati.BasePriceCents,
		MinOrderQty: 1, IsActive: &nonaktif,
	}); err != nil {
		t.Fatalf("menonaktifkan produk: %v", err)
	}

	item, err := p.CatalogFor(ctx, nil, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	if len(item) != 1 {
		t.Fatalf("katalog seharusnya memuat 1 produk, dapat %d", len(item))
	}
	if item[0].ID != aktif.ID {
		t.Fatalf("produk yang muncul salah: %+v", item[0])
	}
}

// TestKatalog_ProdukHabisTetapMuncul menjaga sisi lain SRS-CAT-001. Produk
// habis sengaja tetap ditampilkan beserta penandanya. Menghilangkannya membuat
// pelanggan mengira Iceman tidak menjual produk itu, padahal hanya kosong.
func TestKatalog_ProdukHabisTetapMuncul(t *testing.T) {
	p, _ := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Kristal", 1500000, 1)
	habis := false
	if _, err := p.Update(ctx, prod.ID, catalog.ProductInput{
		Name: prod.Name, BasePriceCents: prod.BasePriceCents,
		MinOrderQty: 1, IsAvailable: &habis,
	}); err != nil {
		t.Fatalf("menandai habis: %v", err)
	}

	item, err := p.CatalogFor(ctx, nil, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	if len(item) != 1 {
		t.Fatalf("katalog seharusnya memuat 1 produk, dapat %d", len(item))
	}
	if item[0].Selectable {
		t.Fatal("produk habis seharusnya tidak dapat dipilih")
	}
	if item[0].Reason != catalog.ReasonUnavailable {
		t.Fatalf("alasan %q, seharusnya %q", item[0].Reason, catalog.ReasonUnavailable)
	}
}

// TestKatalog_HargaKhususMenggantikanHargaDasar menjaga janji SRS-CAT-001:
// pelanggan kontrak melihat harga miliknya, bukan harga dasar.
func TestKatalog_HargaKhususMenggantikanHargaDasar(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)

	if err := p.SetContractPrice(ctx, pelanggan, prod.ID, 2200000, hariIni()); err != nil {
		t.Fatalf("menetapkan harga khusus: %v", err)
	}

	// Pelanggan yang punya perjanjian melihat harga khususnya.
	punya, err := p.CatalogFor(ctx, &pelanggan, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog pelanggan: %v", err)
	}
	if punya[0].PriceCents != 2200000 || !punya[0].IsContractPrice {
		t.Fatalf("harga pelanggan kontrak salah: %+v", punya[0])
	}

	// Pengunjung tanpa akun tetap melihat harga dasar.
	umum, err := p.CatalogFor(ctx, nil, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog umum: %v", err)
	}
	if umum[0].PriceCents != 2500000 || umum[0].IsContractPrice {
		t.Fatalf("harga umum salah: %+v", umum[0])
	}
}

// TestKatalog_HargaKhususPelangganLainTidakBocor menjaga agar harga perjanjian
// seorang pelanggan tidak terlihat pelanggan lain. Harga kontrak hasil
// negosiasi, dan membocorkannya merusak posisi Iceman pada negosiasi berikutnya.
func TestKatalog_HargaKhususPelangganLainTidakBocor(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	punyaKontrak := buatPelanggan(t, pool)
	pelangganLain := buatPelanggan(t, pool)

	if err := p.SetContractPrice(ctx, punyaKontrak, prod.ID, 2000000, hariIni()); err != nil {
		t.Fatalf("menetapkan harga khusus: %v", err)
	}

	lain, err := p.CatalogFor(ctx, &pelangganLain, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog pelanggan lain: %v", err)
	}
	if lain[0].PriceCents != 2500000 || lain[0].IsContractPrice {
		t.Fatalf("harga pelanggan lain bocor: %+v", lain[0])
	}
}

// TestKatalog_HargaKhususKedaluwarsaDiabaikan menjaga masa berlaku. Harga yang
// sudah lewat periodenya tidak boleh terus dipakai.
func TestKatalog_HargaKhususKedaluwarsaDiabaikan(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)

	// Harga lama yang masa berlakunya sudah berakhir kemarin.
	_, err := pool.Exec(ctx, `
		INSERT INTO contract_prices (customer_id, product_id, price_cents, valid_from, valid_until)
		VALUES ($1, $2, 1000000, current_date - 10, current_date - 1)`, pelanggan, prod.ID)
	if err != nil {
		t.Fatalf("menyiapkan harga kedaluwarsa: %v", err)
	}

	item, err := p.CatalogFor(ctx, &pelanggan, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	if item[0].PriceCents != 2500000 || item[0].IsContractPrice {
		t.Fatalf("harga kedaluwarsa masih dipakai: %+v", item[0])
	}
}

// TestKatalog_HargaKhususBelumBerlakuDiabaikan menjaga sisi sebaliknya: harga
// yang dijadwalkan mulai besok belum boleh dipakai hari ini.
func TestKatalog_HargaKhususBelumBerlakuDiabaikan(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO contract_prices (customer_id, product_id, price_cents, valid_from)
		VALUES ($1, $2, 1000000, current_date + 1)`, pelanggan, prod.ID)
	if err != nil {
		t.Fatalf("menyiapkan harga mendatang: %v", err)
	}

	item, err := p.CatalogFor(ctx, &pelanggan, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	if item[0].PriceCents != 2500000 {
		t.Fatalf("harga yang belum berlaku sudah dipakai: %+v", item[0])
	}
}

// TestKatalog_HargaTerbaruMenangSaatDuaPeriodeBerlaku menjaga aturan pemilihan
// di hargaBerlaku: bila dua harga berlaku bersamaan, yang mulai paling akhir
// yang dipakai. Itu membuat penetapan harga baru menimpa yang lama tanpa perlu
// menutup periode lama lebih dahulu.
func TestKatalog_HargaTerbaruMenangSaatDuaPeriodeBerlaku(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO contract_prices (customer_id, product_id, price_cents, valid_from)
		VALUES ($1, $2, 2300000, current_date - 30),
		       ($1, $2, 2100000, current_date - 1)`, pelanggan, prod.ID)
	if err != nil {
		t.Fatalf("menyiapkan dua harga: %v", err)
	}

	item, err := p.CatalogFor(ctx, &pelanggan, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	if item[0].PriceCents != 2100000 {
		t.Fatalf("harga terpilih %d, seharusnya 2100000 yang mulai paling akhir",
			item[0].PriceCents)
	}
}

// TestPriceFor_SamaDenganKatalog menjaga agar harga yang dipakai checkout sama
// dengan harga yang dilihat pelanggan di katalog. Dua jalur perhitungan yang
// berbeda adalah cara paling mudah membuat pelanggan dibebani harga lain
// daripada yang ia lihat.
func TestPriceFor_SamaDenganKatalog(t *testing.T) {
	p, pool := newProducts(t)
	ctx := context.Background()

	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)
	if err := p.SetContractPrice(ctx, pelanggan, prod.ID, 2200000, hariIni()); err != nil {
		t.Fatalf("menetapkan harga khusus: %v", err)
	}

	item, err := p.CatalogFor(ctx, &pelanggan, catalog.Filter{})
	if err != nil {
		t.Fatalf("membaca katalog: %v", err)
	}
	satu, err := p.PriceFor(ctx, &pelanggan, prod.ID)
	if err != nil {
		t.Fatalf("membaca harga satu produk: %v", err)
	}
	if satu.PriceCents != item[0].PriceCents {
		t.Fatalf("harga checkout %d berbeda dari katalog %d",
			satu.PriceCents, item[0].PriceCents)
	}
	if satu.IsContractPrice != item[0].IsContractPrice {
		t.Fatal("penanda harga kontrak berbeda antara checkout dan katalog")
	}
}

func TestPriceFor_ProdukTidakAda(t *testing.T) {
	p, _ := newProducts(t)
	_, err := p.PriceFor(context.Background(), nil, uuid.New())
	if !errors.Is(err, catalog.ErrProductNotFound) {
		t.Fatalf("produk tidak ada seharusnya ErrProductNotFound, dapat %v", err)
	}
}

func TestSetContractPrice_TolakHargaNegatif(t *testing.T) {
	p, pool := newProducts(t)
	prod := buatProduk(t, p, "Es Balok", 2500000, 1)
	pelanggan := buatPelanggan(t, pool)

	err := p.SetContractPrice(context.Background(), pelanggan, prod.ID, -1, hariIni())
	if !errors.Is(err, catalog.ErrPriceNegative) {
		t.Fatalf("harga negatif seharusnya ditolak, dapat %v", err)
	}
}
