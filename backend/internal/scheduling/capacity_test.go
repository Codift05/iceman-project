package scheduling_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/scheduling"
)

func jejak(t *testing.T, pool *pgxpool.Pool, slotID uuid.UUID) []audit.Row {
	t.Helper()
	rows, err := audit.NewReader(pool).List(context.Background(),
		audit.Filter{Entity: "delivery_slots", EntityID: &slotID})
	if err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}
	return rows
}

func TestSetCapacity_TercatatPadaJejakAudit(t *testing.T) {
	pool := newPool(t)
	ctx := audit.WithRequestID(context.Background(), "req-kapasitas-1")
	slotID := seedSlot(t, pool, 20, 5, future(), false)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduling.SetCapacity(ctx, tx, slotID, 30); err != nil {
		t.Fatalf("mengubah kapasitas: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	rows := jejak(t, pool, slotID)
	if len(rows) != 1 {
		t.Fatalf("jejak = %d baris, seharusnya 1", len(rows))
	}
	var before, after map[string]any
	_ = json.Unmarshal(rows[0].Before, &before)
	_ = json.Unmarshal(rows[0].After, &after)
	if before["capacity"] != float64(20) || after["capacity"] != float64(30) {
		t.Fatalf("nilai lama dan baru salah: %v -> %v", before, after)
	}
	if rows[0].RequestID != "req-kapasitas-1" {
		t.Fatalf("request_id = %q, seharusnya terbawa dari konteks", rows[0].RequestID)
	}
}

// Menurunkan kapasitas di bawah jumlah terpakai berarti membatalkan pesanan
// yang sudah diterima. Itu keputusan manusia, bukan efek samping penyuntingan.
func TestSetCapacity_TolakDiBawahTerpakai(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 20, 15, future(), false)

	tx, _ := pool.Begin(ctx)
	err := scheduling.SetCapacity(ctx, tx, slotID, 10)
	if !errors.Is(err, scheduling.ErrCapacityBelowUsed) {
		t.Fatalf("galat = %v, seharusnya ErrCapacityBelowUsed", err)
	}
	_ = tx.Rollback(ctx)

	if used := slotUsed(t, pool, slotID); used != 15 {
		t.Fatalf("kuota terpakai = %d, seharusnya tetap 15", used)
	}
	if n := len(jejak(t, pool, slotID)); n != 0 {
		t.Fatalf("jejak = %d baris, seharusnya 0 karena perubahan ditolak", n)
	}
}

// Inti sifat jejak audit: bila transaksi batal karena langkah lain gagal,
// catatannya ikut batal. Tanpa ini, jejak dapat memuat perubahan yang
// sebenarnya tidak pernah terjadi.
func TestSetCapacity_JejakIkutBatalSaatTransaksiGagal(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 20, 0, future(), false)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduling.SetCapacity(ctx, tx, slotID, 50); err != nil {
		t.Fatalf("mengubah kapasitas: %v", err)
	}
	// Anggap langkah berikutnya gagal, misalnya pemeriksaan hak akses
	// menemukan pelakunya tidak berwenang.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var capacity int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity FROM delivery_slots WHERE id = $1`, slotID).Scan(&capacity); err != nil {
		t.Fatal(err)
	}
	if capacity != 20 {
		t.Fatalf("kapasitas = %d, seharusnya kembali 20", capacity)
	}
	if n := len(jejak(t, pool, slotID)); n != 0 {
		t.Fatalf("jejak = %d baris, seharusnya 0 setelah rollback", n)
	}
}

// Menaikkan kapasitas membuat slot yang tadinya penuh dapat dipesan lagi,
// tanpa rilis ulang aplikasi (BR-001, TC-SCH-04).
func TestSetCapacity_SlotPenuhDapatDipesanLagi(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	slotID := seedSlot(t, pool, 5, 5, future(), false)

	if err := reserveOnce(ctx, pool, slotID); !errors.Is(err, scheduling.ErrSlotFull) {
		t.Fatalf("slot penuh seharusnya ditolak, dapat %v", err)
	}

	tx, _ := pool.Begin(ctx)
	if err := scheduling.SetCapacity(ctx, tx, slotID, 6); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := reserveOnce(ctx, pool, slotID); err != nil {
		t.Fatalf("setelah kapasitas dinaikkan seharusnya bisa dipesan: %v", err)
	}
}
