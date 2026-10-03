// Package scheduling menangani area layanan, slot pengiriman, dan kuota kapasitas.
//
// Inti paket ini adalah Reserve: menambah satu kuota terpakai pada sebuah slot
// dengan cara yang tetap benar walau beberapa permintaan datang bersamaan.
// Pemeriksaan ketersediaan tanpa kunci baris akan kebobolan, berapapun cepatnya
// bahasa yang dipakai, karena dua transaksi dapat membaca nilai yang sama
// sebelum salah satunya menulis. Lihat Architecture Bab 8.1 dan SRS-ORD-002.
package scheduling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Galat yang dipetakan ke kode galat API pada SRS Bab 8.
var (
	ErrSlotNotFound     = errors.New("slot tidak ditemukan")
	ErrSlotUnavailable  = errors.New("slot tidak tersedia")   // hari libur atau di luar hari operasional
	ErrSlotCutoffPassed = errors.New("batas pemesanan lewat") // melewati cutoff_at
	ErrSlotFull         = errors.New("kuota slot penuh")
)

// SlotState adalah keadaan slot pada saat baris dikunci.
type SlotState struct {
	ID        uuid.UUID
	Capacity  int32
	Used      int32
	CutoffAt  time.Time
	IsHoliday bool
}

// Remaining mengembalikan sisa kuota slot.
func (s SlotState) Remaining() int32 { return s.Capacity - s.Used }

// Reserve mengambil satu kuota pada slot, di dalam transaksi milik pemanggil.
//
// Transaksi sengaja diterima sebagai parameter, bukan dibuat di dalam fungsi,
// karena pengambilan kuota harus menyatu dengan penyimpanan pesanan dan
// pemasukan job ke antrean. Ketiganya berhasil bersama atau gagal bersama.
func Reserve(ctx context.Context, tx pgx.Tx, slotID uuid.UUID, now time.Time) (SlotState, error) {
	var s SlotState
	s.ID = slotID

	// Kunci baris slot. Transaksi lain yang mengincar slot yang sama menunggu
	// di baris ini sampai transaksi berjalan selesai, sehingga nilai used yang
	// dibaca selalu yang terbaru, bukan nilai basi.
	const lockSQL = `
		SELECT capacity, used, cutoff_at, is_holiday
		FROM   delivery_slots
		WHERE  id = $1
		FOR    UPDATE`

	err := tx.QueryRow(ctx, lockSQL, slotID).Scan(&s.Capacity, &s.Used, &s.CutoffAt, &s.IsHoliday)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrSlotNotFound
	}
	if err != nil {
		return s, fmt.Errorf("mengunci slot: %w", err)
	}

	switch {
	case s.IsHoliday:
		return s, ErrSlotUnavailable
	case !now.Before(s.CutoffAt):
		return s, ErrSlotCutoffPassed
	case s.Used >= s.Capacity:
		return s, ErrSlotFull
	}

	// Syarat used < capacity diulang pada UPDATE sebagai pengaman. Bila suatu
	// saat ada jalur kode yang memanggil tanpa mengunci lebih dahulu, baris ini
	// yang mencegah kuota terlampaui.
	const takeSQL = `
		UPDATE delivery_slots
		SET    used = used + 1
		WHERE  id = $1
		  AND  used < capacity`

	tag, err := tx.Exec(ctx, takeSQL, slotID)
	if err != nil {
		return s, fmt.Errorf("menambah kuota terpakai: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s, ErrSlotFull
	}

	s.Used++
	return s, nil
}

// Release mengembalikan satu kuota, dipakai saat pembatalan dan penjadwalan ulang.
// Dijalankan dalam transaksi yang sama dengan perubahan status pesanan (BR-004).
func Release(ctx context.Context, tx pgx.Tx, slotID uuid.UUID) error {
	const sql = `
		UPDATE delivery_slots
		SET    used = used - 1
		WHERE  id = $1
		  AND  used > 0`

	tag, err := tx.Exec(ctx, sql, slotID)
	if err != nil {
		return fmt.Errorf("mengembalikan kuota: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSlotNotFound
	}
	return nil
}

// Move memindahkan kuota dari satu slot ke slot lain untuk penjadwalan ulang.
// Urutan lepas lalu ambil dijalankan dalam satu transaksi, sehingga kuota tidak
// pernah hilang maupun terhitung ganda bila salah satu langkah gagal.
func Move(ctx context.Context, tx pgx.Tx, fromSlot, toSlot uuid.UUID, now time.Time) error {
	if fromSlot == toSlot {
		return nil
	}
	if _, err := Reserve(ctx, tx, toSlot, now); err != nil {
		return err
	}
	return Release(ctx, tx, fromSlot)
}
