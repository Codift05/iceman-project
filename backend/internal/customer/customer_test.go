package customer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/customer"
)

func TestPelanggan_BuatDanBaca(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Toko Jaya", customer.TypeContract)
	got, err := c.Get(ctx, cust.ID)
	if err != nil {
		t.Fatalf("membaca pelanggan: %v", err)
	}
	if got.Name != "Toko Jaya" || got.Type != customer.TypeContract || !got.IsActive {
		t.Fatalf("pelanggan terbaca salah: %+v", got)
	}

	lewatTelepon, err := c.ByPhone(ctx, cust.Phone)
	if err != nil {
		t.Fatalf("mencari lewat telepon: %v", err)
	}
	if lewatTelepon.ID != cust.ID {
		t.Fatal("pencarian lewat telepon mengembalikan pelanggan lain")
	}
}

func TestPelanggan_TeleponGandaDitolak(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	nomor := telepon()
	if _, err := c.Create(ctx, customer.Input{Phone: nomor, Name: "Pertama"}); err != nil {
		t.Fatalf("membuat pelanggan pertama: %v", err)
	}
	_, err := c.Create(ctx, customer.Input{Phone: nomor, Name: "Kedua"})
	if !errors.Is(err, customer.ErrPhoneExists) {
		t.Fatalf("telepon ganda seharusnya ditolak, dapat %v", err)
	}
}

func TestPelanggan_ValidasiMasukan(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	if _, err := c.Create(ctx, customer.Input{Name: "Tanpa Telepon"}); !errors.Is(err, customer.ErrPhoneRequired) {
		t.Fatalf("tanpa telepon seharusnya ditolak, dapat %v", err)
	}
	if _, err := c.Create(ctx, customer.Input{Phone: telepon()}); !errors.Is(err, customer.ErrNameRequired) {
		t.Fatalf("tanpa nama seharusnya ditolak, dapat %v", err)
	}
	if _, err := c.Create(ctx, customer.Input{
		Phone: telepon(), Name: "X", Type: "GROSIR",
	}); err == nil {
		t.Fatal("jenis pelanggan yang tidak dikenali seharusnya ditolak")
	}
}

func TestPelanggan_BakuRitel(t *testing.T) {
	c, _ := newCustomers(t)
	cust, err := c.Create(context.Background(), customer.Input{Phone: telepon(), Name: "Tanpa Jenis"})
	if err != nil {
		t.Fatalf("membuat pelanggan: %v", err)
	}
	if cust.Type != customer.TypeRetail {
		t.Fatalf("jenis baku %q, seharusnya RETAIL", cust.Type)
	}
}

// TestPelanggan_PerubahanJenisTercatat menjaga agar perpindahan ritel ke
// kontrak terlacak. Jenis pelanggan menentukan apakah pesanannya perlu dibayar
// di muka, jadi perubahannya berdampak pada uang.
func TestPelanggan_PerubahanJenisTercatat(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Naik Kontrak", customer.TypeRetail)
	if _, err := c.Update(ctx, cust.ID, customer.Input{
		Name: cust.Name, Type: customer.TypeContract,
	}); err != nil {
		t.Fatalf("mengubah jenis pelanggan: %v", err)
	}

	var detail string
	err := pool.QueryRow(ctx, `
		SELECT detail FROM audit_trail
		WHERE  entity = 'customers' AND entity_id = $1 AND action = 'UPDATE'`,
		cust.ID).Scan(&detail)
	if err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if detail != "jenis pelanggan RETAIL menjadi CONTRACT" {
		t.Fatalf("rincian jejak %q tidak menyebut perubahan jenis", detail)
	}
}

func TestPelanggan_TidakAda(t *testing.T) {
	c, _ := newCustomers(t)
	if _, err := c.Get(context.Background(), uuid.New()); !errors.Is(err, customer.ErrNotFound) {
		t.Fatalf("pelanggan tidak ada seharusnya ErrNotFound, dapat %v", err)
	}
}

// --- termin kontrak ---

// TestTermin_PelangganKontrakBertherminBayarBelakangan menjaga BR-002: inilah
// satu satunya keadaan yang membuat pesanan melewati pembayaran di muka.
func TestTermin_PelangganKontrakBerterminBayarBelakangan(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Toko Kontrak", customer.TypeContract)
	if _, err := c.SetContractTerm(ctx, cust.ID, customer.TermInput{
		PaymentTermDays: 30, CreditLimitCents: 50000000,
	}); err != nil {
		t.Fatalf("menetapkan termin: %v", err)
	}

	boleh, term, err := c.PaysOnTerms(ctx, cust.ID)
	if err != nil {
		t.Fatalf("memeriksa termin: %v", err)
	}
	if !boleh {
		t.Fatal("pelanggan kontrak bertermin seharusnya boleh bayar belakangan")
	}
	if term.PaymentTermDays != 30 {
		t.Fatalf("termin %d hari, seharusnya 30", term.PaymentTermDays)
	}
}

