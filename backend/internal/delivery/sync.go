package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Command adalah satu perintah dari perangkat driver.
//
// Perintah dibuat di perangkat, mungkin saat tidak ada jaringan, lalu dikirim
// menyusul. Karena itu ia membawa pengenalnya sendiri dan waktu perangkatnya.
type Command struct {
	// ClientEventID dibuat di perangkat dan menjadi dasar idempotensi (DB-03).
	ClientEventID string    `json:"client_event_id"`
	DeliveryID    uuid.UUID `json:"delivery_id"`
	// Status adalah status tujuan yang diminta.
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	DeviceTime *time.Time `json:"device_time,omitempty"`
}

// CommandResult adalah hasil pemrosesan satu perintah.
type CommandResult struct {
	ClientEventID string `json:"client_event_id"`
	// Outcome bernilai APPLIED atau CONFLICT.
	Outcome string `json:"outcome"`
	// ServerStatus adalah status di server sesudah perintah diproses, atau
	// status yang menjadi acuan ketika perintahnya berkonflik. Klien memakainya
	// untuk menyelaraskan salinan lokalnya.
	ServerStatus string `json:"server_status"`
	Reason       string `json:"reason,omitempty"`
}

// Hasil pemrosesan perintah.
const (
	OutcomeApplied  = "APPLIED"
	OutcomeConflict = "CONFLICT"
)

// Sync memproses sekumpulan perintah dari perangkat driver.
//
// Tiga janji SRS-DLV-006 dijaga di sini:
//
// Pengenal yang sama tidak pernah diproses dua kali. Perintah yang sudah
// pernah masuk dijawab dengan hasil yang tersimpan, bukan dengan galat, karena
// dari sisi perangkat kirimannya memang berhasil dan ia hanya mengulang karena
// belum menerima konfirmasi.
//
// Perintah diproses mengikuti urutan waktu perangkat, bukan urutan kedatangan.
// Antrean lokal dapat terkirim tidak berurutan ketika beberapa permintaan
// berjalan bersamaan, dan memproses "tiba" sebelum "berangkat" akan ditolak
// matriks padahal driver mengerjakannya dengan benar.
//
// Bila status di server sudah lebih baru, perintah ditolak dan status server
// menjadi acuan. Alasan konfliknya disimpan agar dapat ditelusuri.
func (d *Deliveries) Sync(ctx context.Context, driverID uuid.UUID, cmds []Command) ([]CommandResult, error) {
	// Salinan diurutkan menurut waktu perangkat. Perintah tanpa waktu
	// perangkat diletakkan di belakang, karena tidak ada dasar menempatkannya
	// lebih awal.
	urut := make([]Command, len(cmds))
	copy(urut, cmds)
	sort.SliceStable(urut, func(i, j int) bool {
		switch {
		case urut[i].DeviceTime == nil:
			return false
		case urut[j].DeviceTime == nil:
			return true
		default:
			return urut[i].DeviceTime.Before(*urut[j].DeviceTime)
		}
	})

	out := make([]CommandResult, 0, len(urut))
	for _, c := range urut {
		hasil, err := d.satuPerintah(ctx, driverID, c)
		if err != nil {
			return nil, err
		}
		out = append(out, *hasil)
	}
	return out, nil
}

func (d *Deliveries) satuPerintah(ctx context.Context, driverID uuid.UUID, c Command) (*CommandResult, error) {
	if c.ClientEventID == "" {
		return nil, fmt.Errorf("perintah tanpa pengenal perangkat")
	}

	// Perintah yang sudah pernah diproses dijawab dengan hasil tersimpan.
	// Diperiksa lebih dahulu agar pengiriman ulang tidak menyentuh baris
	// pengiriman sama sekali.
	var (
		outcome    string
		konflik    string
		statusKini string
	)
	err := d.pool.QueryRow(ctx, `
		SELECT s.outcome, s.conflict_reason, coalesce(dl.status::text, '')
		FROM   sync_events s
		LEFT JOIN deliveries dl ON dl.id = s.delivery_id
		WHERE  s.client_event_id = $1`, c.ClientEventID).
		Scan(&outcome, &konflik, &statusKini)
	if err == nil {
		return &CommandResult{
			ClientEventID: c.ClientEventID,
			Outcome:       outcome,
			ServerStatus:  statusKini,
			Reason:        konflik,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("memeriksa perintah terdahulu: %w", err)
	}

	pelaku := driverID
	_, errUbah := d.ChangeStatus(ctx, c.DeliveryID, c.Status, ChangeInput{
		ActorID:    &pelaku,
		DeviceTime: c.DeviceTime,
		Reason:     c.Reason,
	})

	hasil := &CommandResult{ClientEventID: c.ClientEventID, Outcome: OutcomeApplied}
	if errUbah != nil {
		// Hanya perpindahan yang tidak sah dihitung sebagai konflik, yaitu
		// keadaan di mana server sudah lebih maju daripada perangkat. Galat
		// lain, misalnya tugas orang lain, bukan konflik sinkronisasi dan
		// harus sampai ke pemanggil apa adanya.
		if !errors.Is(errUbah, ErrInvalidTransition) {
			return nil, errUbah
		}
		hasil.Outcome = OutcomeConflict
		hasil.Reason = errUbah.Error()
	}

	// Status server dibaca sesudahnya, baik perintahnya diterapkan maupun
	// berkonflik, karena itulah acuan yang dipakai klien untuk menyelaraskan
	// salinan lokalnya.
	kini, err := d.Get(ctx, c.DeliveryID)
	if err != nil {
		return nil, err
	}
	hasil.ServerStatus = kini.Status

	_, err = d.pool.Exec(ctx, `
		INSERT INTO sync_events
		       (client_event_id, driver_id, delivery_id, command, device_time,
		        outcome, conflict_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (client_event_id) DO NOTHING`,
		c.ClientEventID, driverID, c.DeliveryID, c.Status, c.DeviceTime,
		hasil.Outcome, hasil.Reason)
	if err != nil {
		return nil, fmt.Errorf("mencatat perintah: %w", err)
	}
	return hasil, nil
}
