// Package delivery mengelola penugasan driver, pembaruan status pengiriman,
// bukti serah terima, dan pelacakan posisi.
//
// Modul driver berjalan di perangkat yang sering kehilangan sinyal, sehingga
// dua hal menjadi bawaan di seluruh paket ini: setiap perintah membawa
// pengenal buatan perangkat agar pengiriman ulang tidak diproses dua kali, dan
// setiap perubahan menyimpan waktu perangkat di samping waktu server.
package delivery

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/notify"
)

// Deps adalah apa yang dibutuhkan pengelola pengiriman.
//
// Berbentuk struct, seperti pada pesanan dan pembayaran, supaya penambahan
// kebutuhan baru tidak mengubah tanda tangan konstruktornya dan memaksa
// seluruh pemanggil disunting.
type Deps struct {
	Pool *pgxpool.Pool
	// Notifier boleh kosong. Bila kosong, notifikasi tidak diantre dan
	// perubahan status pengiriman tetap berhasil, sesuai BR-010.
	Notifier *notify.Service
}

// Deliveries menangani penugasan dan pengelolaan pengiriman.
type Deliveries struct {
	pool     *pgxpool.Pool
	notifier *notify.Service
}

// NewDeliveries membuat pengelola pengiriman.
func NewDeliveries(d Deps) *Deliveries {
	return &Deliveries{pool: d.Pool, notifier: d.Notifier}
}

// Status pengiriman (SRS Bab 5.3).
const (
	StatusAssigned    = "ASSIGNED"
	StatusAccepted    = "ACCEPTED"
	StatusOnTheWay    = "ON_THE_WAY"
	StatusArrived     = "ARRIVED"
	StatusDelivered   = "DELIVERED"
	StatusFailed      = "FAILED_DELIVERY"
	StatusRescheduled = "RESCHEDULED"
)

// Galat domain pengiriman.
var (
	ErrNotFound          = errors.New("pengiriman tidak ditemukan")
	ErrAlreadyAssigned   = errors.New("pesanan ini sudah memiliki penugasan")
	ErrDriverInactive    = errors.New("driver tidak aktif")
	ErrDriverOtherDepot  = errors.New("driver bukan milik depo pesanan ini")
	ErrOrderNotReady     = errors.New("pesanan belum siap ditugaskan")
	ErrInvalidTransition = errors.New("perubahan status pengiriman tidak diizinkan")
	ErrReasonRequired    = errors.New("alasan wajib diisi")
	ErrProofRequired     = errors.New("bukti serah terima wajib diunggah")
	ErrNotAssignedDriver = errors.New("pengiriman ini bukan tugas Anda")
	ErrConsentRequired   = errors.New("persetujuan pelacakan belum diberikan")
	ErrInvalidCoordinate = errors.New("koordinat di luar rentang yang sah")
	// ErrTrackingInactive dibedakan dari ErrInvalidTransition karena pesannya
	// ke driver berbeda: yang perlu ia lakukan bukan memperbaiki status,
	// melainkan menunggu sampai berangkat.
	ErrTrackingInactive = errors.New("pelacakan belum aktif pada status pengiriman ini")
	ErrSyncConflict     = errors.New("status di server sudah lebih baru")
	ErrCategoryRequired = errors.New("kategori kendala wajib dipilih")
)

// Delivery adalah satu penugasan pengiriman.
type Delivery struct {
	ID       uuid.UUID `json:"id"`
	OrderID  uuid.UUID `json:"order_id"`
	OrderNo  string    `json:"order_no"`
	DriverID uuid.UUID `json:"driver_id"`
	DepotID  uuid.UUID `json:"depot_id"`
	Status   string    `json:"status"`

	SequenceNo int32 `json:"sequence_no"`

	// Alamat tujuan disalin dari pesanan saat dibaca, bukan disimpan ulang.
	// Pesanan sudah menyimpan salinannya sendiri, dan menyalinnya dua kali
	// membuat dua sumber kebenaran untuk satu alamat.
	RecipientName  string  `json:"recipient_name"`
	RecipientPhone string  `json:"recipient_phone"`
	AddressLine    string  `json:"address_line"`
	AddressNotes   string  `json:"address_notes,omitempty"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`

	LastLatitude   *float64   `json:"last_latitude,omitempty"`
	LastLongitude  *float64   `json:"last_longitude,omitempty"`
	LastPositionAt *time.Time `json:"last_position_at,omitempty"`
	ETAAt          *time.Time `json:"eta_at,omitempty"`

	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	DepartedAt  *time.Time `json:"departed_at,omitempty"`
	ArrivedAt   *time.Time `json:"arrived_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// StatusEvent adalah satu catatan perubahan status pengiriman.
type StatusEvent struct {
	FromStatus *string    `json:"from_status,omitempty"`
	ToStatus   string     `json:"to_status"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	DeviceTime *time.Time `json:"device_time,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
}

// Assignment adalah satu catatan penugasan atau penugasan ulang.
type Assignment struct {
	FromDriver *uuid.UUID `json:"from_driver,omitempty"`
	ToDriver   uuid.UUID  `json:"to_driver"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
}

// Proof adalah bukti serah terima.
type Proof struct {
	PhotoKey     string     `json:"photo_key"`
	ReceiverName string     `json:"receiver_name,omitempty"`
	Notes        string     `json:"notes,omitempty"`
	DeviceTime   *time.Time `json:"device_time,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Issue adalah kendala yang dilaporkan driver.
type Issue struct {
	ID         uuid.UUID  `json:"id"`
	Category   string     `json:"category"`
	Note       string     `json:"note,omitempty"`
	PhotoKey   string     `json:"photo_key,omitempty"`
	ReportedBy *uuid.UUID `json:"reported_by,omitempty"`
	DeviceTime *time.Time `json:"device_time,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Position adalah satu rekaman posisi driver.
type Position struct {
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	AccuracyM  *float64  `json:"accuracy_m,omitempty"`
	DeviceTime time.Time `json:"device_time"`
	RecordedAt time.Time `json:"recorded_at"`
}
