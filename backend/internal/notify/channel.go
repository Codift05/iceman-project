package notify

import (
	"context"
	"fmt"
	"log/slog"
)

// Channel mengirim notifikasi lewat satu kanal.
//
// Dibuat sebagai antarmuka karena kanal mana yang wajib belum diputuskan.
// Implementasi sungguhan, misalnya push lewat Firebase atau surel lewat SMTP,
// ditambahkan setelah OQ-012 diputuskan tanpa mengubah paket ini.
type Channel interface {
	// Name mengembalikan nama kanal, dicocokkan dengan pengaturan event.
	Name() string
	// Send mengirim satu notifikasi. Galat yang dikembalikan dicatat dan
	// membuat jobnya dicoba ulang.
	Send(ctx context.Context, m Message) error
}

// LogChannel mencatat notifikasi tanpa mengirimnya ke mana pun.
//
// Ini kanal bawaan selama kanal sungguhan belum dipilih. Gunanya bukan
// sekadar penyangga: seluruh jalur notifikasi, mulai dari pengantrean sampai
// pencatatan percobaan, dapat diuji dan dipantau sekarang. Ketika kanal
// sungguhan disambungkan, yang berubah hanya satu implementasi.
type LogChannel struct{ Log *slog.Logger }

// Name mengembalikan nama kanal.
func (LogChannel) Name() string { return ChannelLog }

// Send mencatat notifikasi.
func (c LogChannel) Send(_ context.Context, m Message) error {
	if c.Log == nil {
		return fmt.Errorf("kanal catatan tanpa pencatat")
	}
	c.Log.Info("notifikasi dicatat tanpa dikirim",
		"event", m.Event, "penerima", m.Recipient,
		"judul", m.Title, "order_id", m.OrderID,
		"catatan", "kanal sungguhan belum disambungkan")
	return nil
}

// Registry memetakan nama kanal ke implementasinya.
type Registry struct {
	kanal map[string]Channel
}

// NewRegistry membuat daftar kanal.
//
// Kanal yang tidak terdaftar tidak membuat pengiriman gagal diam diam:
// percobaannya dicatat sebagai gagal beserta alasannya, sehingga terlihat pada
// pemantauan. Pengaturan event dapat menyebut kanal yang implementasinya belum
// ada, dan itu memang terjadi selama OQ-012 belum diputuskan.
func NewRegistry(kanal ...Channel) *Registry {
	m := make(map[string]Channel, len(kanal))
	for _, c := range kanal {
		if c != nil {
			m[c.Name()] = c
		}
	}
	return &Registry{kanal: m}
}

// Get mencari implementasi sebuah kanal.
func (r *Registry) Get(nama string) (Channel, bool) {
	c, ok := r.kanal[nama]
	return c, ok
}

// Names mengembalikan nama kanal yang tersedia.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.kanal))
	for k := range r.kanal {
		out = append(out, k)
	}
	return out
}
