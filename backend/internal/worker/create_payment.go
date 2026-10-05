package worker

import (
	"context"
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
//
// Isinya masih kosong. Penyedia pembayaran dan bentuk tagihannya belum
// diputuskan, dan menebaknya sekarang berarti menulis kode yang hampir pasti
// dibongkar. Yang sudah ada sekarang adalah jalur antreannya, lengkap dengan
// sifat transaksionalnya, sehingga domain pembayaran tinggal mengisi
// pemanggilan penyedianya.
type CreatePaymentWorker struct {
	river.WorkerDefaults[CreatePaymentArgs]
	Deps
}

// Work mencatat bahwa tagihan perlu dibuat.
//
// Job ditandai selesai, bukan gagal, karena kegagalan akan membuatnya dicoba
// ulang terus menerus tanpa ada yang berubah. Yang perlu diketahui operasional
// adalah pesanan mana yang menunggu tagihan, dan itu tercatat di sini.
func (w *CreatePaymentWorker) Work(ctx context.Context, job *river.Job[CreatePaymentArgs]) error {
	if job.Args.OrderID == uuid.Nil {
		return fmt.Errorf("pesanan tidak disebutkan")
	}
	w.Log.Info("tagihan pembayaran perlu dibuat",
		"order_id", job.Args.OrderID,
		"total_sen", job.Args.TotalCents,
		"catatan", "penyedia pembayaran belum disambungkan")
	return nil
}
