package customer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
)

// TestAlamat_PelangganLainTidakDapatMembaca adalah uji keamanan, bukan uji
// fungsi. Bila kepemilikan alamat tidak dijaga, seorang pelanggan dapat
// mengirim pesanan ke alamat orang lain, atau sekadar memastikan alamat
// tertentu ada dengan mencoba pengenalnya.
func TestAlamat_PelangganLainTidakDapatMembaca(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)

	pemilik := buatPelanggan(t, c, "Pemilik", customer.TypeRetail)
	penyusup := buatPelanggan(t, c, "Penyusup", customer.TypeRetail)

	alamat, err := c.AddAddress(ctx, pemilik.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}

	// Pemilik dapat membacanya.
	if _, err := c.GetAddress(ctx, pemilik.ID, alamat.ID); err != nil {
		t.Fatalf("pemilik seharusnya dapat membaca alamatnya: %v", err)
	}

	// Pelanggan lain tidak, dan galatnya tidak membedakan "ada tapi bukan
	// milikmu" dari "tidak ada".
	_, err = c.GetAddress(ctx, penyusup.ID, alamat.ID)
	if !errors.Is(err, customer.ErrAddressNotFound) {
		t.Fatalf("pelanggan lain seharusnya ErrAddressNotFound, dapat %v", err)
	}

	_, err = c.ResolveForDelivery(ctx, penyusup.ID, alamat.ID)
	if !errors.Is(err, customer.ErrAddressNotFound) {
		t.Fatalf("pengiriman ke alamat orang lain seharusnya ditolak, dapat %v", err)
	}
}

// TestAlamat_PertamaOtomatisUtama menjaga agar pelanggan tidak pernah punya
// alamat tanpa satu pun yang utama, sebab checkout memakainya sebagai bawaan.
func TestAlamat_PertamaOtomatisUtama(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	pertama, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat pertama: %v", err)
	}
	if !pertama.IsPrimary {
		t.Fatal("alamat pertama seharusnya otomatis menjadi utama")
	}

	kedua, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Kantor"))
	if err != nil {
		t.Fatalf("menambah alamat kedua: %v", err)
	}
	if kedua.IsPrimary {
		t.Fatal("alamat kedua seharusnya tidak otomatis menjadi utama")
	}
}

// TestAlamat_PindahUtamaTidakMelanggarKeunikan menjaga alasan penandaan lama
// dilepas dalam pernyataan tersendiri. Satu UPDATE yang menukar penanda dapat
// melanggar indeks keunikan di tengah jalan walau keadaan akhirnya sah.
func TestAlamat_PindahUtamaTidakMelanggarKeunikan(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	rumah, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat rumah: %v", err)
	}
	kantor, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Kantor"))
	if err != nil {
		t.Fatalf("menambah alamat kantor: %v", err)
	}

	if err := c.SetPrimaryAddress(ctx, cust.ID, kantor.ID); err != nil {
		t.Fatalf("memindahkan alamat utama: %v", err)
	}

	daftar, err := c.ListAddresses(ctx, cust.ID)
	if err != nil {
		t.Fatalf("membaca daftar alamat: %v", err)
	}
	var jumlahUtama int
	for _, a := range daftar {
		if a.IsPrimary {
			jumlahUtama++
			if a.ID != kantor.ID {
				t.Fatalf("alamat utama %s, seharusnya kantor", a.Label)
			}
		}
	}
	if jumlahUtama != 1 {
		t.Fatalf("alamat utama ada %d, seharusnya tepat 1", jumlahUtama)
	}
	// Alamat utama harus terurut paling depan agar checkout dapat memakai
	// elemen pertama sebagai bawaan.
	if daftar[0].ID != kantor.ID {
		t.Fatal("alamat utama seharusnya terurut paling depan")
	}
	_ = rumah
}

// TestAlamat_PelangganLainTidakDapatMemindahkanUtama menjaga kepemilikan pada
// jalur pengubahan, bukan hanya pembacaan.
func TestAlamat_PelangganLainTidakDapatMemindahkanUtama(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)

	pemilik := buatPelanggan(t, c, "Pemilik", customer.TypeRetail)
	penyusup := buatPelanggan(t, c, "Penyusup", customer.TypeRetail)
	alamat, err := c.AddAddress(ctx, pemilik.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}

	if err := c.SetPrimaryAddress(ctx, penyusup.ID, alamat.ID); !errors.Is(err, customer.ErrAddressNotFound) {
		t.Fatalf("seharusnya ditolak, dapat %v", err)
	}
	if err := c.DeactivateAddress(ctx, penyusup.ID, alamat.ID); !errors.Is(err, customer.ErrAddressNotFound) {
		t.Fatalf("penonaktifan oleh orang lain seharusnya ditolak, dapat %v", err)
	}
}

