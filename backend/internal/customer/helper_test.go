package customer_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/customer"
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

func newCustomers(t *testing.T) (*customer.Customers, *pgxpool.Pool) {
	t.Helper()
	pool := newPool(t)
	return customer.NewCustomers(pool), pool
}

// telepon membuat nomor telepon unik agar uji tidak bertabrakan.
func telepon() string { return "0811" + uuid.NewString()[:8] }

func buatPelanggan(t *testing.T, c *customer.Customers, nama, jenis string) *customer.Customer {
	t.Helper()
	cust, err := c.Create(context.Background(), customer.Input{
		Phone: telepon(), Name: nama, Type: jenis,
	})
	if err != nil {
		t.Fatalf("membuat pelanggan %s: %v", nama, err)
	}
	return cust
}

// buatWilayah membuat satu depo dan satu wilayah layanan aktif.
func buatWilayah(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var depotID, areaID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Pelanggan', 1.4748, 124.8421, 8)
		RETURNING id`, "UJI-"+suffix).Scan(&depotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 15000)
		RETURNING id`, depotID, "Area "+suffix).Scan(&areaID)
	if err != nil {
		t.Fatalf("menyiapkan wilayah: %v", err)
	}
	return areaID
}

func alamatBaku(areaID uuid.UUID, label string) customer.AddressInput {
	return customer.AddressInput{
		ServiceAreaID: areaID,
		Label:         label,
		RecipientName: "Penerima Uji",
		Phone:         "081122334455",
		AddressLine:   "Jl. Uji No. 1",
		Latitude:      1.4750,
		Longitude:     124.8430,
	}
}
