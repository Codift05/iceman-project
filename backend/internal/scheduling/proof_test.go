package scheduling_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// reserveTanpaKunci meniru cara yang terlihat benar namun salah: membaca kuota,
// memeriksanya di lapisan aplikasi, lalu menulis. Tanpa FOR UPDATE, dua transaksi
// dapat membaca nilai yang sama sebelum salah satunya menulis.
//
// Fungsi ini hanya ada di berkas uji. Ia dipakai untuk membuktikan bahwa
// penguncian baris pada Reserve memang yang membuat hasilnya benar, bukan
// kebetulan karena pengujian berjalan terlalu cepat.
func reserveTanpaKunci(ctx context.Context, pool *pgxpool.Pool, slotID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var capacity, used int32
	err = tx.QueryRow(ctx,
		`SELECT capacity, used FROM delivery_slots WHERE id = $1`, slotID).
		Scan(&capacity, &used)
	if err != nil {
		return err
	}
	if used >= capacity {
		return errPenuh
	}
	// Jeda kecil memperbesar jendela balapan agar cacatnya terlihat konsisten.
	time.Sleep(2 * time.Millisecond)

	// Penulisan sengaja tanpa syarat used < capacity, meniru kode yang
	// mengandalkan pemeriksaan di atas.
	if _, err := tx.Exec(ctx,
		`UPDATE delivery_slots SET used = $2 WHERE id = $1`, slotID, used+1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type errSederhana string

func (e errSederhana) Error() string { return string(e) }

const errPenuh = errSederhana("penuh")

// TestPembanding_TanpaKunciKebobolan menunjukkan bahwa cara tanpa kunci baris
// benar benar menjual kuota melebihi kapasitas, sedangkan Reserve tidak.
//
// Uji ini adalah dasar keputusan arsitektur AD-02 dan penanganan pada
// Architecture Bab 8.1. Bila suatu saat ada yang mengusulkan menghapus
// FOR UPDATE demi kecepatan, jalankan uji ini lebih dahulu.
func TestPembanding_TanpaKunciKebobolan(t *testing.T) {
	if testing.Short() {
		t.Skip("dilewati pada mode singkat")
	}
	pool := newPool(t)
	ctx := context.Background()

	const (
		kapasitas = 5
		penyerbu  = 40
	)

	// Kolom used dibiarkan melar sementara agar cacatnya terlihat, bukan
	// tertahan constraint basis data. Constraint tetap aktif pada tabel asli.
	slotID := seedSlot(t, pool, kapasitas, 0, future(), false)
	if _, err := pool.Exec(ctx,
		`ALTER TABLE delivery_slots DROP CONSTRAINT delivery_slots_used_valid`); err != nil {
		t.Fatalf("melepas constraint sementara: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE delivery_slots SET used = capacity WHERE used > capacity`)
		_, _ = pool.Exec(context.Background(),
			`ALTER TABLE delivery_slots ADD CONSTRAINT delivery_slots_used_valid
			 CHECK (used >= 0 AND used <= capacity)`)
	})

	var berhasil int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := reserveTanpaKunci(ctx, pool, slotID); err == nil {
				atomic.AddInt32(&berhasil, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	used := slotUsed(t, pool, slotID)
	t.Logf("tanpa kunci baris: %d percobaan, %d dianggap berhasil, kuota terpakai tercatat %d dari kapasitas %d",
		penyerbu, berhasil, used, kapasitas)

	if berhasil <= kapasitas {
		t.Skipf("balapan tidak terpicu pada mesin ini (berhasil=%d). "+
			"Uji ini bersifat menunjukkan, bukan menjamin.", berhasil)
	}
	t.Logf("terbukti kebobolan: %d pemesanan melebihi kapasitas %d", berhasil, kapasitas)
}

// TestPembanding_DenganKunciTidakKebobolan menjalankan beban yang sama
// melalui Reserve, dan hasilnya harus tepat sebesar kapasitas.
func TestPembanding_DenganKunciTidakKebobolan(t *testing.T) {
	pool := newPool(t)

	const (
		kapasitas = 5
		penyerbu  = 40
	)
	slotID := seedSlot(t, pool, kapasitas, 0, future(), false)

	ok, _ := hammer(t, pool, slotID, penyerbu)

	if ok != kapasitas {
		t.Fatalf("berhasil = %d, seharusnya tepat %d", ok, kapasitas)
	}
	if used := slotUsed(t, pool, slotID); used != kapasitas {
		t.Fatalf("kuota terpakai = %d, seharusnya %d", used, kapasitas)
	}
	t.Logf("dengan kunci baris: %d percobaan bersamaan, tepat %d berhasil, tidak ada kelebihan",
		penyerbu, ok)
}
