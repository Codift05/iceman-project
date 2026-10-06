// Package notify mengirim notifikasi untuk event pesanan, pembayaran, dan
// pengiriman.
//
// Kanal notifikasi belum diputuskan (OQ-012 pada Decision Log), dan dua dari
// empat pilihannya memakai WhatsApp yang sudah dikeluarkan dari lingkup.
// Karena itu seluruh aturan di paket ini dibuat tidak bergantung pada kanal:
// pengiriman dilakukan lewat antarmuka Channel, dan kanal bawaannya hanya
// mencatat. Menyambungkan kanal sungguhan berarti menambah satu implementasi
// antarmuka, bukan mengubah logikanya.
//
// Satu sifat berlaku di seluruh paket ini: kegagalan notifikasi tidak boleh
// menggagalkan transaksi inti (SRS-NOT-001, BR-010). Pesanan yang sudah dibuat
// tetap sah walau pemberitahuannya tidak sampai.
package notify

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Event yang dikenali.
//
// Daftarnya diambil dari tabel transisi SRS Bab 5.1 dan 5.3, yaitu perpindahan
// yang menyebut "Antre notifikasi" sebagai efek samping wajib, ditambah
// pengingat pembelian berulang yang disebut SRS-NOT-001.
const (
	EventOrderPaid           = "ORDER_PAID"
	EventOrderOutForDelivery = "ORDER_OUT_FOR_DELIVERY"
	EventOrderCompleted      = "ORDER_COMPLETED"
	EventOrderCancelled      = "ORDER_CANCELLED"
	EventDeliveryFailed      = "DELIVERY_FAILED"
	EventReorderReminder     = "REORDER_REMINDER"
)

// Kanal yang dikenali.
//
// ChannelLog mencatat notifikasi tanpa mengirimnya ke mana pun. Itu kanal
// bawaan selama OQ-012 belum diputuskan, dan tetap berguna sesudahnya sebagai
// kanal pembanding saat menelusuri notifikasi yang tidak sampai.
const (
	ChannelLog      = "LOG"
	ChannelPush     = "PUSH"
	ChannelEmail    = "EMAIL"
	ChannelSMS      = "SMS"
	ChannelWhatsApp = "WHATSAPP"
)

// Status percobaan pengiriman.
const (
	StatusPending = "PENDING"
	StatusSent    = "SENT"
	StatusFailed  = "FAILED"
	// StatusSkipped dipakai bila eventnya dimatikan admin atau tidak punya
	// penerima. Dicatat, bukan dibuang, supaya pemantauan dapat membedakan
	// notifikasi yang sengaja tidak dikirim dari yang gagal terkirim.
	StatusSkipped = "SKIPPED"
)

// Galat domain notifikasi.
var (
	ErrNotFound       = errors.New("notifikasi tidak ditemukan")
	ErrEventUnknown   = errors.New("jenis event tidak dikenali")
	ErrChannelUnknown = errors.New("kanal tidak dikenali")
)

// KnownEvents mengembalikan seluruh event yang dikenali.
func KnownEvents() []string {
	return []string{
		EventOrderPaid, EventOrderOutForDelivery, EventOrderCompleted,
		EventOrderCancelled, EventDeliveryFailed, EventReorderReminder,
	}
}

// KnownChannels mengembalikan seluruh kanal yang dikenali.
func KnownChannels() []string {
	return []string{ChannelLog, ChannelPush, ChannelEmail, ChannelSMS, ChannelWhatsApp}
}

// ValidEvent menjawab apakah sebuah nilai adalah event yang dikenali.
func ValidEvent(e string) bool { return mengandung(KnownEvents(), e) }

// ValidChannel menjawab apakah sebuah nilai adalah kanal yang dikenali.
func ValidChannel(c string) bool { return mengandung(KnownChannels(), c) }

func mengandung(daftar []string, v string) bool {
	for _, x := range daftar {
		if x == v {
			return true
		}
	}
	return false
}

// Setting adalah pengaturan satu jenis event.
type Setting struct {
	Event    string   `json:"event"`
	Enabled  bool     `json:"enabled"`
	Channels []string `json:"channels"`
}

// Notification adalah satu percobaan pengiriman yang tercatat.
type Notification struct {
	ID         uuid.UUID  `json:"id"`
	Event      string     `json:"event"`
	Channel    string     `json:"channel"`
	Recipient  string     `json:"recipient,omitempty"`
	OrderID    *uuid.UUID `json:"order_id,omitempty"`
	CustomerID *uuid.UUID `json:"customer_id,omitempty"`
	Title      string     `json:"title,omitempty"`
	Body       string     `json:"body,omitempty"`
	Status     string     `json:"status"`
	Attempt    int        `json:"attempt"`
	LastError  string     `json:"last_error,omitempty"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Message adalah notifikasi yang siap dikirim lewat sebuah kanal.
type Message struct {
	Event      string
	Channel    string
	Recipient  string
	Title      string
	Body       string
	OrderID    *uuid.UUID
	CustomerID *uuid.UUID
}
