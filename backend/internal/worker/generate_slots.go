package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/iceman/backend/internal/scheduling"
)

// GenerateSlotsArgs adalah masukan job pembuatan slot.
type GenerateSlotsArgs struct {
	// Days adalah jumlah hari ke depan yang harus memiliki slot.
	Days int `json:"days"`
	// AreaID membatasi pembuatan pada satu area. Kosong berarti seluruh area aktif.
	AreaID *uuid.UUID `json:"area_id,omitempty"`
}

// Kind adalah nama job pada antrean.
func (GenerateSlotsArgs) Kind() string { return "generate_slots" }

// InsertOpts menandai job ini boleh dicoba ulang beberapa kali.
//
// Tidak ada penguncian keunikan di sini karena pembuatan slot bersifat
// idempoten: menjalankannya dua kali menghasilkan keadaan yang sama.
func (GenerateSlotsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3}
}

// GenerateSlotsWorker membuat slot pengiriman untuk hari hari ke depan.
type GenerateSlotsWorker struct {
	river.WorkerDefaults[GenerateSlotsArgs]
	Deps
}

// Work menjalankan pembuatan slot.
//
// Operasi ini idempoten: slot yang sudah ada dilewati berkat unique index pada
// area, tanggal, dan jam mulai (DB-09). Dengan begitu job dapat dijalankan
// ulang kapan saja tanpa menggandakan slot, termasuk setelah gagal di tengah.
func (w *GenerateSlotsWorker) Work(ctx context.Context, job *river.Job[GenerateSlotsArgs]) error {
	days := job.Args.Days
	if days <= 0 || days > 90 {
		days = 30
	}

	gen := scheduling.NewGenerator(w.Pool)
	hasil, err := gen.EnsureSlots(ctx, scheduling.GenerateInput{
		From:   time.Now(),
		Days:   days,
		AreaID: job.Args.AreaID,
	})
	if err != nil {
		return fmt.Errorf("membuat slot: %w", err)
	}

	w.Log.Info("pembuatan slot selesai",
		"hari", days,
		"area_diproses", hasil.Areas,
		"slot_dibuat", hasil.Created,
		"slot_sudah_ada", hasil.Skipped)
	return nil
}
