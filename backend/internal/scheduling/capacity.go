package scheduling

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// ErrCapacityBelowUsed muncul bila kapasitas hendak diturunkan di bawah jumlah
// yang sudah terpakai. Menurunkannya berarti membatalkan pesanan yang sudah
// diterima, dan itu keputusan manusia, bukan efek samping penyuntingan angka.
var ErrCapacityBelowUsed = errors.New("kapasitas tidak boleh di bawah jumlah terpakai")

// SetCapacity mengubah kapasitas sebuah slot dan mencatatnya pada jejak audit.
//
// Kapasitas termasuk data kritis menurut BR-005, sehingga perubahannya wajib
// tercatat beserta nilai lama, nilai baru, pelaku, dan waktu. Pencatatan
// dilakukan dalam transaksi yang sama, jadi bila perubahan batal, catatannya
// ikut batal.
func SetCapacity(ctx context.Context, tx pgx.Tx, slotID uuid.UUID, newCapacity int32) error {
	var before SlotState
	before.ID = slotID

	const lockSQL = `
		SELECT capacity, used, cutoff_at, is_holiday
		FROM   delivery_slots
		WHERE  id = $1
		FOR    UPDATE`

	err := tx.QueryRow(ctx, lockSQL, slotID).
		Scan(&before.Capacity, &before.Used, &before.CutoffAt, &before.IsHoliday)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSlotNotFound
	}
	if err != nil {
		return fmt.Errorf("mengunci slot: %w", err)
	}

	if newCapacity < before.Used {
		return ErrCapacityBelowUsed
	}
	if newCapacity == before.Capacity {
		return nil
	}

	if _, err := tx.Exec(ctx,
		`UPDATE delivery_slots SET capacity = $2 WHERE id = $1`, slotID, newCapacity); err != nil {
		return fmt.Errorf("mengubah kapasitas: %w", err)
	}

	return audit.Record(ctx, tx, audit.Entry{
		Entity:   "delivery_slots",
		EntityID: &slotID,
		Action:   audit.ActionUpdate,
		Before:   map[string]any{"capacity": before.Capacity, "used": before.Used},
		After:    map[string]any{"capacity": newCapacity, "used": before.Used},
	})
}
