package order_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
)

// TestPesanUlang_MenyalinJumlahDenganHargaTerkini menjaga SRS-ORD-005: total
// pesanan baru mengikuti harga saat ini, bukan harga lama.
func TestPesanUlang_MenyalinJumlahDenganHargaTerkini(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 4)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	// Harga naik sesudah pesanan lama dibuat.
	if _, err := l.produk.Update(ctx, balok.ID, produkInput(balok, 3000000, nil)); err != nil {
		t.Fatalf("mengubah harga: %v", err)
	}

	hasil, err := l.pesanan.Reorder(ctx, p.ID, lama.ID)
	if err != nil {
		t.Fatalf("pesan ulang: %v", err)
	}
	if len(hasil.Skipped) != 0 {
		t.Fatalf("item dilewati %d, seharusnya 0", len(hasil.Skipped))
	}
	if len(hasil.Cart.Items) != 1 {
		t.Fatalf("item keranjang %d, seharusnya 1", len(hasil.Cart.Items))
	}
	if hasil.Cart.Items[0].Qty != 4 {
		t.Fatalf("jumlah %d, seharusnya disalin menjadi 4", hasil.Cart.Items[0].Qty)
	}
	if hasil.Cart.Items[0].UnitPriceCents != 3000000 {
		t.Fatalf("harga %d, seharusnya harga terkini 3000000",
			hasil.Cart.Items[0].UnitPriceCents)
	}
	if hasil.Cart.SubtotalCents != 12000000 {
		t.Fatalf("subtotal %d, seharusnya 12000000", hasil.Cart.SubtotalCents)
	}

	// Pesanan lama tidak berubah sama sekali.
	cek, err := l.pesanan.Get(ctx, lama.ID)
	if err != nil {
		t.Fatalf("membaca pesanan lama: %v", err)
	}
	if cek.Items[0].UnitPriceCents != 2500000 || cek.TotalCents != lama.TotalCents {
		t.Fatalf("pesanan lama berubah akibat pesan ulang: %+v", cek)
	}
}

// TestPesanUlang_ProdukDihentikanDilewatiDanDilaporkan menjaga janji
// SRS-ORD-005: pesan ulang tetap berhasil untuk item lainnya, disertai
// pemberitahuan.
func TestPesanUlang_ProdukDihentikanDilewatiDanDilaporkan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	kristal := l.buatProduk(t, "Es Kristal", 1500000, 1)
	l.isiKeranjang(t, p, balok, 2)
	l.isiKeranjang(t, p, kristal, 3)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	// Kristal dihentikan sesudahnya.
	nonaktif := false
	if _, err := l.produk.Update(ctx, kristal.ID,
		produkInput(kristal, kristal.BasePriceCents, &nonaktif)); err != nil {
		t.Fatalf("menonaktifkan produk: %v", err)
	}

	hasil, err := l.pesanan.Reorder(ctx, p.ID, lama.ID)
	if err != nil {
		t.Fatalf("pesan ulang seharusnya tetap berhasil: %v", err)
	}
	if len(hasil.Cart.Items) != 1 || hasil.Cart.Items[0].ProductID != balok.ID {
		t.Fatalf("keranjang seharusnya hanya memuat balok: %+v", hasil.Cart.Items)
	}
	if len(hasil.Skipped) != 1 {
		t.Fatalf("item dilewati %d, seharusnya 1", len(hasil.Skipped))
	}
	if hasil.Skipped[0].ProductID != kristal.ID {
		t.Fatalf("item yang dilewati salah: %+v", hasil.Skipped[0])
	}
	// Nama ikut dilaporkan agar pelanggan tahu produk apa yang gagal.
	if hasil.Skipped[0].Name != "Es Kristal" {
		t.Fatalf("nama item yang dilewati %q, seharusnya Es Kristal", hasil.Skipped[0].Name)
	}
	if hasil.Skipped[0].Reason != cart.ReasonInactive {
		t.Fatalf("alasan %q, seharusnya %q", hasil.Skipped[0].Reason, cart.ReasonInactive)
	}
}

