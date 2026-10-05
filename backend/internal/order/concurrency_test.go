package order_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/scheduling"
)

// TestCheckout_DuaBersamaanKuotaTerakhirSatuBerhasil adalah uji inti domain
// pesanan, bukan pelengkap.
//
// SRS-ORD-002 menjanjikan: dua permintaan bersamaan untuk kuota terakhir
// menghasilkan tepat satu pesanan. Tanpa SELECT FOR UPDATE pada baris slot,
// keduanya membaca kuota tersisa yang sama lalu keduanya merasa berhak.
func TestCheckout_DuaBersamaanKuotaTerakhirSatuBerhasil(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 1)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)

	// Dua pelanggan berbeda, masing masing dengan keranjangnya sendiri.
	satu := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	dua := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	l.isiKeranjang(t, satu, balok, 1)
	l.isiKeranjang(t, dua, balok, 1)

	var berhasil, gagal int32
	mulai := make(chan struct{})
	var wg sync.WaitGroup

	for _, p := range []pembeli{satu, dua} {
		wg.Add(1)
		go func(p pembeli) {
			defer wg.Done()
			<-mulai
			_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
			})
			if err == nil {
				atomic.AddInt32(&berhasil, 1)
			} else {
				atomic.AddInt32(&gagal, 1)
			}
		}(p)
	}
	close(mulai)
	wg.Wait()

	if berhasil != 1 {
		t.Fatalf("pesanan berhasil %d, seharusnya tepat 1", berhasil)
	}
	if gagal != 1 {
		t.Fatalf("pesanan gagal %d, seharusnya tepat 1", gagal)
	}
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}
	if n := l.jumlahPesanan(t, slot); n != 1 {
		t.Fatalf("pesanan tersimpan %d, seharusnya 1", n)
	}
}

// TestCheckout_DuaPuluhBersamaanKuotaLimaLimaBerhasil memperbesar skalanya,
// dan sekaligus menjaga hal yang lebih halus daripada jumlah akhir.
//
// Jumlah akhir saja tidak membuktikan penguncian baris bekerja. Kekangan DB-01
// menolak kuota yang melampaui kapasitas, sehingga tanpa SELECT FOR UPDATE pun
// hanya lima pesanan yang terbentuk: sisanya gagal pada tahap commit karena
// melanggar kekangan. Yang membedakan keduanya adalah bentuk galatnya.
//
// Karena itu uji ini mewajibkan setiap kegagalan berupa ErrSlotFull, yaitu
// galat yang dapat diterjemahkan menjadi "Slot penuh, pilih waktu lain".
// Pelanggaran kekangan hanya dapat diterjemahkan menjadi "Terjadi gangguan",
// yang tidak memberi tahu pelanggan apa yang harus dilakukannya.
func TestCheckout_DuaPuluhBersamaanKuotaLimaLimaBerhasil(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	const kapasitas = 5
	const penyerbu = 20
	slot := l.buatSlot(t, w.AreaID, kapasitas)
	balok := l.buatProduk(t, "Es Balok", 1000000, 1)

	pembelis := make([]pembeli, penyerbu)
	for i := range pembelis {
		pembelis[i] = l.buatPembeli(t, w.AreaID, customer.TypeRetail)
		l.isiKeranjang(t, pembelis[i], balok, 1)
	}

	var (
		mu       sync.Mutex
		berhasil int
		galat    []error
	)
	mulai := make(chan struct{})
	var wg sync.WaitGroup
	for _, p := range pembelis {
		wg.Add(1)
		go func(p pembeli) {
			defer wg.Done()
			<-mulai
			_, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				galat = append(galat, err)
				return
			}
			berhasil++
		}(p)
	}
	close(mulai)
	wg.Wait()

	if berhasil != kapasitas {
		t.Fatalf("pesanan berhasil %d, seharusnya %d", berhasil, kapasitas)
	}
	if n := l.kuotaTerpakai(t, slot); n != kapasitas {
		t.Fatalf("kuota terpakai %d, seharusnya %d", n, kapasitas)
	}
	if n := l.jumlahPesanan(t, slot); n != kapasitas {
		t.Fatalf("pesanan tersimpan %d, seharusnya %d", n, kapasitas)
	}

	// Inilah bagian yang membuktikan penguncian baris, bukan hanya kekangan.
	if len(galat) != penyerbu-kapasitas {
		t.Fatalf("kegagalan %d, seharusnya %d", len(galat), penyerbu-kapasitas)
	}
	for _, err := range galat {
		if !errors.Is(err, scheduling.ErrSlotFull) {
			t.Fatalf("kegagalan seharusnya ErrSlotFull agar dapat dijelaskan "+
				"kepada pelanggan, dapat: %v", err)
		}
	}
}

