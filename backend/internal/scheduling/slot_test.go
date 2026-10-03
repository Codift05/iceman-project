package scheduling_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/scheduling"
)

// reserveOnce menjalankan satu percobaan pemesanan dalam transaksinya sendiri,
// persis seperti yang akan dilakukan modul pemesanan nanti.
func reserveOnce(ctx context.Context, pool *pgxpool.Pool, slotID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := scheduling.Reserve(ctx, tx, slotID, time.Now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// hammer menjalankan n percobaan bersamaan dan mengembalikan jumlah yang berhasil
// beserta sebaran galatnya.
func hammer(t *testing.T, pool *pgxpool.Pool, slotID uuid.UUID, n int) (int32, map[string]int32) {
	t.Helper()
	ctx := context.Background()

	var ok int32
	counts := map[string]*int32{"full": new(int32), "lain": new(int32)}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // lepaskan seluruh goroutine sedekat mungkin pada saat yang sama
			switch err := reserveOnce(ctx, pool, slotID); {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, scheduling.ErrSlotFull):
				atomic.AddInt32(counts["full"], 1)
			default:
				t.Errorf("galat tak terduga: %v", err)
				atomic.AddInt32(counts["lain"], 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	out := map[string]int32{}
	for k, v := range counts {
		out[k] = atomic.LoadInt32(v)
	}
	return atomic.LoadInt32(&ok), out
}

// TC-CON-01: dua permintaan bersamaan untuk slot dengan sisa kuota satu.
func TestReserve_DuaBersamaanSisaSatu(t *testing.T) {
	pool := newPool(t)
	slotID := seedSlot(t, pool, 20, 19, future(), false)

	ok, errs := hammer(t, pool, slotID, 2)

	if ok != 1 {
		t.Fatalf("berhasil = %d, seharusnya tepat 1", ok)
	}
	if errs["full"] != 1 {
		t.Fatalf("ditolak penuh = %d, seharusnya 1", errs["full"])
	}
	if used := slotUsed(t, pool, slotID); used != 20 {
		t.Fatalf("kuota terpakai = %d, seharusnya 20", used)
	}
}

// TC-CON-02: dua puluh permintaan bersamaan untuk slot dengan sisa kuota lima.
func TestReserve_DuaPuluhBersamaanSisaLima(t *testing.T) {
	pool := newPool(t)
	slotID := seedSlot(t, pool, 20, 15, future(), false)

	ok, errs := hammer(t, pool, slotID, 20)

	if ok != 5 {
		t.Fatalf("berhasil = %d, seharusnya tepat 5", ok)
	}
	if errs["full"] != 15 {
		t.Fatalf("ditolak penuh = %d, seharusnya 15", errs["full"])
	}
	if used := slotUsed(t, pool, slotID); used != 20 {
		t.Fatalf("kuota terpakai = %d, seharusnya 20", used)
	}
}

// TC-CON-06: uji ketahanan. Banyak pemesanan bersamaan pada sepuluh slot,
// jumlah yang berhasil harus sama persis dengan total kuota yang tersedia.
func TestReserve_BanyakSlotTidakAdaKuotaHilang(t *testing.T) {
	if testing.Short() {
		t.Skip("dilewati pada mode singkat")
	}
	pool := newPool(t)

	const (
		slots      = 10
		capacity   = 20
		percobaan  = 60 // per slot, tiga kali lipat kapasitas
		totalKuota = slots * capacity
	)

	ids := make([]uuid.UUID, slots)
	for i := range ids {
		ids[i] = seedSlot(t, pool, capacity, 0, future(), false)
	}

	var berhasil int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	ctx := context.Background()

	for _, id := range ids {
		for j := 0; j < percobaan; j++ {
			wg.Add(1)
			go func(slotID uuid.UUID) {
				defer wg.Done()
				<-start
				if err := reserveOnce(ctx, pool, slotID); err == nil {
					atomic.AddInt32(&berhasil, 1)
				}
			}(id)
		}
	}
	close(start)
	wg.Wait()

	if berhasil != totalKuota {
		t.Fatalf("berhasil = %d, seharusnya tepat %d", berhasil, totalKuota)
	}
	var jumlahTerpakai int32
	for _, id := range ids {
		u := slotUsed(t, pool, id)
		if u != capacity {
			t.Errorf("slot %s terpakai = %d, seharusnya %d", id, u, capacity)
		}
		jumlahTerpakai += u
	}
	if jumlahTerpakai != totalKuota {
		t.Fatalf("total terpakai = %d, seharusnya %d", jumlahTerpakai, totalKuota)
	}
	t.Logf("%d percobaan bersamaan pada %d slot menghasilkan tepat %d pemesanan",
		slots*percobaan, slots, berhasil)
}

func TestReserve_TolakSetelahCutoff(t *testing.T) {
	pool := newPool(t)
	slotID := seedSlot(t, pool, 20, 0, past(), false)

	err := reserveOnce(context.Background(), pool, slotID)
	if !errors.Is(err, scheduling.ErrSlotCutoffPassed) {
		t.Fatalf("galat = %v, seharusnya ErrSlotCutoffPassed", err)
	}
	if used := slotUsed(t, pool, slotID); used != 0 {
		t.Fatalf("kuota terpakai = %d, seharusnya tetap 0", used)
	}
}

func TestReserve_TolakHariLibur(t *testing.T) {
	pool := newPool(t)
	slotID := seedSlot(t, pool, 20, 0, future(), true)

	err := reserveOnce(context.Background(), pool, slotID)
	if !errors.Is(err, scheduling.ErrSlotUnavailable) {
		t.Fatalf("galat = %v, seharusnya ErrSlotUnavailable", err)
	}
}

func TestReserve_SlotTidakDikenal(t *testing.T) {
	pool := newPool(t)
	err := reserveOnce(context.Background(), pool, uuid.New())
	if !errors.Is(err, scheduling.ErrSlotNotFound) {
		t.Fatalf("galat = %v, seharusnya ErrSlotNotFound", err)
	}
}

// Pembatalan mengembalikan kuota sehingga pelanggan lain dapat memakainya (TC-ORD-03).
func TestRelease_KuotaKembali(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 20, 20, future(), false)

	if err := reserveOnce(ctx, pool, slotID); !errors.Is(err, scheduling.ErrSlotFull) {
		t.Fatalf("slot penuh seharusnya ditolak, dapat %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduling.Release(ctx, tx, slotID); err != nil {
		t.Fatalf("melepas kuota: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := reserveOnce(ctx, pool, slotID); err != nil {
		t.Fatalf("setelah pembatalan seharusnya bisa dipesan, dapat %v", err)
	}
	if used := slotUsed(t, pool, slotID); used != 20 {
		t.Fatalf("kuota terpakai = %d, seharusnya 20", used)
	}
}

// Transaksi yang dibatalkan tidak boleh menyisakan kuota terpakai (SRS-ORD-002).
func TestReserve_RollbackTidakMenyisakanKuota(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 20, 0, future(), false)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduling.Reserve(ctx, tx, slotID, time.Now()); err != nil {
		t.Fatalf("mengambil kuota: %v", err)
	}
	// Anggap langkah berikutnya gagal, misalnya produk ternyata habis.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if used := slotUsed(t, pool, slotID); used != 0 {
		t.Fatalf("kuota terpakai = %d, seharusnya 0 setelah rollback", used)
	}
}

// DB-01 adalah jaring pengaman terakhir: basis data menolak kuota terlampaui
// walau ada jalur kode yang lupa mengunci baris lebih dahulu.
func TestConstraint_KuotaTidakBolehTerlampaui(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 5, 5, future(), false)

	_, err := pool.Exec(ctx,
		`UPDATE delivery_slots SET used = used + 1 WHERE id = $1`, slotID)
	if err == nil {
		t.Fatal("basis data seharusnya menolak kuota melampaui kapasitas")
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() != "23514" {
		t.Fatalf("SQLSTATE = %s, seharusnya 23514 pelanggaran CHECK", pgErr.SQLState())
	}
	t.Logf("ditolak basis data seperti seharusnya: %v", err)
}

var _ = pgx.ErrNoRows
