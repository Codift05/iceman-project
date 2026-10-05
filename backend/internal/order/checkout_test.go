package order_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/scheduling"
)

func TestCheckout_PesananTersimpanLengkap(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 3)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
		Notes: "tolong pagi",
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if got.OrderNo == "" {
		t.Fatal("nomor pesanan kosong")
	}
	if got.SubtotalCents != 7500000 {
		t.Fatalf("subtotal %d, seharusnya 7500000", got.SubtotalCents)
	}
	if got.DeliveryFeeCents != 15000 {
		t.Fatalf("ongkos kirim %d, seharusnya 15000", got.DeliveryFeeCents)
	}
	if got.TotalCents != 7515000 {
		t.Fatalf("total %d, seharusnya 7515000", got.TotalCents)
	}
	if len(got.Items) != 1 || got.Items[0].Qty != 3 {
		t.Fatalf("isi pesanan salah: %+v", got.Items)
	}

	// Kuota slot berkurang tepat satu.
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}

	// Keranjang dikosongkan.
	k, err := l.keranjang.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca keranjang: %v", err)
	}
	if len(k.Items) != 0 {
		t.Fatalf("keranjang masih memuat %d item sesudah checkout", len(k.Items))
	}

	// Riwayat status mencatat status awal.
	riwayat, err := l.pesanan.History(ctx, got.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	if len(riwayat) != 1 || riwayat[0].ToStatus != order.StatusWaitingPayment {
		t.Fatalf("riwayat status salah: %+v", riwayat)
	}
	if riwayat[0].FromStatus != nil {
		t.Fatal("status awal seharusnya tanpa status asal")
	}
}

// TestCheckout_SalinanHargaTidakIkutBerubah menjaga BR-007. Pesanan menyimpan
// salinan harga, sehingga mengubah harga produk sesudahnya tidak mengubah
// tagihan yang sudah disetujui pelanggan.
func TestCheckout_SalinanHargaTidakIkutBerubah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 2)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	// Admin menaikkan harga dan menonaktifkan produknya sama sekali.
	nonaktif := false
	if _, err := l.produk.Update(ctx, balok.ID, produkInput(balok, 9999999, &nonaktif)); err != nil {
		t.Fatalf("mengubah produk: %v", err)
	}

	lagi, err := l.pesanan.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("membaca pesanan: %v", err)
	}
	if lagi.Items[0].UnitPriceCents != 2500000 {
		t.Fatalf("harga pada pesanan berubah menjadi %d", lagi.Items[0].UnitPriceCents)
	}
	if lagi.TotalCents != 5015000 {
		t.Fatalf("total pesanan berubah menjadi %d", lagi.TotalCents)
	}
	if lagi.Items[0].Name != "Es Balok" {
		t.Fatalf("nama produk pada pesanan hilang: %q", lagi.Items[0].Name)
	}
}

// TestCheckout_PelangganKontrakLangsungDiproses menjaga BR-002.
func TestCheckout_PelangganKontrakLangsungDiproses(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeContract)
	if _, err := l.pelanggan.SetContractTerm(ctx, p.ID, customer.TermInput{
		PaymentTermDays: 30,
	}); err != nil {
		t.Fatalf("menetapkan termin: %v", err)
	}
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got.Status != order.StatusProcessing {
		t.Fatalf("status %q, seharusnya PROCESSING", got.Status)
	}
	if got.PaymentTermDays == nil || *got.PaymentTermDays != 30 {
		t.Fatalf("termin pada pesanan salah: %+v", got.PaymentTermDays)
	}
	// Pesanan bertermin tidak perlu tagihan di muka.
	if n := l.jumlahJobTagihan(t, got.ID); n != 0 {
		t.Fatalf("job tagihan %d, seharusnya 0 untuk pesanan bertermin", n)
	}
}

// TestCheckout_PelangganRitelMenungguPembayaranDanTagihanDiantre menjaga sisi
// sebaliknya, termasuk janji SRS-ORD-002 bahwa pembuatan tagihan diantre dalam
// transaksi yang sama.
func TestCheckout_PelangganRitelMenungguPembayaranDanTagihanDiantre(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got.Status != order.StatusWaitingPayment {
		t.Fatalf("status %q, seharusnya WAITING_PAYMENT", got.Status)
	}
	if got.PaymentTermDays != nil {
		t.Fatal("pesanan ritel seharusnya tanpa termin")
	}
	if n := l.jumlahJobTagihan(t, got.ID); n != 1 {
		t.Fatalf("job tagihan %d, seharusnya 1", n)
	}
}

// TestCheckout_KontrakTanpaTerminTetapBayarDiMuka menjaga agar penandaan jenis
// pelanggan saja tidak cukup untuk melewati pembayaran.
func TestCheckout_KontrakTanpaTerminTetapBayarDiMuka(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeContract)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got.Status != order.StatusWaitingPayment {
		t.Fatalf("status %q, seharusnya WAITING_PAYMENT", got.Status)
	}
}

