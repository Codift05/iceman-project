package dashboard_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/dashboard"
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

func hariIni() string { return time.Now().Format("2006-01-02") }

// depoUji membuat satu depo beserta wilayah dan slotnya.
//
// Indikator disaring per depo, sehingga setiap uji memakai deponya sendiri dan
// tidak terganggu data uji lain pada basis data yang sama.
func depoUji(t *testing.T, pool *pgxpool.Pool) (depotID, areaID, slotID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Dasbor', 1.4748, 124.8421, 8) RETURNING id`,
		"UJI-"+suffix).Scan(&depotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 15000) RETURNING id`, depotID, "Area "+suffix).Scan(&areaID)
	if err != nil {
		t.Fatalf("menyiapkan wilayah: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end, capacity, cutoff_at)
		VALUES ($1, current_date, '08:00', '11:00', 50, now() + interval '20 hours')
		RETURNING id`, areaID).Scan(&slotID)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}
	return depotID, areaID, slotID
}

// pesananUji menyisipkan satu pesanan dengan status, tanggal, dan nilai
// tertentu.
//
// Dibuat dengan SQL langsung agar uji ini dapat menyusun keadaan yang sulit
// dicapai lewat alur biasa, misalnya pesanan yang tanggal pengirimannya sudah
// lewat namun belum selesai.
func pesananUji(t *testing.T, pool *pgxpool.Pool, depotID, areaID, slotID uuid.UUID,
	status string, tanggalKirim string, total int64, bertermin bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var custID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO customers (phone, name) VALUES ($1, 'Pelanggan Dasbor')
		RETURNING id`, "0811"+suffix).Scan(&custID)
	if err != nil {
		t.Fatalf("menyiapkan pelanggan: %v", err)
	}

	var termin *int32
	if bertermin {
		n := int32(30)
		termin = &n
	}

	var id uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, customer_id, depot_id, service_area_id,
		       recipient_name, recipient_phone, address_line, latitude, longitude,
		       slot_id, scheduled_date, status, subtotal_cents, delivery_fee_cents,
		       total_cents, payment_term_days, cancelled_reason)
		VALUES ($1, $2, $3, $4, 'P', '0811', 'Jl', 1.475, 124.843, $5, $6::date,
		        $7::order_status, $8, 0, $8, $9,
		        CASE WHEN $7 = 'CANCELLED' THEN 'uji' ELSE '' END)
		RETURNING id`,
		"ICE-UJI-"+suffix, custID, depotID, areaID, slotID, tanggalKirim,
		status, total, termin).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pesanan: %v", err)
	}
	return id
}

// TestIndikator_OperasionalDihitungMenurutDefinisinya menjaga agar tiap
// indikator menghitung apa yang definisinya sebutkan, bukan yang mirip.
func TestIndikator_OperasionalDihitungMenurutDefinisinya(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)
	depotID, areaID, slotID := depoUji(t, pool)

	kemarin := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// Dua pesanan hari ini: satu menunggu bayar, satu sudah diproses.
	pesananUji(t, pool, depotID, areaID, slotID, "WAITING_PAYMENT", hariIni(), 100000, false)
	pesananUji(t, pool, depotID, areaID, slotID, "PROCESSING", hariIni(), 200000, true)
	// Satu dibatalkan: tidak dihitung pada pengiriman terjadwal.
	pesananUji(t, pool, depotID, areaID, slotID, "CANCELLED", hariIni(), 300000, false)
	// Satu terlambat: tanggal kirim kemarin, belum selesai.
	pesananUji(t, pool, depotID, areaID, slotID, "SCHEDULED", kemarin, 400000, false)

	got, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, false)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}

	// Pesanan baru dihitung menurut waktu pembuatan, jadi keempatnya masuk.
	if got.NewOrders != 4 {
		t.Fatalf("pesanan baru %d, seharusnya 4", got.NewOrders)
	}
	// Pengiriman terjadwal hari ini: dua, karena yang dibatalkan tidak
	// dihitung dan yang terlambat tanggalnya kemarin.
	if got.ScheduledToday != 2 {
		t.Fatalf("pengiriman terjadwal %d, seharusnya 2", got.ScheduledToday)
	}
	if got.PendingPayments != 1 {
		t.Fatalf("menunggu pembayaran %d, seharusnya 1", got.PendingPayments)
	}
	// Terlambat: satu, yang tanggal kirimnya kemarin dan belum selesai.
	if got.Late != 1 {
		t.Fatalf("keterlambatan %d, seharusnya 1", got.Late)
	}
	if got.From != hariIni() || got.To != hariIni() {
		t.Fatalf("periode bawaan %s sampai %s, seharusnya hari ini", got.From, got.To)
	}
}

