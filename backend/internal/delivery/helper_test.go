package delivery_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/delivery"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/scheduling"
	"github.com/iceman/backend/internal/store"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

type lingkungan struct {
	pool      *pgxpool.Pool
	produk    *catalog.Products
	pelanggan *customer.Customers
	keranjang *cart.Carts
	pesanan   *order.Orders
	kirim     *delivery.Deliveries
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

	produk := catalog.NewProducts(pool)
	pelanggan := customer.NewCustomers(pool)
	keranjang := cart.NewCarts(pool, produk)

	return &lingkungan{
		pool: pool, produk: produk, pelanggan: pelanggan, keranjang: keranjang,
		pesanan: order.NewOrders(order.Deps{
			Pool: pool, Carts: keranjang, Customers: pelanggan,
			Slots: scheduling.NewSlots(pool),
		}),
		kirim: delivery.NewDeliveries(pool),
	}
}

// depo adalah satu depo beserta wilayah layanannya.
type depo struct {
	DepotID uuid.UUID
	AreaID  uuid.UUID
}

func (l *lingkungan) buatDepo(t *testing.T) depo {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var d depo
	err := l.pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Kirim', 1.4748, 124.8421, 8)
		RETURNING id`, "UJI-"+suffix).Scan(&d.DepotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = l.pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 15000)
		RETURNING id`, d.DepotID, "Area "+suffix).Scan(&d.AreaID)
	if err != nil {
		t.Fatalf("menyiapkan wilayah: %v", err)
	}
	return d
}

// buatDriver membuat pengguna berperan DRIVER pada satu depo.
func (l *lingkungan) buatDriver(t *testing.T, depotID uuid.UUID, aktif bool) uuid.UUID {
	t.Helper()
	status := "ACTIVE"
	if !aktif {
		status = "INACTIVE"
	}
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO users (role_id, depot_id, email, name, password_hash, status)
		SELECT r.id, $1, $2, 'Driver Uji', 'x', $3
		FROM   roles r WHERE r.code = 'DRIVER'
		RETURNING id`, depotID, "drv-"+uuid.NewString()[:8]+"@uji.test", status).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan driver: %v", err)
	}
	if _, err := l.pool.Exec(context.Background(),
		`INSERT INTO drivers (user_id, vehicle_plate) VALUES ($1, 'DB 1234 XX')`, id); err != nil {
		t.Fatalf("menyiapkan profil driver: %v", err)
	}
	return id
}

// buatAdmin membuat pengguna berperan ADMIN_OPS, dipakai sebagai pelaku.
func (l *lingkungan) buatAdmin(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO users (role_id, email, name, password_hash, status)
		SELECT r.id, $1, 'Admin Uji', 'x', 'ACTIVE'
		FROM   roles r WHERE r.code = 'ADMIN_OPS'
		RETURNING id`, "adm-"+uuid.NewString()[:8]+"@uji.test").Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan admin: %v", err)
	}
	return id
}

