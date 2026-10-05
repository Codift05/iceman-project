package order_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/store"
	"github.com/iceman/backend/internal/worker"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

type lingkungan struct {
	pool      *pgxpool.Pool
	produk    *catalog.Products
	pelanggan *customer.Customers
	keranjang *cart.Carts
	pesanan   *order.Orders
	queue     *river.Client[pgx.Tx]
}

func siapkan(t *testing.T) *lingkungan {
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
	queue, err := worker.NewClient(pool)
	if err != nil {
		t.Fatalf("membuat klien antrean: %v", err)
	}

	produk := catalog.NewProducts(pool)
	pelanggan := customer.NewCustomers(pool)
	keranjang := cart.NewCarts(pool, produk)

	return &lingkungan{
		pool: pool, produk: produk, pelanggan: pelanggan, keranjang: keranjang,
		queue: queue,
		pesanan: order.NewOrders(order.Deps{
			Pool: pool, Carts: keranjang, Customers: pelanggan, Queue: queue,
		}),
	}
}

func diam() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// wilayah adalah satu depo, satu wilayah layanan, dan slotnya.
type wilayah struct {
	DepotID uuid.UUID
	AreaID  uuid.UUID
}

func (l *lingkungan) buatWilayah(t *testing.T) wilayah {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var w wilayah
	err := l.pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Pesanan', 1.4748, 124.8421, 8)
		RETURNING id`, "UJI-"+suffix).Scan(&w.DepotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = l.pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 15000)
		RETURNING id`, w.DepotID, "Area "+suffix).Scan(&w.AreaID)
	if err != nil {
		t.Fatalf("menyiapkan wilayah: %v", err)
	}
	return w
}

// buatSlot membuat slot dengan kapasitas tertentu, besok, batas pemesanan masih
// jauh, dan bukan hari libur.
func (l *lingkungan) buatSlot(t *testing.T, areaID uuid.UUID, kapasitas int32) uuid.UUID {
	t.Helper()
	return l.buatSlotKhusus(t, areaID, kapasitas, 0,
		time.Now().Add(20*time.Hour), false, "08:00")
}

func (l *lingkungan) buatSlotKhusus(t *testing.T, areaID uuid.UUID, kapasitas, terpakai int32, cutoff time.Time, libur bool, jamMulai string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end,
		        capacity, used, cutoff_at, is_holiday)
		VALUES ($1, current_date + 1, $2, '23:00', $3, $4, $5, $6)
		RETURNING id`, areaID, jamMulai, kapasitas, terpakai, cutoff, libur).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}
	return id
}

func (l *lingkungan) buatProduk(t *testing.T, nama string, harga int64, minOrder int32) *catalog.Product {
	t.Helper()
	prod, err := l.produk.Create(context.Background(), catalog.ProductInput{
		SKU: "UJI-" + uuid.NewString()[:8], Name: nama,
		Category: "Balok", Packaging: "Balok 25 kg",
		BasePriceCents: harga, MinOrderQty: minOrder,
	})
	if err != nil {
		t.Fatalf("membuat produk %s: %v", nama, err)
	}
	return prod
}

// pembeli adalah pelanggan beserta alamatnya yang siap checkout.
type pembeli struct {
	ID        uuid.UUID
	AddressID uuid.UUID
}

func (l *lingkungan) buatPembeli(t *testing.T, areaID uuid.UUID, jenis string) pembeli {
	t.Helper()
	ctx := context.Background()

	cust, err := l.pelanggan.Create(ctx, customer.Input{
		Phone: "0811" + uuid.NewString()[:8], Name: "Pembeli Uji", Type: jenis,
	})
	if err != nil {
		t.Fatalf("membuat pelanggan: %v", err)
	}
	alamat, err := l.pelanggan.AddAddress(ctx, cust.ID, customer.AddressInput{
		ServiceAreaID: areaID, Label: "Rumah",
		RecipientName: "Penerima Uji", Phone: "081122334455",
		AddressLine: "Jl. Uji No. 1", Latitude: 1.4750, Longitude: 124.8430,
	})
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}
	return pembeli{ID: cust.ID, AddressID: alamat.ID}
}

// isiKeranjang menambah satu produk ke keranjang pembeli.
func (l *lingkungan) isiKeranjang(t *testing.T, p pembeli, prod *catalog.Product, qty int32) {
	t.Helper()
	if _, err := l.keranjang.Add(context.Background(), p.ID, prod.ID, qty); err != nil {
		t.Fatalf("mengisi keranjang: %v", err)
	}
}

// kuotaTerpakai membaca kuota terpakai sebuah slot.
func (l *lingkungan) kuotaTerpakai(t *testing.T, slotID uuid.UUID) int32 {
	t.Helper()
	var used int32
	err := l.pool.QueryRow(context.Background(),
		`SELECT used FROM delivery_slots WHERE id = $1`, slotID).Scan(&used)
	if err != nil {
		t.Fatalf("membaca kuota terpakai: %v", err)
	}
	return used
}

// jumlahPesanan menghitung pesanan pada sebuah slot.
func (l *lingkungan) jumlahPesanan(t *testing.T, slotID uuid.UUID) int {
	t.Helper()
	var n int
	err := l.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE slot_id = $1`, slotID).Scan(&n)
	if err != nil {
		t.Fatalf("menghitung pesanan: %v", err)
	}
	return n
}

// jumlahJobTagihan menghitung job pembuatan tagihan untuk sebuah pesanan.
func (l *lingkungan) jumlahJobTagihan(t *testing.T, orderID uuid.UUID) int {
	t.Helper()
	var n int
	err := l.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM river_job
		WHERE  kind = 'create_payment' AND args->>'order_id' = $1`,
		orderID.String()).Scan(&n)
	if err != nil {
		t.Fatalf("menghitung job tagihan: %v", err)
	}
	return n
}

// produkInput menyusun masukan pengubahan produk dari produk yang sudah ada,
// agar uji hanya menyebut nilai yang memang ingin diubahnya.
func produkInput(prod *catalog.Product, harga int64, aktif *bool) catalog.ProductInput {
	return catalog.ProductInput{
		Name: prod.Name, Category: prod.Category, Packaging: prod.Packaging,
		BasePriceCents: harga, MinOrderQty: prod.MinOrderQty, IsActive: aktif,
	}
}

// buatAdmin membuat satu pengguna internal, dipakai sebagai pembuat pesanan
// manual.
func (l *lingkungan) buatAdmin(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO users (name, email, password_hash, role_id, status)
		SELECT 'Admin Uji', $1, 'x', r.id, 'ACTIVE'
		FROM   roles r WHERE r.code = 'ADMIN_OPS'
		RETURNING id`, "admin-"+uuid.NewString()[:8]+"@uji.test").Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan admin: %v", err)
	}
	return id
}

// timeDepan adalah batas pemesanan yang masih jauh, dipakai slot uji yang
// harus selalu dapat dipesan.
func timeDepan() time.Time { return time.Now().Add(20 * time.Hour) }
