package notify_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/notify"
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

func diam() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// kanalPalsu mencatat pesan yang dikirim, dan dapat dibuat selalu gagal.
type kanalPalsu struct {
	nama     string
	gagal    error
	terkirim []notify.Message
}

func (k *kanalPalsu) Name() string { return k.nama }

func (k *kanalPalsu) Send(_ context.Context, m notify.Message) error {
	if k.gagal != nil {
		return k.gagal
	}
	k.terkirim = append(k.terkirim, m)
	return nil
}

// antreanPalsu mencatat notifikasi yang diantre.
type antreanPalsu struct {
	masuk []uuid.UUID
	gagal error
}

func (a *antreanPalsu) EnqueueSendTx(_ context.Context, _ pgx.Tx, id uuid.UUID) error {
	if a.gagal != nil {
		return a.gagal
	}
	a.masuk = append(a.masuk, id)
	return nil
}

// pesananUji membuat satu pesanan minimal, cukup untuk menguji notifikasi.
//
// Dibuat dengan SQL langsung, bukan lewat domain pesanan, agar paket uji ini
// tidak bergantung pada seluruh rantai katalog, keranjang, dan penjadwalan.
// Yang diuji di sini notifikasinya, bukan pembuatan pesanannya.
func pesananUji(t *testing.T, pool *pgxpool.Pool) (orderID, customerID uuid.UUID, nomor, telepon string) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	telepon = "0811" + suffix
	nomor = "ICE-UJI-" + suffix

	var depotID, areaID, slotID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Notif', 1.4748, 124.8421, 8) RETURNING id`,
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
		VALUES ($1, current_date + 1, '08:00', '11:00', 20, now() + interval '20 hours')
		RETURNING id`, areaID).Scan(&slotID)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO customers (phone, name) VALUES ($1, 'Pelanggan Notif')
		RETURNING id`, telepon).Scan(&customerID)
	if err != nil {
		t.Fatalf("menyiapkan pelanggan: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, customer_id, depot_id, service_area_id,
		       recipient_name, recipient_phone, address_line, latitude, longitude,
		       slot_id, scheduled_date, status, subtotal_cents, delivery_fee_cents,
		       total_cents)
		VALUES ($1, $2, $3, $4, 'Penerima', $5, 'Jl. Uji', 1.475, 124.843, $6,
		        current_date + 1, 'WAITING_PAYMENT', 10000, 15000, 25000)
		RETURNING id`, nomor, customerID, depotID, areaID, telepon, slotID).Scan(&orderID)
	if err != nil {
		t.Fatalf("menyiapkan pesanan: %v", err)
	}
	return orderID, customerID, nomor, telepon
}

// setEvent menetapkan pengaturan satu event langsung ke basis data.
func setEvent(t *testing.T, pool *pgxpool.Pool, event string, aktif bool, kanal ...string) {
	t.Helper()
	// Variadic tanpa argumen bernilai nil, dan nil masuk sebagai NULL yang
	// ditolak kolomnya. Yang dimaksud "tanpa kanal" adalah senarai kosong.
	if kanal == nil {
		kanal = []string{}
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO notification_settings (event, enabled, channels)
		VALUES ($1, $2, $3)
		ON CONFLICT (event) DO UPDATE
		SET enabled = excluded.enabled, channels = excluded.channels`,
		event, aktif, kanal)
	if err != nil {
		t.Fatalf("menetapkan pengaturan event: %v", err)
	}
}

// barisNotifikasi membaca notifikasi sebuah pesanan.
func barisNotifikasi(t *testing.T, svc *notify.Service, orderID uuid.UUID) []notify.Notification {
	t.Helper()
	out, err := svc.List(context.Background(), notify.ListFilter{OrderID: &orderID})
	if err != nil {
		t.Fatalf("membaca notifikasi: %v", err)
	}
	return out
}
