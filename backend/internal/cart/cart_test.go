package cart_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
)

func TestKeranjang_TambahDanHitungSubtotal(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	k, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 3)
	if err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	if len(k.Items) != 1 {
		t.Fatalf("item keranjang %d, seharusnya 1", len(k.Items))
	}
	if k.Items[0].Qty != 3 {
		t.Fatalf("jumlah %d, seharusnya 3", k.Items[0].Qty)
	}
	if k.Items[0].LineTotalCents != 7500000 {
		t.Fatalf("total baris %d, seharusnya 7500000", k.Items[0].LineTotalCents)
	}
	if k.SubtotalCents != 7500000 {
		t.Fatalf("subtotal %d, seharusnya 7500000", k.SubtotalCents)
	}
	if !k.Checkoutable() {
		t.Fatal("keranjang ini seharusnya dapat dibawa ke checkout")
	}
}

// TestKeranjang_TambahProdukSamaMenambahJumlah menjaga SRS-ORD-001. Baris baru
// untuk produk yang sama membuat satu produk muncul dua kali di keranjang dan
// di pesanan.
func TestKeranjang_TambahProdukSamaMenambahJumlah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 1000000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah pertama: %v", err)
	}
	k, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 3)
	if err != nil {
		t.Fatalf("menambah kedua: %v", err)
	}
	if len(k.Items) != 1 {
		t.Fatalf("item keranjang %d, seharusnya tetap 1 baris", len(k.Items))
	}
	if k.Items[0].Qty != 5 {
		t.Fatalf("jumlah %d, seharusnya 5", k.Items[0].Qty)
	}
}

// TestKeranjang_HargaIkutBerubahSaatAdminUbahHarga adalah alasan keranjang
// tidak menyimpan harga. Harga yang tersimpan di keranjang akan terbawa ke
// checkout walau admin sudah mengubahnya (SRS-ORD-001).
func TestKeranjang_HargaIkutBerubahSaatAdminUbahHarga(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}

	l.ubahHarga(t, balok, 2750000)

	k, err := l.keranjang.Get(ctx, pelanggan)
	if err != nil {
		t.Fatalf("membaca keranjang: %v", err)
	}
	if k.Items[0].UnitPriceCents != 2750000 {
		t.Fatalf("harga satuan %d, seharusnya mengikuti harga baru 2750000",
			k.Items[0].UnitPriceCents)
	}
	if k.SubtotalCents != 5500000 {
		t.Fatalf("subtotal %d, seharusnya 5500000", k.SubtotalCents)
	}
}

// TestKeranjang_HargaKhususPelangganDipakai menjaga agar keranjang memakai
// jalur harga yang sama dengan katalog.
func TestKeranjang_HargaKhususPelangganDipakai(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if err := l.produk.SetContractPrice(ctx, pelanggan, balok.ID, 2200000,
		"2020-01-01"); err != nil {
		t.Fatalf("menetapkan harga khusus: %v", err)
	}
	k, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2)
	if err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	if k.Items[0].UnitPriceCents != 2200000 || !k.Items[0].IsContractPrice {
		t.Fatalf("harga khusus tidak dipakai: %+v", k.Items[0])
	}
	if k.SubtotalCents != 4400000 {
		t.Fatalf("subtotal %d, seharusnya 4400000", k.SubtotalCents)
	}
}

