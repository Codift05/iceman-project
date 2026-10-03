package worker_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/scheduling"
	"github.com/iceman/backend/internal/store"
	"github.com/iceman/backend/internal/worker"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

// newPool menyiapkan basis data uji lengkap dengan tabel antrean.
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

	if err := worker.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrasi antrean: %v", err)
	}
	return pool
}

// diam membuang catatan log pekerja agar keluaran uji tetap terbaca.
func diam() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedArea membuat satu depo dan satu area baru, lalu menonaktifkan area lain.
//
// Area lain dinonaktifkan karena pembuatan slot bekerja pada seluruh area
// aktif. Tanpa itu, sisa area dari berkas uji lain ikut terbuat slotnya dan
// uji ini melambat tanpa guna.
func seedArea(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE service_areas SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("mengisolasi area: %v", err)
	}

	suffix := uuid.NewString()[:8]
	var depotID, areaID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Pekerja', 1.4748, 124.8421, 8)
		RETURNING id`, "UJI-"+suffix).Scan(&depotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 10000)
		RETURNING id`, depotID, "Area "+suffix).Scan(&areaID)
	if err != nil {
		t.Fatalf("menyiapkan area: %v", err)
	}
	return areaID
}

// tungguSlot menunggu sampai jumlah slot area mencapai yang diharapkan.
//
// Job berjalan di goroutine lain, jadi jumlahnya tidak langsung tersedia
// sesudah job dimasukkan. Menunggu dengan batas waktu lebih jujur daripada
// tidur sekian detik lalu berharap sudah selesai.
func tungguSlot(t *testing.T, pool *pgxpool.Pool, areaID uuid.UUID, mau int) {
	t.Helper()
	ctx := context.Background()
	batas := time.Now().Add(20 * time.Second)

	var ada int
	for time.Now().Before(batas) {
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM delivery_slots WHERE service_area_id = $1`,
			areaID).Scan(&ada); err != nil {
			t.Fatalf("menghitung slot: %v", err)
		}
		if ada == mau {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("slot yang terbuat %d, seharusnya %d", ada, mau)
}

// mustTemplates adalah pola slot bawaan yang dipakai pekerja.
func mustTemplates() []scheduling.Template { return scheduling.DefaultTemplates }