// TestCheckout_KunciIdempotensiMenghasilkanSatuPesanan menjaga SRS-ORD-002.
// Jaringan seluler sering memutus sambungan sesudah permintaan terkirim,
// sehingga klien mengirim ulang permintaan yang sebenarnya sudah berhasil.
func TestCheckout_KunciIdempotensiMenghasilkanSatuPesanan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 2)

	kunci := uuid.NewString()
	in := order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
		IdempotencyKey: kunci,
	}

	pertama, err := l.pesanan.Checkout(ctx, in)
	if err != nil {
		t.Fatalf("checkout pertama: %v", err)
	}
	kedua, err := l.pesanan.Checkout(ctx, in)
	if err != nil {
		t.Fatalf("checkout kedua dengan kunci sama: %v", err)
	}

	if pertama.ID != kedua.ID {
		t.Fatalf("dua pesanan berbeda dibuat: %s dan %s", pertama.ID, kedua.ID)
	}
	if n := l.jumlahPesanan(t, slot); n != 1 {
		t.Fatalf("pesanan pada slot %d, seharusnya 1", n)
	}
	// Yang terpenting: kuota tidak terambil dua kali.
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}
}

// TestCheckout_PenolakanTidakMenyisakanKuotaDanKeranjang menjaga sifat
// transaksional. Pesanan yang gagal tidak boleh menyisakan kuota terpakai yang
// bertambah, maupun keranjang yang sudah terkosongkan.
func TestCheckout_PenolakanTidakMenyisakanKuotaDanKeranjang(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	kasus := []struct {
		nama string
		slot func() uuid.UUID
		mau  error
	}{
		{"slot penuh", func() uuid.UUID {
			return l.buatSlotKhusus(t, w.AreaID, 1, 1, time.Now().Add(20*time.Hour), false, "09:00")
		}, scheduling.ErrSlotFull},
		{"batas pesan terlewat", func() uuid.UUID {
			return l.buatSlotKhusus(t, w.AreaID, 10, 0, time.Now().Add(-time.Hour), false, "10:00")
		}, scheduling.ErrSlotCutoffPassed},
		{"hari libur", func() uuid.UUID {
			return l.buatSlotKhusus(t, w.AreaID, 10, 0, time.Now().Add(20*time.Hour), true, "11:00")
		}, scheduling.ErrSlotUnavailable},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			slot := k.slot()
			sebelum := l.kuotaTerpakai(t, slot)
			l.isiKeranjang(t, p, balok, 1)

			_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
			})
			if !errors.Is(err, k.mau) {
				t.Fatalf("galat %v, seharusnya %v", err, k.mau)
			}

			if n := l.kuotaTerpakai(t, slot); n != sebelum {
				t.Fatalf("kuota terpakai berubah dari %d menjadi %d walau checkout gagal",
					sebelum, n)
			}
			// Keranjang harus tetap utuh agar pelanggan dapat mencoba slot lain.
			k2, err := l.keranjang.Get(ctx, p.ID)
			if err != nil {
				t.Fatalf("membaca keranjang: %v", err)
			}
			if len(k2.Items) == 0 {
				t.Fatal("keranjang terkosongkan padahal checkout gagal")
			}
			if n := l.jumlahPesanan(t, slot); n != 0 {
				t.Fatalf("pesanan tersimpan %d padahal checkout gagal", n)
			}
		})
	}
}

// TestCheckout_SlotWilayahLainDitolak menjaga agar pelanggan tidak dapat
// mengambil kuota slot milik wilayah lain. Deponya akan kebagian pesanan yang
// tidak dapat diantarnya.
func TestCheckout_SlotWilayahLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	wA := l.buatWilayah(t)
	wB := l.buatWilayah(t)
	slotB := l.buatSlot(t, wB.AreaID, 10)
	p := l.buatPembeli(t, wA.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slotB,
	})
	if !errors.Is(err, order.ErrSlotAreaMismatch) {
		t.Fatalf("galat %v, seharusnya ErrSlotAreaMismatch", err)
	}
	if n := l.kuotaTerpakai(t, slotB); n != 0 {
		t.Fatalf("kuota slot wilayah lain terambil: %d", n)
	}
}

// TestCheckout_AlamatPelangganLainDitolak menjaga kepemilikan alamat pada jalur
// checkout, bukan hanya pada pembacaan alamat.
func TestCheckout_AlamatPelangganLainDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	pemilik := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	penyusup := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, penyusup, balok, 1)

	_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: penyusup.ID, AddressID: pemilik.AddressID, SlotID: slot,
	})
	if !errors.Is(err, customer.ErrAddressNotFound) {
		t.Fatalf("galat %v, seharusnya ErrAddressNotFound", err)
	}
}

