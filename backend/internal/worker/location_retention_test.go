package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/iceman/backend/internal/worker"
)

// TestMasaSimpanPosisi_MenyiapkanDanMelepasPartisi menjaga SRS-TRK-004.
//
// Partisi dipakai agar pembersihan dilakukan dengan melepas partisi, bukan
// menghapus baris satu per satu. Uji ini membuat satu partisi lama berisi data
// lalu memastikan jobnya melepasnya, dan memastikan partisi bulan mendatang
// disiapkan.
func TestMasaSimpanPosisi_MenyiapkanDanMelepasPartisi(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	// Partisi untuk bulan yang jauh di masa lalu, lengkap dengan satu baris.
	lama := time.Now().AddDate(0, -6, 0)
	namaLama := "driver_locations_" + lama.Format("2006_01")
	if _, err := pool.Exec(ctx,
		`SELECT ensure_driver_location_partition($1::date)`, lama.Format("2006-01-02")); err != nil {
		t.Fatalf("menyiapkan partisi lama: %v", err)
	}

	var ada bool
	if err := pool.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM pg_class WHERE relname = $1)`, namaLama).Scan(&ada); err != nil {
		t.Fatalf("memeriksa partisi lama: %v", err)
	}
	if !ada {
		t.Fatalf("partisi %s seharusnya terbuat", namaLama)
	}

	client, err := worker.NewWorker(worker.Deps{Pool: pool, Log: diam()}, worker.Options{})
	if err != nil {
		t.Fatalf("membuat pekerja: %v", err)
	}
	if _, err := client.Insert(ctx, worker.LocationRetentionArgs{RetentionDays: 30}, nil); err != nil {
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

	// Partisi lama dilepas.
	tungguPartisi(t, pool, namaLama, false)

	// Partisi tiga bulan ke depan disiapkan, supaya penyebaran yang terlambat
	// tidak membuat pencatatan posisi gagal.
	for i := 0; i <= 3; i++ {
		nama := "driver_locations_" + time.Now().AddDate(0, i, 0).Format("2006_01")
		tungguPartisi(t, pool, nama, true)
	}
}

// TestMasaSimpanPosisi_PartisiBulanIniTidakDilepas menjaga agar rekaman yang
// masih dalam masa simpan tidak ikut terbuang bersama tetangganya di bulan
// yang sama.
func TestMasaSimpanPosisi_PartisiBulanIniTidakDilepas(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	nama := "driver_locations_" + time.Now().Format("2006_01")

	client, err := worker.NewWorker(worker.Deps{Pool: pool, Log: diam()}, worker.Options{})
	if err != nil {
		t.Fatalf("membuat pekerja: %v", err)
	}
	// Masa simpan satu hari sekalipun tidak boleh melepas partisi bulan ini,
	// karena bulan ini belum berakhir.
	if _, err := client.Insert(ctx, worker.LocationRetentionArgs{RetentionDays: 1}, nil); err != nil {
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

	tungguPartisi(t, pool, nama, true)
	// Diberi waktu supaya job benar benar selesai sebelum diperiksa ulang.
	time.Sleep(500 * time.Millisecond)
	var masihAda bool
	if err := pool.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM pg_class WHERE relname = $1)`, nama).Scan(&masihAda); err != nil {
		t.Fatalf("memeriksa partisi: %v", err)
	}
	if !masihAda {
		t.Fatalf("partisi bulan ini %s seharusnya tidak dilepas", nama)
	}
}