// TestKeranjang_ItemProdukNonaktifDitandaiDanTidakDihitung menjaga SRS-ORD-001:
// item yang produknya menjadi tidak aktif ditandai dan tidak ikut dihitung.
// Menghapusnya diam diam membuat pelanggan tidak tahu mengapa totalnya berubah.
func TestKeranjang_ItemProdukNonaktifDitandaiDanTidakDihitung(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	kristal := l.buatProduk(t, "Es Kristal", 1500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah balok: %v", err)
	}
	if _, err := l.keranjang.Add(ctx, pelanggan, kristal.ID, 1); err != nil {
		t.Fatalf("menambah kristal: %v", err)
	}

	l.setPenanda(t, kristal, false, true)

	k, err := l.keranjang.Get(ctx, pelanggan)
	if err != nil {
		t.Fatalf("membaca keranjang: %v", err)
	}
	if len(k.Items) != 2 {
		t.Fatalf("item keranjang %d, seharusnya tetap 2 agar pelanggan tahu apa yang gagal",
			len(k.Items))
	}

	var terhalang *cart.Item
	for i := range k.Items {
		if k.Items[i].ProductID == kristal.ID {
			terhalang = &k.Items[i]
		}
	}
	if terhalang == nil {
		t.Fatal("item produk nonaktif hilang dari keranjang")
	}
	if terhalang.Usable || terhalang.Reason != cart.ReasonInactive {
		t.Fatalf("item nonaktif salah ditandai: %+v", terhalang)
	}
	if k.SubtotalCents != 5000000 {
		t.Fatalf("subtotal %d, seharusnya hanya menjumlahkan balok yaitu 5000000",
			k.SubtotalCents)
	}
	if k.Checkoutable() {
		t.Fatal("keranjang dengan item terhalang seharusnya belum dapat di-checkout")
	}
}

// TestKeranjang_ProdukHabisDitandaiSebagaiTidakTersedia memisahkan alasan habis
// dari alasan nonaktif. Keduanya menghalangi, namun hanya yang habis dapat
// pulih sendiri, sehingga pesan ke pelanggan berbeda.
func TestKeranjang_ProdukHabisDitandaiSebagaiTidakTersedia(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	l.setPenanda(t, balok, true, false)

	k, err := l.keranjang.Get(ctx, pelanggan)
	if err != nil {
		t.Fatalf("membaca keranjang: %v", err)
	}
	if k.Items[0].Reason != cart.ReasonUnavailable {
		t.Fatalf("alasan %q, seharusnya %q", k.Items[0].Reason, cart.ReasonUnavailable)
	}
}

// TestKeranjang_MinimumOrderBelumTerpenuhiDitandai menjaga agar minimum order
// per produk diperiksa, bukan hanya minimum pesanan secara keseluruhan.
func TestKeranjang_MinimumOrderBelumTerpenuhiDitandai(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 5)

	k, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2)
	if err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	if k.Items[0].Usable {
		t.Fatal("jumlah di bawah minimum order seharusnya menghalangi")
	}
	if k.Items[0].Reason != cart.ReasonMinOrder {
		t.Fatalf("alasan %q, seharusnya %q", k.Items[0].Reason, cart.ReasonMinOrder)
	}
	if k.SubtotalCents != 0 {
		t.Fatalf("subtotal %d, seharusnya 0 karena satu satunya item terhalang",
			k.SubtotalCents)
	}

	// Menambah sampai memenuhi minimum membukanya kembali.
	k, err = l.keranjang.SetQty(ctx, pelanggan, balok.ID, 5)
	if err != nil {
		t.Fatalf("mengubah jumlah: %v", err)
	}
	if !k.Items[0].Usable || k.SubtotalCents != 12500000 {
		t.Fatalf("setelah memenuhi minimum seharusnya terbuka: %+v", k.Items[0])
	}
}

// TestKeranjang_JumlahNolMenghapusBaris menjaga SRS-ORD-001 agar antarmuka
// tidak perlu endpoint terpisah untuk menghapus.
func TestKeranjang_JumlahNolMenghapusBaris(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	k, err := l.keranjang.SetQty(ctx, pelanggan, balok.ID, 0)
	if err != nil {
		t.Fatalf("menghapus item: %v", err)
	}
	if len(k.Items) != 0 {
		t.Fatalf("item tersisa %d, seharusnya 0", len(k.Items))
	}
	if k.Checkoutable() {
		t.Fatal("keranjang kosong seharusnya tidak dapat di-checkout")
	}
}

