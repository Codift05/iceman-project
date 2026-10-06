package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
)

// Deps adalah apa yang dibutuhkan layanan notifikasi.
type Deps struct {
	Pool     *pgxpool.Pool
	Channels *Registry
	// Enqueuer boleh kosong. Tanpa itu, notifikasi tetap tercatat berstatus
	// menunggu dan akan diambil penyapu berkala.
	Enqueuer Enqueuer
}

// Service menangani notifikasi.
type Service struct{ d Deps }

// NewService membuat layanan notifikasi.
func NewService(d Deps) *Service {
	if d.Channels == nil {
		d.Channels = NewRegistry()
	}
	return &Service{d: d}
}

// Request adalah permintaan pengiriman notifikasi.
type Request struct {
	Event      string
	Recipient  string
	Title      string
	Body       string
	OrderID    *uuid.UUID
	CustomerID *uuid.UUID
}

// QueueTx mencatat notifikasi yang perlu dikirim, di dalam transaksi pemanggil.
//
// Dipanggil dari tempat perubahan status terjadi, sehingga catatan notifikasi
// ikut batal bila transaksinya batal. Tanpa itu, pesanan yang gagal disimpan
// tetap meninggalkan pemberitahuan kepada pelanggan tentang hal yang tidak
// terjadi.
//
// Yang ditulis di sini hanya catatan berstatus menunggu, satu baris per kanal
// yang aktif. Pengirimannya dikerjakan pekerja latar (SRS-NOT-001), karena
// pengiriman pada jalur permintaan pengguna membuat pembuatan pesanan menunggu
// layanan pihak ketiga.
//
// Mengembalikan pengenal baris yang perlu dikirim. Senarai kosong bukan galat:
// event yang dimatikan admin memang tidak menghasilkan apa pun.
func (s *Service) QueueTx(ctx context.Context, tx pgx.Tx, in Request) ([]uuid.UUID, error) {
	if !ValidEvent(in.Event) {
		return nil, fmt.Errorf("%w: %s", ErrEventUnknown, in.Event)
	}

	setting, err := settingTx(ctx, tx, in.Event)
	if err != nil {
		return nil, err
	}

	// Event yang dimatikan tetap dicatat sekali sebagai dilewati, supaya
	// pemantauan dapat membedakan notifikasi yang sengaja tidak dikirim dari
	// yang hilang tanpa jejak.
	if setting == nil || !setting.Enabled || len(setting.Channels) == 0 {
		alasan := "event dimatikan"
		if setting == nil {
			alasan = "event belum punya pengaturan"
		} else if len(setting.Channels) == 0 {
			alasan = "event tidak punya kanal aktif"
		}
		if _, err := catat(ctx, tx, in, ChannelLog, StatusSkipped, alasan); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Penerima yang kosong tidak membuat pengiriman dicoba terus menerus.
	// Dicatat sebagai dilewati beserta alasannya, karena yang perlu
	// diperbaiki adalah datanya, bukan pengirimannya.
	if strings.TrimSpace(in.Recipient) == "" {
		if _, err := catat(ctx, tx, in, ChannelLog, StatusSkipped,
			"penerima tidak diketahui"); err != nil {
			return nil, err
		}
		return nil, nil
	}

	var ids []uuid.UUID
	for _, kanal := range setting.Channels {
		id, err := catat(ctx, tx, in, kanal, StatusPending, "")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// catat menyisipkan satu baris percobaan notifikasi.
func catat(ctx context.Context, tx pgx.Tx, in Request, kanal, status, alasan string) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO notifications
		       (event, channel, recipient, order_id, customer_id, title, body,
		        status, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		in.Event, kanal, strings.TrimSpace(in.Recipient), in.OrderID, in.CustomerID,
		in.Title, in.Body, status, alasan).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("mencatat notifikasi: %w", err)
	}
	return id, nil
}

// Send mengirim satu notifikasi yang sudah tercatat.
//
// Dipanggil pekerja latar. Galat dikembalikan agar jobnya dicoba ulang dengan
// jeda bertambah, dan setiap percobaan menambah penghitungnya serta menyimpan
// alasan kegagalan terakhir (SRS-NOT-001).
func (s *Service) Send(ctx context.Context, id uuid.UUID) error {
	// Baris dibaca di luar transaksi karena pengiriman memanggil layanan luar
	// dan tidak boleh menahan kunci selama itu.
	var (
		m      Message
		status string
		dibuat time.Time
	)
	err := s.d.Pool.QueryRow(ctx, `
		SELECT event, channel, recipient, title, body, order_id, customer_id,
		       status, created_at
		FROM   notifications WHERE id = $1`, id).
		Scan(&m.Event, &m.Channel, &m.Recipient, &m.Title, &m.Body,
			&m.OrderID, &m.CustomerID, &status, &dibuat)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("membaca notifikasi: %w", err)
	}

	// Sudah terkirim atau sengaja dilewati tidak dikirim ulang. Pekerja dapat
	// mengambil job yang sama dua kali, dan notifikasi ganda membuat pelanggan
	// mengira ada dua pesanan.
	if status != StatusPending && status != StatusFailed {
		return nil
	}

	kanal, ada := s.d.Channels.Get(m.Channel)
	if !ada {
		// Kanal yang implementasinya belum ada ditandai gagal beserta
		// alasannya, bukan dicoba ulang. Mengulangnya tidak akan mengubah
		// apa pun sampai kanalnya disambungkan.
		return s.tandai(ctx, id, dibuat, StatusFailed,
			fmt.Sprintf("kanal %q belum disambungkan", m.Channel), false)
	}

	if errKirim := kanal.Send(ctx, m); errKirim != nil {
		if err := s.tandai(ctx, id, dibuat, StatusFailed, errKirim.Error(), true); err != nil {
			return err
		}
		return fmt.Errorf("mengirim notifikasi lewat %s: %w", m.Channel, errKirim)
	}
	return s.tandai(ctx, id, dibuat, StatusSent, "", true)
}

