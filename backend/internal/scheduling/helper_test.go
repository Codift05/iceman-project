package scheduling_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/store"
)

// dsn membaca alamat basis data uji dari lingkungan. Tidak ada nilai baku yang
// memuat kredensial, agar repositori tetap bebas dari kata sandi. Jalankan uji
// lewat "make test" yang sudah memuatnya dari berkas .env.
func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

// newPool menyiapkan basis data uji: migrasi diterapkan, lalu tabel dikosongkan
// agar setiap berkas uji berangkat dari keadaan yang sama.
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

// seedSlot membuat satu depo, satu area, dan satu slot dengan kapasitas tertentu.
func seedSlot(t *testing.T, pool *pgxpool.Pool, capacity, used int32, cutoff time.Time, holiday bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var depotID, areaID, slotID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Uji', 1.4748, 124.8421, 8)
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
	err = pool.QueryRow(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end,
		        capacity, used, cutoff_at, is_holiday)
		VALUES ($1, current_date + 1, '11:00', '14:00', $2, $3, $4, $5)
		RETURNING id`, areaID, capacity, used, cutoff, holiday).Scan(&slotID)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}
	return slotID
}

func slotUsed(t *testing.T, pool *pgxpool.Pool, slotID uuid.UUID) int32 {
	t.Helper()
	var used int32
	if err := pool.QueryRow(context.Background(),
		`SELECT used FROM delivery_slots WHERE id = $1`, slotID).Scan(&used); err != nil {
		t.Fatalf("membaca kuota terpakai: %v", err)
	}
	return used
}

func future() time.Time { return time.Now().Add(24 * time.Hour) }
func past() time.Time   { return time.Now().Add(-1 * time.Hour) }