// TestKeranjang_ProdukNonaktifTidakDapatDitambahkan menjaga agar produk yang
// sudah dinonaktifkan tidak dapat masuk keranjang sejak awal.
func TestKeranjang_ProdukNonaktifTidakDapatDitambahkan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.setPenanda(t, balok, false, true)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 1); !errors.Is(err, cart.ErrProductUnusable) {
		t.Fatalf("produk nonaktif seharusnya ditolak, dapat %v", err)
	}
}

// TestKeranjang_ProdukHabisTidakDapatDitambahkan menjaga SRS-CAT-001: produk
// habis tampil di katalog namun tidak dapat ditambahkan.
func TestKeranjang_ProdukHabisTidakDapatDitambahkan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.setPenanda(t, balok, true, false)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 1); !errors.Is(err, cart.ErrProductUnusable) {
		t.Fatalf("produk habis seharusnya ditolak, dapat %v", err)
	}
}

// TestKeranjang_PelangganLainTidakTerpengaruh menjaga agar keranjang benar
// benar terpisah per pelanggan.
func TestKeranjang_PelangganLainTidakTerpengaruh(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	satu := l.buatPelanggan(t)
	dua := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, satu, balok.ID, 4); err != nil {
		t.Fatalf("menambah ke keranjang pertama: %v", err)
	}

	kedua, err := l.keranjang.Get(ctx, dua)
	if err != nil {
		t.Fatalf("membaca keranjang kedua: %v", err)
	}
	if len(kedua.Items) != 0 {
		t.Fatalf("keranjang pelanggan lain memuat %d item", len(kedua.Items))
	}

	// Mengubah item yang bukan miliknya tidak boleh berhasil.
	if _, err := l.keranjang.SetQty(ctx, dua, balok.ID, 1); !errors.Is(err, cart.ErrItemNotFound) {
		t.Fatalf("mengubah item pelanggan lain seharusnya ditolak, dapat %v", err)
	}

	pertama, err := l.keranjang.Get(ctx, satu)
	if err != nil {
		t.Fatalf("membaca keranjang pertama: %v", err)
	}
	if pertama.Items[0].Qty != 4 {
		t.Fatalf("jumlah keranjang pertama berubah menjadi %d", pertama.Items[0].Qty)
	}
}

func TestKeranjang_TolakJumlahTidakSah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 0); !errors.Is(err, cart.ErrQtyNegative) {
		t.Fatalf("menambah nol seharusnya ditolak, dapat %v", err)
	}
	if _, err := l.keranjang.SetQty(ctx, pelanggan, balok.ID, -1); !errors.Is(err, cart.ErrQtyNegative) {
		t.Fatalf("jumlah negatif seharusnya ditolak, dapat %v", err)
	}
}

func TestKeranjang_ProdukTidakAda(t *testing.T) {
	l := siapkan(t)
	pelanggan := l.buatPelanggan(t)
	_, err := l.keranjang.Add(context.Background(), pelanggan, uuid.New(), 1)
	if !errors.Is(err, catalog.ErrProductNotFound) {
		t.Fatalf("produk tidak ada seharusnya ErrProductNotFound, dapat %v", err)
	}
}

func TestKeranjang_Kosongkan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	pelanggan := l.buatPelanggan(t)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	if _, err := l.keranjang.Add(ctx, pelanggan, balok.ID, 2); err != nil {
		t.Fatalf("menambah ke keranjang: %v", err)
	}
	if err := l.keranjang.Clear(ctx, pelanggan); err != nil {
		t.Fatalf("mengosongkan keranjang: %v", err)
	}
	k, err := l.keranjang.Get(ctx, pelanggan)
	if err != nil {
		t.Fatalf("membaca keranjang: %v", err)
	}
	if len(k.Items) != 0 {
		t.Fatalf("item tersisa %d sesudah dikosongkan", len(k.Items))
	}
}