// TestIndikator_MenungguBayarTidakDisaringPeriode menjaga alasan di
// definisinya: pesanan yang menunggu bayar sejak kemarin tetap perlu terlihat
// hari ini.
func TestIndikator_MenungguBayarTidakDisaringPeriode(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)
	depotID, areaID, slotID := depoUji(t, pool)

	id := pesananUji(t, pool, depotID, areaID, slotID, "WAITING_PAYMENT", hariIni(), 100000, false)
	// Pesanannya dibuat seminggu lalu.
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET created_at = now() - interval '7 days' WHERE id = $1`, id); err != nil {
		t.Fatalf("menggeser waktu pembuatan: %v", err)
	}

	got, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, false)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}
	// Tidak masuk pesanan baru hari ini, namun tetap terlihat menunggu bayar.
	if got.NewOrders != 0 {
		t.Fatalf("pesanan baru %d, seharusnya 0", got.NewOrders)
	}
	if got.PendingPayments != 1 {
		t.Fatalf("menunggu pembayaran %d, seharusnya tetap 1", got.PendingPayments)
	}
}

// TestIndikator_KeuanganTidakIkutTanpaKewenangan adalah uji keamanan.
// SRS-ADM-001 mewajibkan peran yang tidak berwenang tidak melihat indikator
// keuangan.
func TestIndikator_KeuanganTidakIkutTanpaKewenangan(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)
	depotID, areaID, slotID := depoUji(t, pool)
	pesananUji(t, pool, depotID, areaID, slotID, "PROCESSING", hariIni(), 500000, true)

	tanpa, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, false)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}
	if tanpa.Finance != nil {
		t.Fatalf("indikator keuangan ikut terkirim padahal tidak berwenang: %+v", tanpa.Finance)
	}
	// Indikator operasionalnya tetap terisi: yang disaring hanya bagian
	// keuangannya.
	if tanpa.NewOrders != 1 {
		t.Fatalf("pesanan baru %d, seharusnya tetap terhitung", tanpa.NewOrders)
	}

	dengan, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, true)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}
	if dengan.Finance == nil {
		t.Fatal("indikator keuangan seharusnya terisi bagi yang berwenang")
	}
	if dengan.Finance.RevenueCents != 500000 {
		t.Fatalf("omzet %d, seharusnya 500000", dengan.Finance.RevenueCents)
	}
}

// TestIndikator_OmzetTidakSamaDenganPenerimaan menjaga pembedaan yang penting
// bagi keuangan: pesanan bertermin menambah omzet tanpa menambah penerimaan.
//
// Menyamakan keduanya adalah kesalahan yang mudah dibuat dan sulit terlihat,
// karena pada penjualan tunai angkanya memang sama.
func TestIndikator_OmzetTidakSamaDenganPenerimaan(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)
	depotID, areaID, slotID := depoUji(t, pool)

	// Pesanan bertermin, belum dibayar.
	pesananUji(t, pool, depotID, areaID, slotID, "PROCESSING", hariIni(), 700000, true)

	got, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, true)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}
	if got.Finance.RevenueCents != 700000 {
		t.Fatalf("omzet %d, seharusnya 700000", got.Finance.RevenueCents)
	}
	if got.Finance.CollectedCents != 0 {
		t.Fatalf("penerimaan %d, seharusnya 0 karena belum dibayar", got.Finance.CollectedCents)
	}
	// Dan nilainya muncul sebagai piutang.
	if got.Finance.OutstandingCents != 700000 {
		t.Fatalf("piutang %d, seharusnya 700000", got.Finance.OutstandingCents)
	}
}

// TestIndikator_PesananBatalTidakMenambahOmzet menjaga definisi omzet.
func TestIndikator_PesananBatalTidakMenambahOmzet(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)
	depotID, areaID, slotID := depoUji(t, pool)

	pesananUji(t, pool, depotID, areaID, slotID, "PROCESSING", hariIni(), 100000, false)
	pesananUji(t, pool, depotID, areaID, slotID, "CANCELLED", hariIni(), 900000, false)

	got, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depotID}, true)
	if err != nil {
		t.Fatalf("menghitung indikator: %v", err)
	}
	if got.Finance.RevenueCents != 100000 {
		t.Fatalf("omzet %d, seharusnya 100000 tanpa yang dibatalkan", got.Finance.RevenueCents)
	}
	if got.Finance.CancelledOrders != 1 || got.Finance.CancelledCents != 900000 {
		t.Fatalf("pembatalan salah: %d pesanan, %d sen",
			got.Finance.CancelledOrders, got.Finance.CancelledCents)
	}
}

// TestIndikator_DisaringPerDepo menjaga agar admin satu depo tidak melihat
// angka depo lain tercampur.
func TestIndikator_DisaringPerDepo(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)

	depoA, areaA, slotA := depoUji(t, pool)
	depoB, areaB, slotB := depoUji(t, pool)
	pesananUji(t, pool, depoA, areaA, slotA, "PROCESSING", hariIni(), 100000, false)
	pesananUji(t, pool, depoB, areaB, slotB, "PROCESSING", hariIni(), 200000, false)

	a, err := svc.Compute(ctx, dashboard.Filter{DepotID: &depoA}, true)
	if err != nil {
		t.Fatalf("menghitung indikator depo A: %v", err)
	}
	if a.NewOrders != 1 || a.Finance.RevenueCents != 100000 {
		t.Fatalf("depo A tercampur depo lain: %d pesanan, %d sen",
			a.NewOrders, a.Finance.RevenueCents)
	}
}

// TestIndikator_PeriodeTidakSahDitolak menjaga agar periode yang separuh
// terisi tidak diterima diam diam.
func TestIndikator_PeriodeTidakSahDitolak(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := dashboard.NewService(pool)

	kasus := []dashboard.Filter{
		{From: hariIni()},
		{Until: hariIni()},
		{From: "bukan-tanggal", Until: hariIni()},
		{From: hariIni(), Until: "bukan-tanggal"},
		{From: "2026-10-10", Until: "2026-10-01"},
	}
	for i, f := range kasus {
		if _, err := svc.Compute(ctx, f, false); !errors.Is(err, dashboard.ErrPeriodInvalid) {
			t.Fatalf("kasus ke-%d seharusnya ditolak, dapat %v", i, err)
		}
	}
}

// TestDefinisi_SetiapIndikatorPunyaDefinisi menjaga SRS-ADM-001, yang
// mewajibkan definisi setiap indikator didokumentasikan.
//
// Daftar kuncinya ditulis ulang di sini dari struct indikatornya, sehingga
// indikator baru yang ditambahkan tanpa definisi akan tertangkap.
func TestDefinisi_SetiapIndikatorPunyaDefinisi(t *testing.T) {
	mau := []string{
		"new_orders", "scheduled_today", "pending_payments", "active_deliveries",
		"late", "failed_deliveries",
		"revenue_cents", "collected_cents", "outstanding_cents", "refunded_cents",
		"cancelled_orders", "cancelled_cents",
	}
	def := dashboard.Definitions()
	if len(def) != len(mau) {
		t.Fatalf("definisi ada %d, seharusnya %d", len(def), len(mau))
	}

	ada := map[string]dashboard.Definition{}
	for _, d := range def {
		ada[d.Key] = d
		if d.Label == "" || d.Description == "" {
			t.Fatalf("indikator %s tanpa label atau penjelasan", d.Key)
		}
	}
	for _, k := range mau {
		if _, ok := ada[k]; !ok {
			t.Fatalf("indikator %s tanpa definisi", k)
		}
	}

	// Indikator keuangan ditandai, supaya antarmuka dapat menyembunyikannya
	// tanpa menebak dari namanya.
	for _, k := range []string{"revenue_cents", "collected_cents", "outstanding_cents",
		"refunded_cents", "cancelled_orders", "cancelled_cents"} {
		if !ada[k].Finance {
			t.Fatalf("indikator %s seharusnya ditandai keuangan", k)
		}
	}
	for _, k := range []string{"new_orders", "scheduled_today", "pending_payments",
		"active_deliveries", "late", "failed_deliveries"} {
		if ada[k].Finance {
			t.Fatalf("indikator %s seharusnya tidak ditandai keuangan", k)
		}
	}
}