// TestAlamat_UtamaBerpindahSaatDinonaktifkan menjaga agar pelanggan tidak
// kehilangan alamat bawaan ketika alamat utamanya dinonaktifkan.
func TestAlamat_UtamaBerpindahSaatDinonaktifkan(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	rumah, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat rumah: %v", err)
	}
	if _, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Kantor")); err != nil {
		t.Fatalf("menambah alamat kantor: %v", err)
	}

	if err := c.DeactivateAddress(ctx, cust.ID, rumah.ID); err != nil {
		t.Fatalf("menonaktifkan alamat utama: %v", err)
	}

	daftar, err := c.ListAddresses(ctx, cust.ID)
	if err != nil {
		t.Fatalf("membaca daftar alamat: %v", err)
	}
	if len(daftar) != 1 {
		t.Fatalf("alamat aktif tersisa %d, seharusnya 1", len(daftar))
	}
	if !daftar[0].IsPrimary {
		t.Fatal("alamat yang tersisa seharusnya menjadi utama")
	}
}

// TestAlamat_WilayahTidakAktifDitolak menjaga agar alamat tidak bergantung
// pada wilayah yang sudah berhenti dilayani.
func TestAlamat_WilayahTidakAktifDitolak(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	if _, err := pool.Exec(ctx,
		`UPDATE service_areas SET is_active = false WHERE id = $1`, areaID); err != nil {
		t.Fatalf("menonaktifkan wilayah: %v", err)
	}

	_, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Rumah"))
	if !errors.Is(err, customer.ErrAreaInactive) {
		t.Fatalf("wilayah tidak aktif seharusnya ditolak, dapat %v", err)
	}
}

// TestAlamat_WilayahNonaktifSesudahnyaMenolakPengiriman menjaga agar alamat
// yang dibuat saat wilayahnya masih aktif tetap diperiksa ulang saat checkout.
// Wilayah dapat berhenti dilayani setelah alamat tersimpan.
func TestAlamat_WilayahNonaktifSesudahnyaMenolakPengiriman(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	alamat, err := c.AddAddress(ctx, cust.ID, alamatBaku(areaID, "Rumah"))
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}

	// Saat dibuat wilayahnya aktif, jadi ongkos kirim terbaca.
	kirim, err := c.ResolveForDelivery(ctx, cust.ID, alamat.ID)
	if err != nil {
		t.Fatalf("membaca alamat pengiriman: %v", err)
	}
	if kirim.DeliveryFeeCents != 15000 {
		t.Fatalf("ongkos kirim %d, seharusnya 15000", kirim.DeliveryFeeCents)
	}

	// Depo berhenti melayani sesudahnya.
	if _, err := pool.Exec(ctx, `
		UPDATE depots SET is_active = false
		WHERE  id = (SELECT depot_id FROM service_areas WHERE id = $1)`, areaID); err != nil {
		t.Fatalf("menonaktifkan depo: %v", err)
	}

	if _, err := c.ResolveForDelivery(ctx, cust.ID, alamat.ID); !errors.Is(err, customer.ErrAreaInactive) {
		t.Fatalf("pengiriman ke wilayah tanpa depo aktif seharusnya ditolak, dapat %v", err)
	}
}

func TestAlamat_ValidasiKoordinat(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()
	areaID := buatWilayah(t, pool)
	cust := buatPelanggan(t, c, "Pelanggan", customer.TypeRetail)

	in := alamatBaku(areaID, "Rumah")
	in.Latitude = 99
	if _, err := c.AddAddress(ctx, cust.ID, in); !errors.Is(err, customer.ErrCoordRange) {
		t.Fatalf("lintang di luar rentang seharusnya ditolak, dapat %v", err)
	}
}

func TestAlamat_PelangganTidakAda(t *testing.T) {
	c, pool := newCustomers(t)
	areaID := buatWilayah(t, pool)
	_, err := c.AddAddress(context.Background(), uuid.New(), alamatBaku(areaID, "Rumah"))
	if !errors.Is(err, customer.ErrNotFound) {
		t.Fatalf("pelanggan tidak ada seharusnya ErrNotFound, dapat %v", err)
	}
}