// buatPesanan membuat satu pesanan berstatus PROCESSING, siap ditugaskan.
//
// Pelanggan dibuat berjenis kontrak dengan termin berlaku, karena pesanan
// pelanggan semacam itu langsung berstatus PROCESSING tanpa perlu melewati
// pembayaran lebih dahulu.
func (l *lingkungan) buatPesanan(t *testing.T, d depo) *order.Order {
	t.Helper()
	ctx := context.Background()

	cust, err := l.pelanggan.Create(ctx, customer.Input{
		Phone: "0811" + uuid.NewString()[:8], Name: "Toko Kirim",
		Type: customer.TypeContract,
	})
	if err != nil {
		t.Fatalf("membuat pelanggan: %v", err)
	}
	if _, err := l.pelanggan.SetContractTerm(ctx, cust.ID, customer.TermInput{
		PaymentTermDays: 30,
	}); err != nil {
		t.Fatalf("menetapkan termin: %v", err)
	}
	alamat, err := l.pelanggan.AddAddress(ctx, cust.ID, customer.AddressInput{
		ServiceAreaID: d.AreaID, RecipientName: "Pak Budi", Phone: "081122334455",
		AddressLine: "Jl. Uji No. 1", Latitude: 1.4750, Longitude: 124.8430,
	})
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}

	var slotID uuid.UUID
	err = l.pool.QueryRow(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end, capacity, cutoff_at)
		VALUES ($1, current_date + 1, $2, '23:00', 20, now() + interval '20 hours')
		RETURNING id`, d.AreaID, waktuJendela()).Scan(&slotID)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}

	prod, err := l.produk.Create(ctx, catalog.ProductInput{
		SKU: "UJI-" + uuid.NewString()[:8], Name: "Es Balok",
		BasePriceCents: 2500000, MinOrderQty: 1,
	})
	if err != nil {
		t.Fatalf("membuat produk: %v", err)
	}
	if _, err := l.keranjang.Add(ctx, cust.ID, prod.ID, 1); err != nil {
		t.Fatalf("mengisi keranjang: %v", err)
	}

	o, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: cust.ID, AddressID: alamat.ID, SlotID: slotID,
	})
	if err != nil {
		t.Fatalf("membuat pesanan: %v", err)
	}
	if o.Status != order.StatusProcessing {
		t.Fatalf("pesanan uji berstatus %s, seharusnya PROCESSING", o.Status)
	}
	return o
}

// jendela memberi jam mulai slot yang berbeda tiap pemanggilan, karena DB-09
// melarang dua slot pada wilayah, tanggal, dan jam mulai yang sama.
var jendelaBerikut = 0

func waktuJendela() string {
	jam := []string{"00:00", "01:00", "02:00", "03:00", "04:00", "05:00", "06:00",
		"07:00", "08:00", "09:00", "10:00", "11:00", "12:00", "13:00", "14:00",
		"15:00", "16:00", "17:00", "18:00", "19:00", "20:00", "21:00", "22:00"}
	j := jam[jendelaBerikut%len(jam)]
	jendelaBerikut++
	return j
}

// sebagai menyelipkan pelaku ke konteks, seperti yang dilakukan middleware HTTP.
func sebagai(userID uuid.UUID) context.Context {
	return audit.WithActor(context.Background(), userID)
}

// statusPesanan membaca status sebuah pesanan.
func (l *lingkungan) statusPesanan(t *testing.T, orderID uuid.UUID) string {
	t.Helper()
	var s string
	err := l.pool.QueryRow(context.Background(),
		`SELECT status::text FROM orders WHERE id = $1`, orderID).Scan(&s)
	if err != nil {
		t.Fatalf("membaca status pesanan: %v", err)
	}
	return s
}

func ptrWaktu(t time.Time) *time.Time { return &t }

// kuotaSlotPesanan membaca kuota terpakai pada slot sebuah pesanan.
func (l *lingkungan) kuotaSlotPesanan(t *testing.T, orderID uuid.UUID) int32 {
	t.Helper()
	var used int32
	err := l.pool.QueryRow(context.Background(), `
		SELECT s.used FROM delivery_slots s
		JOIN   orders o ON o.slot_id = s.id
		WHERE  o.id = $1`, orderID).Scan(&used)
	if err != nil {
		t.Fatalf("membaca kuota slot: %v", err)
	}
	return used
}

// tugas menyiapkan satu pengiriman yang sudah ditugaskan.
func (l *lingkungan) tugas(t *testing.T) (*delivery.Delivery, uuid.UUID, uuid.UUID, *order.Order) {
	t.Helper()
	d := l.buatDepo(t)
	drv := l.buatDriver(t, d.DepotID, true)
	adm := l.buatAdmin(t)
	o := l.buatPesanan(t, d)

	got, err := l.kirim.Assign(sebagai(adm), delivery.AssignInput{OrderID: o.ID, DriverID: drv})
	if err != nil {
		t.Fatalf("menugaskan: %v", err)
	}
	return got, drv, adm, o
}

// majukan memindahkan pengiriman melalui rangkaian status sebagai driver.
func (l *lingkungan) majukan(t *testing.T, deliveryID, drv uuid.UUID, urutan ...string) *delivery.Delivery {
	t.Helper()
	var got *delivery.Delivery
	for _, s := range urutan {
		var err error
		got, err = l.kirim.ChangeStatus(context.Background(), deliveryID, s,
			delivery.ChangeInput{ActorID: &drv, Reason: "uji"})
		if err != nil {
			t.Fatalf("memindahkan ke %s: %v", s, err)
		}
	}
	return got
}
