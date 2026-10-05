// Command worker menjalankan pekerjaan latar Iceman Apps.
//
// Dipisahkan dari proses API agar pekerjaan berat tidak memakan waktu tanggap
// permintaan pelanggan, dan agar keduanya dapat diperbanyak sendiri sendiri
// sesuai beban masing masing.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/riverqueue/river"

	"github.com/iceman/backend/internal/store"
	"github.com/iceman/backend/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Error("DATABASE_URL belum diisi. Salin .env.example menjadi .env lebih dahulu.")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Migrasi aplikasi tidak dijalankan di sini. Itu tugas proses API, supaya
	// tidak ada dua proses yang mengubah skema bersamaan saat penyebaran.
	pool, err := store.Connect(ctx, dsn)
	if err != nil {
		log.Error("koneksi basis data gagal", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := worker.Migrate(ctx, pool); err != nil {
		log.Error("migrasi antrean gagal", "error", err)
		os.Exit(1)
	}
	log.Info("migrasi antrean diterapkan")

	maxWorkers := envInt("WORKER_CONCURRENCY", 5)
	client, err := worker.NewWorker(worker.Deps{Pool: pool, Log: log}, worker.Options{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: maxWorkers},
		},
		DailyGeneration: true,
	})
	if err != nil {
		log.Error("membuat pekerja gagal", "error", err)
		os.Exit(1)
	}

	if err := client.Start(ctx); err != nil {
		log.Error("pekerja gagal dijalankan", "error", err)
		os.Exit(1)
	}
	log.Info("pekerja berjalan", "antrean", river.QueueDefault, "pekerja_maksimum", maxWorkers)

	<-ctx.Done()
	log.Info("mematikan pekerja")

	// Job yang sedang berjalan diberi waktu untuk selesai. Mematikannya di
	// tengah jalan membuat job itu dicoba ulang, dan pengulangan hanya aman
	// untuk job yang idempoten.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Stop(shutdownCtx); err != nil {
		log.Error("gagal mematikan pekerja dengan rapi", "error", err)
	}
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}