// tandai memperbarui status satu percobaan notifikasi.
//
// Kolom created_at ikut pada syaratnya karena tabel ini dipartisi menurut
// kolom itu. Tanpa menyebutkannya, PostgreSQL harus memindai seluruh partisi
// untuk menemukan satu baris.
func (s *Service) tandai(ctx context.Context, id uuid.UUID, dibuat time.Time, status, alasan string, tambahPercobaan bool) error {
	tambah := 0
	if tambahPercobaan {
		tambah = 1
	}
	var waktuKirim *time.Time
	if status == StatusSent {
		n := time.Now()
		waktuKirim = &n
	}

	_, err := s.d.Pool.Exec(ctx, `
		UPDATE notifications
		SET    status = $3, last_error = $4, attempt = attempt + $5,
		       sent_at = coalesce(sent_at, $6)
		WHERE  id = $1 AND created_at = $2`,
		id, dibuat, status, alasan, tambah, waktuKirim)
	if err != nil {
		return fmt.Errorf("menandai notifikasi: %w", err)
	}
	return nil
}

// settingTx membaca pengaturan satu event di dalam transaksi pemanggil.
func settingTx(ctx context.Context, tx pgx.Tx, event string) (*Setting, error) {
	var x Setting
	err := tx.QueryRow(ctx,
		`SELECT event, enabled, channels FROM notification_settings WHERE event = $1`,
		event).Scan(&x.Event, &x.Enabled, &x.Channels)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengaturan notifikasi: %w", err)
	}
	return &x, nil
}

// Settings mengembalikan pengaturan seluruh event.
//
// Event yang dikenali namun belum punya baris pengaturan ikut dikembalikan
// dalam keadaan mati, supaya admin melihat daftar lengkap dan tidak perlu
// menebak event apa saja yang ada.
func (s *Service) Settings(ctx context.Context) ([]Setting, error) {
	rows, err := s.d.Pool.Query(ctx,
		`SELECT event, enabled, channels FROM notification_settings`)
	if err != nil {
		return nil, fmt.Errorf("membaca pengaturan notifikasi: %w", err)
	}
	defer rows.Close()

	ada := map[string]Setting{}
	for rows.Next() {
		var x Setting
		if err := rows.Scan(&x.Event, &x.Enabled, &x.Channels); err != nil {
			return nil, fmt.Errorf("membaca baris pengaturan: %w", err)
		}
		ada[x.Event] = x
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membaca pengaturan notifikasi: %w", err)
	}

	out := make([]Setting, 0, len(KnownEvents()))
	for _, e := range KnownEvents() {
		if x, ok := ada[e]; ok {
			out = append(out, x)
			continue
		}
		out = append(out, Setting{Event: e, Enabled: false, Channels: []string{}})
	}
	return out, nil
}

