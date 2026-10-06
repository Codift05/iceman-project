package worker

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// SendNotificationArgs adalah masukan job pengiriman notifikasi.
//
// Job diantre dalam transaksi yang sama dengan perubahan status yang
// memicunya, sehingga tidak mungkin ada pemberitahuan tentang hal yang
// ternyata batal.
type SendNotificationArgs struct {
	NotificationID uuid.UUID `json:"notification_id"`
}

// Kind adalah nama job pada antrean.
func (SendNotificationArgs) Kind() string { return "send_notification" }

// InsertOpts memberi ruang percobaan ulang dengan jeda bertambah.
//
// SRS-NOT-001 meminta pengiriman yang gagal dicoba ulang dengan jeda bertambah
// sampai batas tertentu. Jedanya diurus River; yang ditetapkan di sini hanya
// batasnya.
//
// Antrean notifikasi dipisahkan dari antrean baku agar lonjakan notifikasi
// tidak menunda pekerjaan yang menyangkut uang dan jadwal.
func (SendNotificationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, Queue: QueueNotification}
}

// SendNotificationWorker mengirim satu notifikasi.
type SendNotificationWorker struct {
	river.WorkerDefaults[SendNotificationArgs]
	Deps
}

// Work mengirim notifikasi.
//
// Kegagalan dikembalikan sebagai galat agar dicoba ulang. Itu aman karena
// pengirimannya idempoten: notifikasi yang sudah terkirim tidak dikirim lagi.
func (w *SendNotificationWorker) Work(ctx context.Context, job *river.Job[SendNotificationArgs]) error {
	if job.Args.NotificationID == uuid.Nil {
		return fmt.Errorf("notifikasi tidak disebutkan")
	}
	if w.Notifications == nil {
		w.Log.Warn("layanan notifikasi belum disetel, notifikasi tidak dikirim",
			"notification_id", job.Args.NotificationID)
		return nil
	}
	if err := w.Notifications.Send(ctx, job.Args.NotificationID); err != nil {
		return fmt.Errorf("mengirim notifikasi: %w", err)
	}
	return nil
}
