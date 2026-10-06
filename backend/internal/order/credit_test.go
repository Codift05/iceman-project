package order_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
)

// pembeliKontrak membuat pelanggan kontrak bertermin dengan plafon tertentu.
//
// Plafon nol berarti tanpa batas, sesuai nilai bawaan kolomnya.
func (l *lingkungan) pembeliKontrak(t *testing.T, areaID uuid.UUID, plafon int64) pembeli {
	t.Helper()
	ctx := context.Background()
	p := l.buatPembeli(t, areaID, customer.TypeContract)
	if _, err := l.pelanggan.SetContractTerm(ctx, p.ID, customer.TermInput{
		PaymentTermDays: 30, CreditLimitCents: plafon,
	}); err != nil {
		t.Fatalf("menetapkan termin: %v", err)
	}
	return p
}

// TestBatasKredit_PesananMelampauiPlafonDitolak menutup celah yang ditemukan
// saat SRS-ADM-002 dibaca ulang: plafon piutang tersimpan namun tidak pernah
// diperiksa, sehingga pelanggan kontrak dapat memesan tanpa batas.
func TestBatasKredit_PesananMelampauiPlafonDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	// Plafon hanya cukup untuk satu pesanan.
	p := l.pembeliKontrak(t, w.AreaID, 2600000)
	l.isiKeranjang(t, p, balok, 1)

	pertama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama seharusnya diterima: %v", err)
	}
	if pertama.Status != order.StatusProcessing {
		t.Fatalf("status %q, pelanggan bertermin seharusnya PROCESSING", pertama.Status)
	}

	// Pesanan kedua melampaui plafon.
	l.isiKeranjang(t, p, balok, 1)
	_, err = l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if !errors.Is(err, customer.ErrCreditLimit) {
		t.Fatalf("galat %v, seharusnya ErrCreditLimit", err)
	}
	// Pesannya menyebut angkanya, supaya admin dapat menjelaskannya kepada
	// pelanggan tanpa membuka laporan.
	if err != nil && !strings.Contains(err.Error(), "plafon") {
		t.Fatalf("pesan galat seharusnya menyebut plafon: %v", err)
	}

	// Dan kuota slot tidak terambil oleh pesanan yang ditolak.
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}
}

// TestBatasKredit_PlafonNolBerartiTanpaBatas menjaga tafsiran nilai bawaannya.
// Menafsirkannya sebagai nol rupiah akan menolak seluruh pesanan setiap
// pelanggan kontrak yang plafonnya belum diisi.
func TestBatasKredit_PlafonNolBerartiTanpaBatas(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	p := l.pembeliKontrak(t, w.AreaID, 0)

	for i := 0; i < 3; i++ {
		l.isiKeranjang(t, p, balok, 1)
		if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
			CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
		}); err != nil {
			t.Fatalf("pesanan ke-%d seharusnya diterima: %v", i+1, err)
		}
	}
}

// TestBatasKredit_PelangganRitelTidakDiperiksa menjaga agar pemeriksaan hanya
// berlaku bagi penjualan bertermin. Pelanggan ritel membayar di muka, jadi
// pesanannya tidak menambah piutang.
func TestBatasKredit_PelangganRitelTidakDiperiksa(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)

	for i := 0; i < 3; i++ {
		l.isiKeranjang(t, p, balok, 1)
		if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
			CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
		}); err != nil {
			t.Fatalf("pesanan ritel ke-%d seharusnya diterima: %v", i+1, err)
		}
	}
}

// TestBatasKredit_PesananBatalTidakDihitung menjaga definisi piutangnya:
// pesanan yang dibatalkan tidak perlu dibayar, jadi tidak boleh terus menahan
// plafon pelanggan.
func TestBatasKredit_PesananBatalTidakDihitung(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	p := l.pembeliKontrak(t, w.AreaID, 2600000)

	l.isiKeranjang(t, p, balok, 1)
	pertama, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("pesanan pertama: %v", err)
	}

	// Plafonnya penuh: pesanan kedua ditolak.
	l.isiKeranjang(t, p, balok, 1)
	if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	}); !errors.Is(err, customer.ErrCreditLimit) {
		t.Fatalf("seharusnya ditolak, dapat %v", err)
	}

	// Sesudah pesanan pertama dibatalkan, plafonnya kembali.
	if _, err := l.pesanan.Cancel(ctx, pertama.ID, "pelanggan berubah pikiran"); err != nil {
		t.Fatalf("membatalkan: %v", err)
	}
	if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	}); err != nil {
		t.Fatalf("sesudah pembatalan seharusnya diterima: %v", err)
	}
}