// TestPesanUlang_ProdukHabisDibedakanDariDihentikan menjaga agar pelanggan
// tahu mana yang mungkin tersedia lagi nanti.
func TestPesanUlang_ProdukHabisDibedakanDariDihentikan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	kristal := l.buatProduk(t, "Es Kristal", 1500000, 1)
	l.isiKeranjang(t, p, balok, 1)
	l.isiKeranjang(t, p, kristal, 1)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	aktif, habis := true, false
	if _, err := l.produk.Update(ctx, kristal.ID, produkKetersediaan(kristal, aktif, habis)); err != nil {
		t.Fatalf("menandai habis: %v", err)
	}

	hasil, err := l.pesanan.Reorder(ctx, p.ID, lama.ID)
	if err != nil {
		t.Fatalf("pesan ulang: %v", err)
	}
	if len(hasil.Skipped) != 1 || hasil.Skipped[0].Reason != cart.ReasonUnavailable {
		t.Fatalf("alasan seharusnya UNAVAILABLE: %+v", hasil.Skipped)
	}
}

// TestPesanUlang_SemuaProdukDihentikanGagal menjaga agar keranjang kosong
// tanpa penjelasan tidak pernah terjadi. Pelanggan akan mengira sesuatu rusak.
func TestPesanUlang_SemuaProdukDihentikanGagal(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 2)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	nonaktif := false
	if _, err := l.produk.Update(ctx, balok.ID,
		produkInput(balok, balok.BasePriceCents, &nonaktif)); err != nil {
		t.Fatalf("menonaktifkan produk: %v", err)
	}

	hasil, err := l.pesanan.Reorder(ctx, p.ID, lama.ID)
	if !errors.Is(err, order.ErrItemsUnavailable) {
		t.Fatalf("galat %v, seharusnya ErrItemsUnavailable", err)
	}
	// Daftar kegagalan tetap diberikan agar dapat dijelaskan kepada pelanggan.
	if hasil == nil || len(hasil.Skipped) != 1 {
		t.Fatalf("daftar item yang gagal seharusnya tetap diberikan: %+v", hasil)
	}
}

// TestPesanUlang_MenggantiIsiKeranjangBukanMenambah menjaga arti "membuat
// keranjang baru" pada SRS-ORD-005.
func TestPesanUlang_MenggantiIsiKeranjangBukanMenambah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	kristal := l.buatProduk(t, "Es Kristal", 1500000, 1)
	l.isiKeranjang(t, p, balok, 2)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	// Pelanggan sudah menaruh sesuatu yang lain di keranjangnya.
	l.isiKeranjang(t, p, kristal, 5)

	hasil, err := l.pesanan.Reorder(ctx, p.ID, lama.ID)
	if err != nil {
		t.Fatalf("pesan ulang: %v", err)
	}
	if len(hasil.Cart.Items) != 1 || hasil.Cart.Items[0].ProductID != balok.ID {
		t.Fatalf("keranjang seharusnya diganti isi pesanan lama: %+v", hasil.Cart.Items)
	}
}

// TestPesanUlang_PesananPelangganLainDitolak menjaga agar isi pesanan orang
// lain tidak dapat disalin ke keranjang sendiri.
func TestPesanUlang_PesananPelangganLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	pemilik := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	penyusup := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, pemilik, balok, 1)

	lama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: pemilik.ID, AddressID: pemilik.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	if _, err := l.pesanan.Reorder(ctx, penyusup.ID, lama.ID); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
}

func TestPesanUlang_PesananTidakAda(t *testing.T) {
	l := siapkan(t)
	w := l.buatWilayah(t)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)

	if _, err := l.pesanan.Reorder(context.Background(), p.ID, uuid.New()); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
}
