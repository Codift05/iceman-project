package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/notify"
	"github.com/iceman/backend/internal/worker"
)

// TestPenyapuNotifikasi_MengambilYangTertinggal menjaga jaring pengaman untuk
// keadaan tidak normal: notifikasi yang tercatat namun jobnya tidak pernah
// terantre, misalnya karena proses mati setelah commit.
//
// Tanpa penyapu, notifikasi semacam itu tetap menunggu selamanya dan tidak ada
// yang tahu sampai pelanggan mengeluh tidak menerima kabar.
func TestPenyapuNotifikasi_MengambilYangTertinggal(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	// Notifikasi tertunda yang sudah cukup lama, dibuat langsung tanpa job.
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO notifications
		       (event, channel, recipient, title, body, status, created_at)
		VALUES ('ORDER_PAID', 'LOG', '0811000000', 'Uji', 'Uji penyapu',
		        'PENDING', now() - interval '30 minutes')
		RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan notifikasi tertunda: %v", err)
	}

	notifier := notify.NewService(notify.Deps{
		Pool:     pool,
		Channels: notify.NewRegistry(notify.LogChannel{Log: diam()}),
	})
	client, err := worker.NewWorker(worker.Deps{
		Pool: pool, Log: diam(), Notifications: notifier,
	}, worker.Options{})
	if err != nil {
		t.Fatalf("membuat pekerja: %v", err)
	}
	if _, err := client.Insert(ctx, worker.NotificationSweepArgs{OlderThanMinutes: 5}, nil); err != nil {
		t.Fatalf("memasukkan job: %v", err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatalf("menjalankan pekerja: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(c)
	})

	tungguStatus(t, pool, id, "SENT")
}

// TestPenyapuNotifikasi_TidakMengambilYangBaru menjaga jedanya: penyapu tidak
// boleh berlomba dengan pekerja yang sedang mengerjakan notifikasi yang baru
// saja diantre.
func TestPenyapuNotifikasi_TidakMengambilYangBaru(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO notifications
		       (event, channel, recipient, title, body, status)
		VALUES ('ORDER_PAID', 'LOG', '0811000000', 'Uji', 'Baru saja', 'PENDING')
		RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan notifikasi baru: %v", err)
	}

	notifier := notify.NewService(notify.Deps{
		Pool:     pool,
		Channels: notify.NewRegistry(notify.LogChannel{Log: diam()}),
	})
	client, err := worker.NewWorker(worker.Deps{
		Pool: pool, Log: diam(), Notifications: notifier,
	}, worker.Options{})
	if err != nil {
		t.Fatalf("membuat pekerja: %v", err)
	}
	if _, err := client.Insert(ctx, worker.NotificationSweepArgs{OlderThanMinutes: 5}, nil); err != nil {
		t.Fatalf("memasukkan job: %v", err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatalf("menjalankan pekerja: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(c)
	})

	// Diberi waktu agar penyapunya benar benar selesai, lalu dipastikan
	// statusnya tidak berubah.
	time.Sleep(1500 * time.Millisecond)
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("membaca status: %v", err)
	}
	if status != "PENDING" {
		t.Fatalf("status %q, notifikasi yang baru seharusnya tidak disapu", status)
	}
}

// tungguStatus menunggu sampai sebuah notifikasi mencapai status tertentu.
func tungguStatus(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, mau string) {
	t.Helper()
	ctx := context.Background()
	batas := time.Now().Add(20 * time.Second)

	var status string
	for time.Now().Before(batas) {
		err := pool.QueryRow(ctx,
			`SELECT status FROM notifications WHERE id = $1`, id).Scan(&status)
		if err != nil {
			t.Fatalf("membaca status notifikasi: %v", err)
		}
		if status == mau {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("status notifikasi %q, seharusnya %q", status, mau)
}