// TestBatasKredit_BersamaanTidakMelampauiPlafon adalah uji konkurensi yang
// menjaga penguncian baris termin. Tanpa kunci, dua pesanan bersamaan dapat
// sama sama membaca piutang yang sama lalu keduanya merasa cukup, dan
// piutangnya melampaui plafon.
func TestBatasKredit_BersamaanTidakMelampauiPlafon(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 20)
	balok := l.buatProduk(t, "Es Balok", 1000000, 1)

	// Plafon cukup untuk dua pesanan, dengan delapan percobaan bersamaan.
	// Harga satuan sejuta ditambah ongkos kirim lima belas ribu, jadi satu
	// pesanan bernilai 1.015.000 sen.
	const satuPesanan = 1015000
	p := l.pembeliKontrak(t, w.AreaID, satuPesanan*2)

	const penyerbu = 8
	// Keranjang diisi lebih dahulu sebanyak percobaan, karena checkout
	// mengosongkannya. Tiap percobaan memakai keranjang yang sama, sehingga
	// yang kalah pada plafon akan menemukan keranjangnya sudah kosong; itu
	// tidak mengapa, yang diuji adalah jumlah yang lolos.
	l.isiKeranjang(t, p, balok, 1)

	var lolos, ditolakPlafon, lain int32
	mulai := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-mulai
			_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
			})
			switch {
			case err == nil:
				atomic.AddInt32(&lolos, 1)
			case errors.Is(err, customer.ErrCreditLimit):
				atomic.AddInt32(&ditolakPlafon, 1)
			default:
				atomic.AddInt32(&lain, 1)
			}
		}()
	}
	close(mulai)
	wg.Wait()

	// Yang penting bukan berapa yang lolos, melainkan piutangnya tidak
	// melampaui plafon. Keranjang yang dipakai bersama membuat sebagian
	// percobaan gagal karena keranjang kosong, dan itu bukan yang diuji.
	st, err := l.pelanggan.Credit(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca keadaan kredit: %v", err)
	}
	if st.OutstandingCents > st.LimitCents {
		t.Fatalf("piutang %d sen melampaui plafon %d sen (lolos=%d ditolak=%d lain=%d)",
			st.OutstandingCents, st.LimitCents, lolos, ditolakPlafon, lain)
	}
	if lolos == 0 {
		t.Fatalf("tidak ada pesanan yang lolos (ditolak=%d lain=%d)", ditolakPlafon, lain)
	}
}

// TestKeadaanKredit_DibacaSebelumMemesan menjaga agar petugas dapat melihat
// sisa plafon sebelum menerima pesanan lewat telepon, bukan menunggu
// penolakan saat menyimpannya.
func TestKeadaanKredit_DibacaSebelumMemesan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	p := l.pembeliKontrak(t, w.AreaID, 5000000)

	awal, err := l.pelanggan.Credit(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca keadaan kredit: %v", err)
	}
	if awal.Unlimited {
		t.Fatal("pelanggan berplafon seharusnya tidak tanpa batas")
	}
	if awal.OutstandingCents != 0 || awal.AvailableCents != 5000000 {
		t.Fatalf("keadaan awal salah: %+v", awal)
	}

	l.isiKeranjang(t, p, balok, 1)
	o, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	sesudah, err := l.pelanggan.Credit(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca keadaan kredit: %v", err)
	}
	if sesudah.OutstandingCents != o.TotalCents {
		t.Fatalf("piutang %d, seharusnya %d", sesudah.OutstandingCents, o.TotalCents)
	}
	if sesudah.AvailableCents != 5000000-o.TotalCents {
		t.Fatalf("sisa plafon %d, seharusnya %d", sesudah.AvailableCents, 5000000-o.TotalCents)
	}
}

// TestKeadaanKredit_SisaTidakNegatif menjaga agar sisa plafon yang sudah
// terlampaui dilaporkan nol, bukan negatif. Sisa negatif tidak berarti apa pun
// bagi yang membacanya.
func TestKeadaanKredit_SisaTidakNegatif(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	p := l.pembeliKontrak(t, w.AreaID, 2600000)

	l.isiKeranjang(t, p, balok, 1)
	if _, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
	}); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	// Plafon diturunkan di bawah piutang yang sudah ada.
	if _, err := l.pelanggan.SetContractTerm(ctx, p.ID, customer.TermInput{
		PaymentTermDays: 30, CreditLimitCents: 1000000,
	}); err != nil {
		t.Fatalf("menurunkan plafon: %v", err)
	}

	st, err := l.pelanggan.Credit(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca keadaan kredit: %v", err)
	}
	if st.AvailableCents != 0 {
		t.Fatalf("sisa plafon %d, seharusnya 0 bukan negatif", st.AvailableCents)
	}
}

// Gagal membaca termin kontrak harus menggagalkan pemeriksaan, bukan dianggap
// "pelanggan ini tanpa termin".
//
// Keduanya pernah disamakan: galat apa pun dari query termin dibalas nil,
// sehingga satu gangguan sesaat pada basis data mematikan kendali plafon dan
// pesanan di atas batas lewat tanpa meninggalkan jejak. Transaksi yang sudah
// ditutup dipakai di sini sebagai pengganti gangguan itu, karena pgx
// membalasnya dengan galat yang bukan ErrNoRows.
func TestBatasKredit_GalatBacaTerminTidakDianggapTanpaTermin(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	p := l.pembeliKontrak(t, w.AreaID, 1000000)

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("membuka transaksi: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("menutup transaksi: %v", err)
	}

	err = l.pelanggan.CheckCreditTx(ctx, tx, p.ID, 1)
	if err == nil {
		t.Fatal("pemeriksaan plafon seharusnya gagal saat termin tidak terbaca, dapat nil")
	}
	// Galatnya bukan penolakan plafon, melainkan kegagalan membaca: keduanya
	// tidak boleh tertukar, agar 422 tidak muncul untuk gangguan teknis.
	if errors.Is(err, customer.ErrCreditLimit) {
		t.Fatalf("galat teknis tidak boleh jadi penolakan plafon: %v", err)
	}
	if !strings.Contains(err.Error(), "termin") {
		t.Fatalf("galat seharusnya menyebut pembacaan termin, dapat: %v", err)
	}
}