// SetSetting mengubah pengaturan satu event.
//
// Kanal yang tidak dikenali ditolak di sini, bukan dibiarkan sampai pengiriman
// gagal. Admin yang salah menulis nama kanal perlu tahu saat menyimpannya.
func (s *Service) SetSetting(ctx context.Context, in Setting) (*Setting, error) {
	if !ValidEvent(in.Event) {
		return nil, fmt.Errorf("%w: %s", ErrEventUnknown, in.Event)
	}
	bersih := make([]string, 0, len(in.Channels))
	for _, c := range in.Channels {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !ValidChannel(c) {
			return nil, fmt.Errorf("%w: %s", ErrChannelUnknown, c)
		}
		if !mengandung(bersih, c) {
			bersih = append(bersih, c)
		}
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var sebelum Setting
	adaSebelum := true
	err = tx.QueryRow(ctx,
		`SELECT event, enabled, channels FROM notification_settings
		 WHERE event = $1 FOR UPDATE`, in.Event).
		Scan(&sebelum.Event, &sebelum.Enabled, &sebelum.Channels)
	if errors.Is(err, pgx.ErrNoRows) {
		adaSebelum = false
	} else if err != nil {
		return nil, fmt.Errorf("mengunci pengaturan: %w", err)
	}

	var x Setting
	err = tx.QueryRow(ctx, `
		INSERT INTO notification_settings (event, enabled, channels)
		VALUES ($1, $2, $3)
		ON CONFLICT (event) DO UPDATE
		SET    enabled = excluded.enabled, channels = excluded.channels
		RETURNING event, enabled, channels`,
		in.Event, in.Enabled, bersih).
		Scan(&x.Event, &x.Enabled, &x.Channels)
	if err != nil {
		return nil, fmt.Errorf("menyimpan pengaturan notifikasi: %w", err)
	}

	// Perubahan pengaturan tercatat. Notifikasi yang berhenti sampai karena
	// eventnya dimatikan adalah keluhan yang sulit ditelusuri tanpa jejak
	// siapa yang mematikannya dan kapan.
	entry := audit.Entry{
		Entity: "notification_settings", Action: audit.ActionUpdate,
		After:  map[string]any{"event": x.Event, "enabled": x.Enabled, "channels": x.Channels},
		Detail: rinciPengaturan(adaSebelum, sebelum, x),
	}
	if adaSebelum {
		entry.Before = map[string]any{
			"enabled": sebelum.Enabled, "channels": sebelum.Channels,
		}
	}
	if err := audit.Record(ctx, tx, entry); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan pengaturan notifikasi: %w", err)
	}
	return &x, nil
}

func rinciPengaturan(adaSebelum bool, sebelum, sesudah Setting) string {
	if !adaSebelum {
		return fmt.Sprintf("pengaturan event %s dibuat, aktif=%v, kanal=%v",
			sesudah.Event, sesudah.Enabled, sesudah.Channels)
	}
	var bagian []string
	if sebelum.Enabled != sesudah.Enabled {
		kata := map[bool]string{true: "diaktifkan", false: "dimatikan"}
		bagian = append(bagian, "event "+kata[sesudah.Enabled])
	}
	if strings.Join(sebelum.Channels, ",") != strings.Join(sesudah.Channels, ",") {
		bagian = append(bagian, fmt.Sprintf("kanal %v menjadi %v",
			sebelum.Channels, sesudah.Channels))
	}
	if len(bagian) == 0 {
		return "pengaturan event " + sesudah.Event + " disimpan tanpa perubahan"
	}
	return strings.Join(bagian, ", ")
}

// ListFilter menyaring daftar percobaan notifikasi.
type ListFilter struct {
	Status  string
	Event   string
	OrderID *uuid.UUID
	Limit   int
}

// List mengembalikan percobaan notifikasi untuk pemantauan.
//
// Saringan status ada karena yang paling sering dicari adalah yang gagal
// (ERD Bab 7.2 menyediakan indeksnya untuk itu).
func (s *Service) List(ctx context.Context, f ListFilter) ([]Notification, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, event, channel, recipient, order_id, customer_id,
		       title, body, status, attempt, last_error, sent_at, created_at
		FROM   notifications
		WHERE  ($1 = '' OR status = $1)
		  AND  ($2 = '' OR event = $2)
		  AND  ($3::uuid IS NULL OR order_id = $3)
		ORDER  BY created_at DESC
		LIMIT  $4`, strings.ToUpper(f.Status), strings.ToUpper(f.Event), f.OrderID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("membaca daftar notifikasi: %w", err)
	}
	defer rows.Close()

	out := []Notification{}
	for rows.Next() {
		var x Notification
		err := rows.Scan(&x.ID, &x.Event, &x.Channel, &x.Recipient, &x.OrderID,
			&x.CustomerID, &x.Title, &x.Body, &x.Status, &x.Attempt,
			&x.LastError, &x.SentAt, &x.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris notifikasi: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// AvailableChannels mengembalikan kanal yang implementasinya tersedia.
//
// Berbeda dari KnownChannels, yang menyebut seluruh kanal yang namanya
// dikenali. Selama OQ-012 belum diputuskan, pengaturan dapat menyebut kanal
// yang implementasinya belum ada, dan perbedaan itu perlu terlihat agar admin
// tahu mengapa notifikasinya tidak terkirim.
func (s *Service) AvailableChannels() []string { return s.d.Channels.Names() }

// SetEnqueuer memasang penyambung antrean setelah layanan dibuat.
//
// Dibutuhkan proses pekerja, yang memakai klien antreannya sendiri sebagai
// penyambung: kliennya baru ada setelah layanan notifikasi diserahkan
// kepadanya, sehingga keduanya tidak dapat dirangkai dalam satu panggilan.
func (s *Service) SetEnqueuer(e Enqueuer) { s.d.Enqueuer = e }
