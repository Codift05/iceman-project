package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"
)

// LocationRetentionArgs adalah masukan job pengelolaan partisi posisi driver.
type LocationRetentionArgs struct {
	// RetentionDays adalah lama rekaman posisi mentah disimpan. Nol berarti
	// memakai nilai bawaan tiga puluh hari (SRS-TRK-004).
	RetentionDays int `json:"retention_days"`
}

// Kind adalah nama job pada antrean.
func (LocationRetentionArgs) Kind() string { return "location_retention" }

// InsertOpts membatasi percobaan ulang.
func (LocationRetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3}
}

// LocationRetentionWorker menyiapkan partisi bulan berikutnya dan melepas
// partisi yang sudah melewati masa simpan.
//
// Dua pekerjaan ini disatukan karena keduanya menyangkut daftar partisi yang
// sama dan dijalankan pada irama yang sama. Memisahkannya berarti dua job yang
// harus berjalan berurutan tanpa ada yang menjamin urutannya.
type LocationRetentionWorker struct {
	river.WorkerDefaults[LocationRetentionArgs]
	Deps
}

// Work menyiapkan partisi dan melepas yang kedaluwarsa.
//
// Partisi dilepas dengan DROP TABLE, bukan dengan DELETE pada barisnya.
// Melepas partisi hampir seketika dan tidak mengunci tabel induknya,
// sedangkan menghapus puluhan juta baris mengunci dan membengkakkan tabel
// sampai autovacuum menyusulnya (SRS-TRK-004).
func (w *LocationRetentionWorker) Work(ctx context.Context, job *river.Job[LocationRetentionArgs]) error {
	hari := job.Args.RetentionDays
	if hari <= 0 || hari > 365 {
		hari = 30
	}

	// Partisi tiga bulan ke depan disiapkan, bukan hanya bulan berikutnya.
	// Satu bulan saja berarti pencatatan posisi gagal seandainya job ini tidak
	// berjalan selama sebulan, dan kegagalannya baru terlihat saat driver
	// sudah di jalan.
	now := time.Now()
	for i := 0; i <= 3; i++ {
		bulan := now.AddDate(0, i, 0).Format("2006-01-02")
		var nama string
		err := w.Pool.QueryRow(ctx,
			`SELECT ensure_monthly_partition('driver_locations', $1::date)`, bulan).Scan(&nama)
		if err != nil {
			return fmt.Errorf("menyiapkan partisi posisi bulan %s: %w", bulan, err)
		}
	}

	// Partisi yang seluruh rentangnya sudah melewati masa simpan dilepas.
	// Batasnya dihitung dari nama partisi, bukan dari isinya, karena membaca
	// isinya berarti memindai tabel yang justru hendak dibuang.
	batas := now.AddDate(0, 0, -hari)
	rows, err := w.Pool.Query(ctx, `
		SELECT c.relname
		FROM   pg_class c
		JOIN   pg_inherits i ON i.inhrelid = c.oid
		JOIN   pg_class p ON p.oid = i.inhparent
		WHERE  p.relname = 'driver_locations'
		ORDER  BY c.relname`)
	if err != nil {
		return fmt.Errorf("membaca daftar partisi: %w", err)
	}
	var nama []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return fmt.Errorf("membaca nama partisi: %w", err)
		}
		nama = append(nama, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("membaca daftar partisi: %w", err)
	}

	var dilepas []string
	for _, n := range nama {
		bulan, ok := bulanPartisi(n)
		if !ok {
			continue
		}
		// Partisi dilepas hanya bila akhir bulannya pun sudah melewati batas,
		// supaya rekaman yang masih dalam masa simpan tidak ikut terbuang
		// bersama tetangganya di bulan yang sama.
		akhirBulan := bulan.AddDate(0, 1, 0)
		if akhirBulan.After(batas) {
			continue
		}
		if _, err := w.Pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %q`, n)); err != nil {
			return fmt.Errorf("melepas partisi %s: %w", n, err)
		}
		dilepas = append(dilepas, n)
	}

	w.Log.Info("pengelolaan partisi posisi selesai",
		"masa_simpan_hari", hari,
		"partisi_ada", len(nama),
		"partisi_dilepas", dilepas)
	return nil
}

// bulanPartisi menguraikan bulan dari nama partisi, misalnya
// driver_locations_2026_10.
func bulanPartisi(nama string) (time.Time, bool) {
	const awalan = "driver_locations_"
	if len(nama) != len(awalan)+7 || nama[:len(awalan)] != awalan {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006_01", nama[len(awalan):], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
