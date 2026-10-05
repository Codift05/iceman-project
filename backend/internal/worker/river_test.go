package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/worker"
)

// jumlahJob menghitung job pembuatan slot untuk satu area tertentu.
//
// Penanda areanya dipakai sebagai pembeda supaya uji ini tidak terganggu job
// sisa dari uji lain yang memakai basis data yang sama.
func jumlahJob(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, areaID uuid.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM   river_job
		WHERE  kind = 'generate_slots'
		  AND  args->>'area_id' = $1`, areaID.String()).Scan(&n)
	if err != nil {
		t.Fatalf("menghitung job: %v", err)
	}
	return n
}

// TestEnqueueTx_IkutBatalSaatTransaksiBatal membuktikan janji AD-03: job dan
// perubahan data hidup mati bersama.
//
// Inilah alasan antrean ditaruh di PostgreSQL yang sama, bukan di Redis atau
// layanan antrean terpisah. Dengan antrean di luar basis data, job sudah
// terkirim sebelum transaksi di-commit, sehingga ada saat saat job berjalan
// mengacu pada baris yang ternyata tidak pernah tersimpan.
func TestEnqueueTx_IkutBatalSaatTransaksiBatal(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	client, err := worker.NewClient(pool)
	if err != nil {
		t.Fatalf("membuat klien antrean: %v", err)
	}

	penanda := uuid.New()
	kode := "UJI-" + uuid.NewString()[:8]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("memulai transaksi: %v", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Batal', 1.4748, 124.8421, 8)`, kode)
	if err != nil {
		t.Fatalf("menyisipkan depo: %v", err)
	}
	if err := worker.EnqueueTx(ctx, client, tx, worker.GenerateSlotsArgs{Days: 7, AreaID: &penanda}); err != nil {
		t.Fatalf("memasukkan job: %v", err)
	}

	// Di dalam transaksi, keduanya sudah terlihat.
	if n := jumlahJob(t, tx, penanda); n != 1 {
		t.Fatalf("di dalam transaksi job seharusnya 1, dapat %d", n)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("membatalkan transaksi: %v", err)
	}

	// Setelah dibatalkan, keduanya harus hilang bersama.
	if n := jumlahJob(t, pool, penanda); n != 0 {
		t.Fatalf("job seharusnya ikut batal, masih ada %d", n)
	}
	var adaDepot bool
	if err := pool.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM depots WHERE code = $1)`, kode).Scan(&adaDepot); err != nil {
		t.Fatalf("memeriksa depo: %v", err)
	}
	if adaDepot {
		t.Fatal("depo seharusnya ikut batal")
	}
}

// TestEnqueueTx_TersimpanSaatTransaksiDiCommit adalah sisi sebaliknya: kalau
// transaksinya berhasil, job pasti ada. Tanpa uji ini, uji di atas masih lulus
// seandainya EnqueueTx tidak melakukan apa apa.
func TestEnqueueTx_TersimpanSaatTransaksiDiCommit(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	client, err := worker.NewClient(pool)
	if err != nil {
		t.Fatalf("membuat klien antrean: %v", err)
	}

	penanda := uuid.New()
	kode := "UJI-" + uuid.NewString()[:8]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("memulai transaksi: %v", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Commit', 1.4748, 124.8421, 8)`, kode)
	if err != nil {
		t.Fatalf("menyisipkan depo: %v", err)
	}
	if err := worker.EnqueueTx(ctx, client, tx, worker.GenerateSlotsArgs{Days: 7, AreaID: &penanda}); err != nil {
		t.Fatalf("memasukkan job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan transaksi: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'area_id' = $1`, penanda.String())
		_, _ = pool.Exec(ctx, `UPDATE depots SET is_active = false WHERE code = $1`, kode)
	})

	if n := jumlahJob(t, pool, penanda); n != 1 {
		t.Fatalf("job seharusnya tersimpan, dapat %d", n)
	}
}

// TestGenerateSlotsWorker_Work menjalankan pekerja sungguhan lewat antrean,
// bukan memanggil Work langsung, agar pendaftaran job dan penguraian argumen
// ikut teruji.
func TestGenerateSlotsWorker_Work(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	areaID := seedArea(t, pool)

	client, err := worker.NewWorker(worker.Deps{Pool: pool, Log: diam()}, worker.Options{})
	if err != nil {
		t.Fatalf("membuat pekerja: %v", err)
	}
	if _, err := client.Insert(ctx, worker.GenerateSlotsArgs{Days: 3, AreaID: &areaID}, nil); err != nil {
		t.Fatalf("memasukkan job: %v", err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatalf("menjalankan pekerja: %v", err)
	}
	t.Cleanup(func() {
		hentiCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(hentiCtx)
	})

	// Tiga hari dikali tiga jendela bawaan.
	tungguSlot(t, pool, areaID, len(mustTemplates())*3)
}