// TestCheckout_KuotaDanPesananSelaluSamaBanyak menjaga janji yang paling
// penting: tidak ada kuota yang terambil tanpa pesanan, dan tidak ada pesanan
// tanpa kuota.
//
// Beberapa slot dipakai sekaligus agar penguncian baris tidak dapat lolos
// hanya karena seluruh permintaan menunggu di satu baris yang sama.
func TestCheckout_KuotaDanPesananSelaluSamaBanyak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	balok := l.buatProduk(t, "Es Balok", 1000000, 1)

	const jumlahSlot = 4
	const kapasitas = 3
	const penyerbuPerSlot = 10

	jam := []string{"08:00", "11:00", "14:00", "17:00"}
	slots := make([]uuid.UUID, jumlahSlot)
	for i := range slots {
		slots[i] = l.buatSlotKhusus(t, w.AreaID, kapasitas, 0,
			timeDepan(), false, jam[i])
	}

	var wg sync.WaitGroup
	mulai := make(chan struct{})
	for i := 0; i < jumlahSlot*penyerbuPerSlot; i++ {
		p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
		l.isiKeranjang(t, p, balok, 1)
		slot := slots[i%jumlahSlot]

		wg.Add(1)
		go func(p pembeli, slot uuid.UUID) {
			defer wg.Done()
			<-mulai
			_, _ = l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
			})
		}(p, slot)
	}
	close(mulai)
	wg.Wait()

	totalKuota, totalPesanan := int32(0), 0
	for _, slot := range slots {
		kuota := l.kuotaTerpakai(t, slot)
		pesanan := l.jumlahPesanan(t, slot)
		if int(kuota) != pesanan {
			t.Fatalf("slot %s: kuota terpakai %d tetapi pesanan %d", slot, kuota, pesanan)
		}
		if kuota != kapasitas {
			t.Fatalf("slot %s: kuota terpakai %d, seharusnya %d", slot, kuota, kapasitas)
		}
		totalKuota += kuota
		totalPesanan += pesanan
	}
	if totalKuota != jumlahSlot*kapasitas || totalPesanan != jumlahSlot*kapasitas {
		t.Fatalf("total kuota %d dan pesanan %d, seharusnya %d",
			totalKuota, totalPesanan, jumlahSlot*kapasitas)
	}
}

// TestCheckout_KunciIdempotensiBersamaanSatuPesanan menjaga idempotensi pada
// keadaan yang justru paling sering terjadi: klien mengirim ulang permintaan
// sebelum yang pertama selesai, karena sambungannya tampak menggantung.
//
// Pemeriksaan kunci di awal Checkout tidak cukup untuk keadaan ini, karena
// kedua permintaan melewatinya sebelum salah satu menyimpan pesanan. Yang
// menjaganya adalah indeks keunikan pada basis data, dan uji ini memastikan
// jalur itu benar benar ada.
func TestCheckout_KunciIdempotensiBersamaanSatuPesanan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	w := l.buatWilayah(t)
	slot := l.buatSlot(t, w.AreaID, 10)
	p := l.buatPembeli(t, w.AreaID, customer.TypeRetail)
	balok := l.buatProduk(t, "Es Balok", 2500000, 1)
	l.isiKeranjang(t, p, balok, 1)

	kunci := uuid.NewString()
	const penyerbu = 6

	var (
		mu     sync.Mutex
		idUnik = map[uuid.UUID]int{}
		galat  []error
	)
	mulai := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-mulai
			got, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
				CustomerID: p.ID, AddressID: p.AddressID, SlotID: slot,
				IdempotencyKey: kunci,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				galat = append(galat, err)
				return
			}
			idUnik[got.ID]++
		}()
	}
	close(mulai)
	wg.Wait()

	// Seluruh permintaan harus dijawab berhasil. Dari sisi klien, permintaan
	// kembar memang berhasil: pesanannya ada. Menjawabnya dengan galat basis
	// data membuat klien mengira pesanannya gagal lalu mencoba lagi.
	if len(galat) != 0 {
		t.Fatalf("%d dari %d permintaan gagal, seharusnya semuanya berhasil: %v",
			len(galat), penyerbu, galat[0])
	}
	// Dan semuanya harus menunjuk pesanan yang sama.
	if len(idUnik) != 1 {
		t.Fatalf("pesanan berbeda yang dikembalikan ada %d, seharusnya 1: %v",
			len(idUnik), idUnik)
	}
	if n := l.jumlahPesanan(t, slot); n != 1 {
		t.Fatalf("pesanan terbentuk %d, seharusnya 1", n)
	}
	if n := l.kuotaTerpakai(t, slot); n != 1 {
		t.Fatalf("kuota terpakai %d, seharusnya 1", n)
	}
}
