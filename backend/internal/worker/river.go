// Package worker menjalankan pekerjaan latar di atas antrean River.
//
// Antrean memakai PostgreSQL yang sama dengan data aplikasi. Itu bukan
// kebetulan: job dapat dimasukkan ke antrean di dalam transaksi yang sama
// dengan perubahan data, sehingga tidak mungkin ada perubahan tersimpan tanpa
// job susulannya, atau sebaliknya (Architecture AD-03).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// Migrate menyiapkan tabel antrean River.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("menyiapkan migrasi antrean: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("menjalankan migrasi antrean: %w", err)
	}
	return nil
}

// NewClient membuat klien River yang hanya dapat memasukkan job ke antrean.
//
// Dipakai oleh proses API, yang tidak menjalankan job sendiri.
func NewClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// ErrChargeNotRetryable menandai kegagalan pembuatan tagihan yang tidak layak
// dicoba ulang, misalnya pesanannya sudah lunas atau sudah batal.
//
// Dibungkus di sisi pemanggil, bukan diperiksa di sini dengan mengenali galat
// domain pembayaran, karena paket ini tidak boleh mengimpor paket pembayaran.
var ErrChargeNotRetryable = errors.New("tagihan tidak dapat dibuat dan tidak perlu dicoba ulang")

// PaymentOps adalah apa yang dibutuhkan pekerja dari domain pembayaran.
//
// Dinyatakan sebagai antarmuka di sini, bukan dengan mengimpor paket
// pembayaran, karena paket pesanan mengimpor paket ini untuk mengantre job.
// Mengimpor pembayaran dari sini akan membentuk lingkaran: pesanan ke pekerja,
// pekerja ke pembayaran, pembayaran ke pesanan. Penyambungnya dipasang di
// titik masuk aplikasi, pola yang sama dengan penyambung identitas pada
// lapisan HTTP.
type PaymentOps interface {
	// Charge membuat tagihan untuk sebuah pesanan. Kegagalan yang tidak layak
	// dicoba ulang dibungkus dengan ErrChargeNotRetryable.
	Charge(ctx context.Context, orderID uuid.UUID) error
	// ApplyEvent memproses satu event webhook yang sudah tersimpan.
	ApplyEvent(ctx context.Context, eventRowID uuid.UUID) error
}

// Deps adalah apa yang dibutuhkan pekerja saat menjalankan job.
type Deps struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Payments boleh kosong. Bila kosong, job yang membutuhkannya hanya
	// mencatat peringatan dan ditandai selesai, sehingga antrean tidak
	// tersumbat pada lingkungan yang belum menyetelnya.
	Payments PaymentOps
}

// Options mengatur bagaimana pekerja dijalankan.
type Options struct {
	// Queues menentukan antrean yang dilayani. Kosong berarti antrean baku
	// dengan lima pekerja serentak.
	Queues map[string]river.QueueConfig
	// DailyGeneration menyalakan penjadwalan harian pembuatan slot. Hanya
	// proses pekerja produksi yang menyalakannya; uji menjalankan jobnya
	// sendiri agar hasilnya pasti.
	DailyGeneration bool
}

// NewWorker membuat klien River yang menjalankan job.
func NewWorker(d Deps, opt Options) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &GenerateSlotsWorker{Deps: d}); err != nil {
		return nil, fmt.Errorf("mendaftarkan pekerja pembuat slot: %w", err)
	}
	if err := river.AddWorkerSafely(workers, &CreatePaymentWorker{Deps: d}); err != nil {
		return nil, fmt.Errorf("mendaftarkan pekerja pembuat tagihan: %w", err)
	}
	if err := river.AddWorkerSafely(workers, &LocationRetentionWorker{Deps: d}); err != nil {
		return nil, fmt.Errorf("mendaftarkan pekerja masa simpan posisi: %w", err)
	}
	if err := river.AddWorkerSafely(workers, &ProcessPaymentEventWorker{Deps: d}); err != nil {
		return nil, fmt.Errorf("mendaftarkan pekerja pemroses event pembayaran: %w", err)
	}

	queues := opt.Queues
	if queues == nil {
		queues = map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
		}
	}

	var periodic []*river.PeriodicJob
	if opt.DailyGeneration {
		// Slot dibuat untuk 30 hari ke depan setiap hari. Tanpa ini, admin
		// harus membuat tiap jendela satu per satu untuk setiap area.
		//
		// River hanya menjalankan penjadwalan ini dari satu proses yang
		// terpilih sebagai pemimpin, jadi menambah pekerja tidak membuat
		// pembuatan slot berjalan berkali kali.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return GenerateSlotsArgs{Days: 30}, nil
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		))

		// Partisi posisi driver disiapkan dan dibersihkan sekali sehari.
		// Dijalankan saat pekerja mulai juga, supaya penyebaran yang terlambat
		// tidak membuat pencatatan posisi gagal karena partisinya belum ada.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return LocationRetentionArgs{RetentionDays: 30}, nil
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	client, err := river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Queues:       queues,
		Workers:      workers,
		Logger:       d.Log,
		PeriodicJobs: periodic,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat klien antrean: %w", err)
	}
	return client, nil
}

// EnqueueTx memasukkan job ke antrean di dalam transaksi pemanggil.
//
// Inilah alasan antrean memakai PostgreSQL yang sama dengan data aplikasi: job
// baru benar benar ada setelah transaksi di-commit. Kalau transaksinya batal,
// job ikut hilang, sehingga tidak ada job yang mengacu pada perubahan yang
// tidak pernah tersimpan (Architecture AD-03).
func EnqueueTx(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, args river.JobArgs) error {
	if _, err := client.InsertTx(ctx, tx, args, nil); err != nil {
		return fmt.Errorf("memasukkan job %s ke antrean: %w", args.Kind(), err)
	}
	return nil
}