// TestCheckout_ItemTerhalangMenghentikanCheckout menjaga agar item yang gagal
// tidak dibuang diam diam. Pelanggan harus memutuskan sendiri apakah pesanan
// tanpa item itu masih sesuai keinginannya.
func TestCheckout_ItemTerhalangMenghentikanCheckout(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	kristal := l.buatProduk(t, "Es Kristal", 1500000, 1)
	l.isiKeranjang(t, p, balok, 2)
	l.isiKeranjang(t, p, kristal, 1)

	// Kristal dinonaktifkan setelah masuk keranjang.
	nonaktif := false
	if _, err := l.produk.Update(ctx, kristal.ID,
		produkInput(kristal, kristal.BasePriceCents, &nonaktif)); err != nil {
		t.Fatalf("menonaktifkan produk: %v", err)
	}

	_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if !errors.Is(err, order.ErrItemsUnavailable) {
		t.Fatalf("galat %v, seharusnya ErrItemsUnavailable", err)
	}
	if n := l.kuotaTerpakai(t, slot); n != 0 {
		t.Fatalf("kuota terambil %d padahal checkout gagal", n)
	}
}

func TestCheckout_KeranjangKosongDitolak(t *testing.T) {
	l := siapkan(t)
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)

	_, err := l.pesanan.Checkout(context.Background(), order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if !errors.Is(err, order.ErrCartEmpty) {
		t.Fatalf("galat %v, seharusnya ErrCartEmpty", err)
	}
}

func TestCheckout_SlotTidakAdaDitolak(t *testing.T) {
	l := siapkan(t)
	w := l.buatWilayah(t)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	_, err := l.pesanan.Checkout(context.Background(), order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: uuid.New(),
	})
	if !errors.Is(err, scheduling.ErrSlotNotFound) {
		t.Fatalf("galat %v, seharusnya ErrSlotNotFound", err)
	}
}

// TestCheckout_PesananPelangganLainTidakTerbaca menjaga SRS-ORD-005: riwayat
// hanya menampilkan pesanan milik pelanggan yang sedang masuk.
func TestCheckout_PesananPelangganLainTidakTerbaca(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	pemilik := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	penyusup := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, pemilik, balok, 1)

	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: pemilik.ID, AddressID: pemilik.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if _, err := l.pesanan.GetForCustomer(ctx, pemilik.ID, got.ID); err != nil {
		t.Fatalf("pemilik seharusnya dapat membaca pesanannya: %v", err)
	}
	if _, err := l.pesanan.GetForCustomer(ctx, penyusup.ID, got.ID); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("pelanggan lain seharusnya ErrNotFound, dapat %v", err)
	}
}

// TestCheckout_PesananManualDitandaiKanalnya menjaga SRS-ORD-006: aturan sama,
// hanya kanal dan pembuatnya ditandai.
func TestCheckout_PesananManualDitandaiKanalnya(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	admin := l.buatAdmin(t)
	got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
		Channel: order.ChannelWhatsApp, CreatedBy: &admin,
	})
	if err != nil {
		t.Fatalf("checkout manual: %v", err)
	}
	if got.Channel != order.ChannelWhatsApp {
		t.Fatalf("kanal %q, seharusnya WHATSAPP", got.Channel)
	}
	// Kuota slot berkurang sama seperti pesanan dari aplikasi.
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}
}

// TestCheckout_SlotPenuhMenawarkanJadwalTerdekat menjaga janji pada diagram
// alur checkout: penolakan SLOT_FULL disertai tawaran slot terdekat.
//
// Tanpa tawaran itu, satu satunya jalan bagi pelanggan adalah mencoba slot
// satu per satu sampai ada yang berhasil.
func TestCheckout_SlotPenuhMenawarkanJadwalTerdekat(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)

	penuh := l.buatSlotKhusus(t, w.AreaID, 1, 1, timeDepan(), false, "08:00")
	kosong := l.buatSlotKhusus(t, w.AreaID, 5, 0, timeDepan(), false, "11:00")

	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: penuh,
	})
	if !errors.Is(err, scheduling.ErrSlotFull) {
		t.Fatalf("galat %v, seharusnya ErrSlotFull", err)
	}

	tawaran := l.pesanan.NextAvailableFor(ctx, penuh)
	if tawaran == nil {
		t.Fatal("seharusnya ada tawaran jadwal terdekat")
	}
	if tawaran.ID != kosong {
		t.Fatalf("tawaran menunjuk slot %s, seharusnya %s", tawaran.ID, kosong)
	}
	if !tawaran.Selectable {
		t.Fatal("slot yang ditawarkan seharusnya dapat dipilih")
	}
}

// TestCheckout_TanpaJadwalLainTawaranKosong menjaga agar ketiadaan tawaran
// bukan kegagalan. Penolakannya sendiri tetap terkirim.
func TestCheckout_TanpaJadwalLainTawaranKosong(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	penuh := l.buatSlotKhusus(t, w.AreaID, 1, 1, timeDepan(), false, "08:00")

	if tawaran := l.pesanan.NextAvailableFor(ctx, penuh); tawaran != nil {
		t.Fatalf("tidak ada slot lain, tawaran seharusnya kosong: %+v", tawaran)
	}
}