// TestTermin_PelangganRitelSelaluBayarDiMuka menjaga sisi sebaliknya.
func TestTermin_PelangganRitelSelaluBayarDiMuka(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Pembeli Ritel", customer.TypeRetail)
	boleh, _, err := c.PaysOnTerms(ctx, cust.ID)
	if err != nil {
		t.Fatalf("memeriksa termin: %v", err)
	}
	if boleh {
		t.Fatal("pelanggan ritel seharusnya tetap bayar di muka")
	}
}

// TestTermin_KontrakTanpaTerminBayarDiMuka menjaga agar penandaan jenis saja
// tidak cukup. Tanpa perjanjian termin, pelanggan kontrak tetap bayar di muka.
func TestTermin_KontrakTanpaTerminBayarDiMuka(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Kontrak Baru", customer.TypeContract)
	boleh, _, err := c.PaysOnTerms(ctx, cust.ID)
	if err != nil {
		t.Fatalf("memeriksa termin: %v", err)
	}
	if boleh {
		t.Fatal("pelanggan kontrak tanpa termin seharusnya bayar di muka")
	}
}

// TestTermin_KedaluwarsaKembaliBayarDiMuka menjaga masa berlaku termin.
// Perjanjian yang habis tidak boleh terus dipakai.
func TestTermin_KedaluwarsaKembaliBayarDiMuka(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Kontrak Habis", customer.TypeContract)
	_, err := pool.Exec(ctx, `
		INSERT INTO contract_terms
		       (customer_id, payment_term_days, valid_from, valid_until)
		VALUES ($1, 30, current_date - 60, current_date - 1)`, cust.ID)
	if err != nil {
		t.Fatalf("menyiapkan termin kedaluwarsa: %v", err)
	}

	boleh, _, err := c.PaysOnTerms(ctx, cust.ID)
	if err != nil {
		t.Fatalf("memeriksa termin: %v", err)
	}
	if boleh {
		t.Fatal("termin yang sudah berakhir seharusnya tidak dipakai lagi")
	}
}

// TestTermin_HanyaSatuAktif menjaga agar penetapan termin baru menonaktifkan
// yang lama, sesuai indeks keunikan pada basis data.
func TestTermin_HanyaSatuAktif(t *testing.T) {
	c, pool := newCustomers(t)
	ctx := context.Background()

	cust := buatPelanggan(t, c, "Toko Kontrak", customer.TypeContract)
	if _, err := c.SetContractTerm(ctx, cust.ID, customer.TermInput{PaymentTermDays: 14}); err != nil {
		t.Fatalf("menetapkan termin pertama: %v", err)
	}
	if _, err := c.SetContractTerm(ctx, cust.ID, customer.TermInput{PaymentTermDays: 30}); err != nil {
		t.Fatalf("menetapkan termin kedua: %v", err)
	}

	var jumlah int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM contract_terms WHERE customer_id = $1 AND is_active`,
		cust.ID).Scan(&jumlah)
	if err != nil {
		t.Fatalf("menghitung termin aktif: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("termin aktif ada %d, seharusnya tepat 1", jumlah)
	}

	term, err := c.ActiveTerm(ctx, cust.ID)
	if err != nil {
		t.Fatalf("membaca termin aktif: %v", err)
	}
	if term.PaymentTermDays != 30 {
		t.Fatalf("termin aktif %d hari, seharusnya yang terbaru yaitu 30", term.PaymentTermDays)
	}
}

// TestTermin_TolakPelangganRitel menjaga agar termin tidak dapat dipasang pada
// pelanggan ritel. Tanpa penolakan ini, pesanannya melewati pembayaran di muka
// tanpa dasar perjanjian.
func TestTermin_TolakPelangganRitel(t *testing.T) {
	c, _ := newCustomers(t)
	cust := buatPelanggan(t, c, "Ritel", customer.TypeRetail)

	_, err := c.SetContractTerm(context.Background(), cust.ID, customer.TermInput{PaymentTermDays: 30})
	if err == nil {
		t.Fatal("termin pada pelanggan ritel seharusnya ditolak")
	}
}

func TestTermin_ValidasiMasukan(t *testing.T) {
	c, _ := newCustomers(t)
	ctx := context.Background()
	cust := buatPelanggan(t, c, "Kontrak", customer.TypeContract)

	besok := time.Now().AddDate(0, 0, 1)
	kemarin := time.Now().AddDate(0, 0, -1)
	kasus := []struct {
		nama string
		in   customer.TermInput
	}{
		{"termin nol hari", customer.TermInput{PaymentTermDays: 0}},
		{"plafon negatif", customer.TermInput{PaymentTermDays: 30, CreditLimitCents: -1}},
		{"periode terbalik", customer.TermInput{PaymentTermDays: 30, ValidFrom: besok, ValidUntil: &kemarin}},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if _, err := c.SetContractTerm(ctx, cust.ID, k.in); !errors.Is(err, customer.ErrTermInvalid) {
				t.Fatalf("dapat %v, seharusnya ErrTermInvalid", err)
			}
		})
	}
}
