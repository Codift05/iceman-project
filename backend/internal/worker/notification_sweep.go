package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// NotificationSweepArgs adalah masukan job penyapu notifikasi tertunda.
//
// Notifikasi diantre dalam transaksi yang sama dengan perubahan yang
// memicunya, jadi dalam keadaan normal tidak ada yang tertinggal. Penyapu ini
// untuk keadaan yang tidak normal: proses yang mati setelah commit namun
// sebelum antreannya terbaca pekerja, atau lingkungan yang penyambung
// antreannya belum disetel.
//
// Tanpa penyapu, notifikasi semacam itu tetap berstatus menunggu selamanya,
// dan tidak ada yang tahu sampai pelanggan mengeluh tidak menerima kabar.
type NotificationSweepArgs struct {
	// OlderThanMinutes membatasi penyapuan pada notifikasi yang sudah cukup
	// lama tertunda. Nol berarti lima menit.
	//
	// Jeda itu perlu supaya penyapu tidak berlomba dengan pekerja yang sedang
	// mengerjakan notifikasi yang baru saja diantre.
	OlderThanMinutes int `json:"older_than_minutes"`
}

// Kind adalah nama job pada antrean.
func (NotificationSweepArgs) Kind() string { return "notification_sweep" }

// InsertOpts membatasi percobaan ulang.
func (NotificationSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: QueueNotification}
}

// NotificationSweepWorker mengantre ulang notifikasi yang tertinggal.
type NotificationSweepWorker struct {
	river.WorkerDefaults[NotificationSweepArgs]
	Deps
}

// Work mencari notifikasi tertunda lalu mengantrekannya.
//
// Yang disapu hanya yang berstatus menunggu. Yang gagal tidak ikut, karena
// kegagalan sudah ditangani percobaan ulang jobnya sendiri, dan mengantrekan
// ulang di sini akan melipatgandakan percobaan tanpa batas.
func (w *NotificationSweepWorker) Work(ctx context.Context, job *river.Job[NotificationSweepArgs]) error {
	menit := job.Args.OlderThanMinutes
	if menit <= 0 || menit > 1440 {
		menit = 5
	}
	batas := time.Now().Add(-time.Duration(menit) * time.Minute)

	rows, err := w.Pool.Query(ctx, `
		SELECT id FROM notifications
		WHERE  status = 'PENDING' AND created_at < $1
		ORDER  BY created_at
		LIMIT  500`, batas)
	if err != nil {
		return fmt.Errorf("mencari notifikasi tertunda: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("membaca notifikasi tertunda: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mencari notifikasi tertunda: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	// Dikerjakan langsung di sini, bukan dengan mengantre job baru, karena
	// mengantre dari dalam pekerja memerlukan klien antreannya sendiri dan
	// jumlahnya sudah dibatasi lima ratus. Pengirimannya idempoten, jadi
	// notifikasi yang ternyata sedang dikerjakan pekerja lain tidak terkirim
	// dua kali.
	var terkirim, gagal int
	for _, id := range ids {
		if w.Notifications == nil {
			break
		}
		if err := w.Notifications.Send(ctx, id); err != nil {
			gagal++
			continue
		}
		terkirim++
	}

	w.Log.Info("penyapuan notifikasi tertunda selesai",
		"tertunda", len(ids), "terkirim", terkirim, "gagal", gagal,
		"lebih_lama_dari_menit", menit)
	return nil
}
