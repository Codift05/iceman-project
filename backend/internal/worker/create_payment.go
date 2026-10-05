package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// CreatePaymentArgs adalah masukan job pembuatan tagihan pembayaran.
//
// Job ini diantre dalam transaksi yang sama dengan pembuatan pesanan
// (SRS-ORD-002), sehingga tidak mungkin ada pesanan berstatus menunggu
// pembayaran tanpa tagihan yang sedang dibuatkan, maupun tagihan untuk pesanan
// yang ternyata batal.
type CreatePaymentArgs struct {
	OrderID uuid.UUID `json:"order_id"`
	// TotalCents dibawa di dalam job agar nominal tagihan berasal dari nilai
	// yang sama dengan yang dihitung saat pesanan dibuat.
	TotalCents int64 `json:"total_cents"`
}

// Kind adalah nama job pada antrean.
func (CreatePaymentArgs) Kind() string { return "create_payment" }

// InsertOpts membatasi percobaan ulang.
func (CreatePaymentArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5}
}

// CreatePaymentWorker membuat tagihan pembayaran untuk sebuah pesanan.
type CreatePaymentWorker struct {
	river.WorkerDefaults[CreatePaymentArgs]
	Deps
}

// Work memanggil domain pembayaran untuk membuat tagihan.
//
// Tanpa layanan pembayaran yang disetel, job hanya mencatat bahwa tagihan
// perlu dibuat dan ditandai selesai. Itu keadaan pengembangan, bukan produksi,
// dan dicatat sebagai peringatan agar terlihat.
func (w *CreatePaymentWorker) Work(ctx context.Context, job *river.Job[CreatePaymentArgs]) error {
	if job.Args.OrderID == uuid.Nil {
		return fmt.Errorf("pesanan tidak disebutkan")
	}
	if w.Payments == nil {
		w.Log.Warn("layanan pembayaran belum disetel, tagihan tidak dibuat",
			"order_id", job.Args.OrderID, "total_sen", job.Args.TotalCents)
		return nil
	}

	if err := w.Payments.Charge(ctx, job.Args.OrderID); err != nil {
		// Kegagalan penyedia dicoba ulang, karena layanan pembayaran yang
		// sedang terganggu biasanya pulih sendiri. Penolakan karena pesanannya
		// tidak dapat ditagih tidak dicoba ulang, karena tidak ada yang akan
		// berubah: pesanan itu memang sudah lunas atau sudah batal.
		if errors.Is(err, ErrChargeNotRetryable) {
			w.Log.Info("tagihan tidak dibuat karena pesanannya tidak dapat ditagih",
				"order_id", job.Args.OrderID, "alasan", err)
			return nil
		}
		return fmt.Errorf("membuat tagihan: %w", err)
	}

	w.Log.Info("tagihan pembayaran dibuat", "order_id", job.Args.OrderID)
	return nil
}

// ProcessPaymentEventArgs adalah masukan job pemrosesan event webhook.
//
// Pemrosesan dipisahkan dari penerimaan webhook karena penyedia memberi batas
// waktu beberapa detik dan mengirim ulang bila terlampaui (SRS-PAY-002).
type ProcessPaymentEventArgs struct {
	EventRowID uuid.UUID `json:"event_row_id"`
}

// Kind adalah nama job pada antrean.
func (ProcessPaymentEventArgs) Kind() string { return "process_payment_event" }

// InsertOpts memberi ruang percobaan ulang yang cukup.
//
// Pemrosesannya idempoten: event yang sudah selesai dijawab tanpa efek
// samping, jadi percobaan ulang aman. Jedanya bertambah sendiri oleh River,
// sebagaimana diminta SRS-PAY-002.
func (ProcessPaymentEventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 10}
}

// ProcessPaymentEventWorker memproses satu event webhook pembayaran.
type ProcessPaymentEventWorker struct {
	river.WorkerDefaults[ProcessPaymentEventArgs]
	Deps
}

// Work memproses event.
func (w *ProcessPaymentEventWorker) Work(ctx context.Context, job *river.Job[ProcessPaymentEventArgs]) error {
	if job.Args.EventRowID == uuid.Nil {
		return fmt.Errorf("event tidak disebutkan")
	}
	if w.Payments == nil {
		w.Log.Warn("layanan pembayaran belum disetel, event tidak diproses",
			"event_row_id", job.Args.EventRowID)
		return nil
	}
	if err := w.Payments.ApplyEvent(ctx, job.Args.EventRowID); err != nil {
		return fmt.Errorf("memproses event pembayaran: %w", err)
	}
	w.Log.Info("event pembayaran diproses", "event_row_id", job.Args.EventRowID)
	return nil
}
