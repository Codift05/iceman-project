package scheduling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
)

// Alasan sebuah slot tidak dapat dipilih. Dikirim ke klien agar pelanggan
// tahu mengapa, bukan sekadar melihat pilihan yang mati (SRS-SCH-001).
const (
	ReasonFull         = "FULL"
	ReasonCutoffPassed = "CUTOFF_PASSED"
	ReasonHoliday      = "HOLIDAY"
)

// Galat domain slot yang perlu dibedakan oleh pemanggil.
var (
	ErrSlotExists       = errors.New("slot pada jendela itu sudah ada")
	ErrCapacityNegative = errors.New("kapasitas tidak boleh negatif")
)

// SlotDate adalah tanggal pengiriman tanpa jam.
//
// Dibungkus sendiri karena time.Time terbit sebagai "2026-10-04T00:00:00Z".
// Klien yang menerimanya sebagai cap waktu akan menggesernya ke zona waktu
// setempat dan berpindah hari, sehingga slot tampil pada tanggal yang salah.
type SlotDate time.Time

// MarshalJSON menerbitkan tanggal sebagai "2006-01-02" saja.
func (d SlotDate) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(d).Format("2006-01-02") + `"`), nil
}

// Time mengembalikan nilai aslinya untuk keperluan perhitungan.
func (d SlotDate) Time() time.Time { return time.Time(d) }

// Slot adalah satu jendela pengiriman beserta kuotanya.
type Slot struct {
	ID            uuid.UUID `json:"id"`
	ServiceAreaID uuid.UUID `json:"service_area_id"`
	Date          SlotDate  `json:"date"`
	WindowStart   string    `json:"window_start"`
	WindowEnd     string    `json:"window_end"`
	Capacity      int32     `json:"capacity"`
	Used          int32     `json:"used"`
	CutoffAt      time.Time `json:"cutoff_at"`
	IsHoliday     bool      `json:"is_holiday"`
}

// Availability adalah tampilan slot untuk pelanggan.
type Availability struct {
	ID          uuid.UUID `json:"id"`
	Date        string    `json:"date"`
	WindowStart string    `json:"window_start"`
	WindowEnd   string    `json:"window_end"`
	Selectable  bool      `json:"selectable"`
	Remaining   *int32    `json:"remaining,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// Slots menangani pengelolaan slot pengiriman.
type Slots struct{ pool *pgxpool.Pool }

// NewSlots membuat pengelola slot.
func NewSlots(pool *pgxpool.Pool) *Slots { return &Slots{pool: pool} }

// SlotInput adalah data pembuatan satu slot.
type SlotInput struct {
	ServiceAreaID uuid.UUID
	Date          time.Time
	WindowStart   string // "08:00"
	WindowEnd     string // "11:00"
	Capacity      int32
	CutoffAt      time.Time
	IsHoliday     bool
}

// Create menambah satu slot. Slot ganda pada area, tanggal, dan jam mulai yang
// sama ditolak basis data (DB-09).
func (s *Slots) Create(ctx context.Context, in SlotInput) (*Slot, error) {
	if in.Capacity < 0 {
		return nil, ErrCapacityNegative
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var x Slot
	var tanggal time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end,
		        capacity, cutoff_at, is_holiday)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, service_area_id, slot_date,
		          to_char(window_start, 'HH24:MI'), to_char(window_end, 'HH24:MI'),
		          capacity, used, cutoff_at, is_holiday`,
		in.ServiceAreaID, in.Date, in.WindowStart, in.WindowEnd,
		in.Capacity, in.CutoffAt, in.IsHoliday).
		Scan(&x.ID, &x.ServiceAreaID, &tanggal, &x.WindowStart, &x.WindowEnd,
			&x.Capacity, &x.Used, &x.CutoffAt, &x.IsHoliday)
	if isUniqueViolation(err) {
		return nil, ErrSlotExists
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan slot: %w", err)
	}
	x.Date = SlotDate(tanggal)

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "delivery_slots", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan slot: %w", err)
	}
	return &x, nil
}

// SetHoliday menandai atau membatalkan hari libur pada sebuah slot.
//
// Menandai hari libur tidak membatalkan pesanan yang sudah ada pada slot itu.
// Pesanan tersebut tetap ada dan menjadi urusan admin untuk ditinjau, karena
// membatalkan pesanan pelanggan adalah keputusan manusia (TC-SCH-06).
func (s *Slots) SetHoliday(ctx context.Context, id uuid.UUID, holiday bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var before bool
	var used int32
	err = tx.QueryRow(ctx,
		`SELECT is_holiday, used FROM delivery_slots WHERE id = $1 FOR UPDATE`, id).
		Scan(&before, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSlotNotFound
	}
	if err != nil {
		return fmt.Errorf("mengunci slot: %w", err)
	}
	if before == holiday {
		return nil
	}

	if _, err := tx.Exec(ctx,
		`UPDATE delivery_slots SET is_holiday = $2 WHERE id = $1`, id, holiday); err != nil {
		return fmt.Errorf("menandai hari libur: %w", err)
	}

	detail := ""
	if holiday && used > 0 {
		detail = fmt.Sprintf("ditandai libur padahal sudah ada %d pesanan, perlu ditinjau admin", used)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "delivery_slots", EntityID: &id, Action: audit.ActionUpdate,
		Before: map[string]any{"is_holiday": before},
		After:  map[string]any{"is_holiday": holiday},
		Detail: detail,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListForArea mengembalikan slot sebuah area pada rentang tanggal, untuk admin.
func (s *Slots) ListForArea(ctx context.Context, areaID uuid.UUID, from time.Time, days int) ([]Slot, error) {
	if days <= 0 || days > 60 {
		days = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, service_area_id, slot_date,
		       to_char(window_start, 'HH24:MI'), to_char(window_end, 'HH24:MI'),
		       capacity, used, cutoff_at, is_holiday
		FROM   delivery_slots
		WHERE  service_area_id = $1
		  AND  slot_date >= $2 AND slot_date < $2::date + $3::int
		ORDER  BY slot_date, window_start`, areaID, from, days)
	if err != nil {
		return nil, fmt.Errorf("membaca slot: %w", err)
	}
	defer rows.Close()

	out := []Slot{}
	for rows.Next() {
		var x Slot
		var tanggal time.Time
		if err := rows.Scan(&x.ID, &x.ServiceAreaID, &tanggal, &x.WindowStart, &x.WindowEnd,
			&x.Capacity, &x.Used, &x.CutoffAt, &x.IsHoliday); err != nil {
			return nil, fmt.Errorf("membaca baris slot: %w", err)
		}
		x.Date = SlotDate(tanggal)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Availability mengembalikan slot yang dilihat pelanggan.
//
// Slot yang tidak dapat dipilih tetap dikirim beserta alasannya. Menyembunyikan
// slot penuh membuat pelanggan mengira layanan tidak tersedia pada hari itu
// (UI/UX Bab 10.1).
func (s *Slots) Availability(ctx context.Context, areaID uuid.UUID, from time.Time, days int, now time.Time) ([]Availability, error) {
	if days <= 0 || days > 30 {
		days = 14
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, slot_date,
		       to_char(window_start, 'HH24:MI'), to_char(window_end, 'HH24:MI'),
		       capacity, used, cutoff_at, is_holiday
		FROM   delivery_slots
		WHERE  service_area_id = $1
		  AND  slot_date >= $2 AND slot_date < $2::date + $3::int
		ORDER  BY slot_date, window_start`, areaID, from, days)
	if err != nil {
		return nil, fmt.Errorf("membaca ketersediaan: %w", err)
	}
	defer rows.Close()

	out := []Availability{}
	for rows.Next() {
		var (
			id             uuid.UUID
			date           time.Time
			ws, we         string
			capacity, used int32
			cutoff         time.Time
			holiday        bool
		)
		if err := rows.Scan(&id, &date, &ws, &we, &capacity, &used, &cutoff, &holiday); err != nil {
			return nil, fmt.Errorf("membaca baris ketersediaan: %w", err)
		}
		a := Availability{ID: id, Date: date.Format("2006-01-02"), WindowStart: ws, WindowEnd: we}
		switch {
		case holiday:
			a.Reason = ReasonHoliday
		case !now.Before(cutoff):
			a.Reason = ReasonCutoffPassed
		case used >= capacity:
			a.Reason = ReasonFull
		default:
			sisa := capacity - used
			a.Selectable = true
			a.Remaining = &sisa
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// NextAvailable mencari slot terdekat yang masih dapat dipilih, dipakai untuk
// menawarkan jalan keluar ketika pilihan pelanggan ditolak.
func (s *Slots) NextAvailable(ctx context.Context, areaID uuid.UUID, now time.Time) (*Availability, error) {
	var (
		id     uuid.UUID
		date   time.Time
		ws, we string
		sisa   int32
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, slot_date,
		       to_char(window_start, 'HH24:MI'), to_char(window_end, 'HH24:MI'),
		       capacity - used
		FROM   delivery_slots
		WHERE  service_area_id = $1
		  AND  NOT is_holiday
		  AND  cutoff_at > $2
		  AND  used < capacity
		ORDER  BY slot_date, window_start
		LIMIT  1`, areaID, now).Scan(&id, &date, &ws, &we, &sisa)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mencari slot terdekat: %w", err)
	}
	return &Availability{
		ID: id, Date: date.Format("2006-01-02"), WindowStart: ws, WindowEnd: we,
		Selectable: true, Remaining: &sisa,
	}, nil
}

// SetCapacity mengubah kapasitas slot dalam transaksinya sendiri.
//
// Pembungkus ini ada agar lapisan HTTP tidak perlu mengurus transaksi.
// Pemanggil yang sudah berada di dalam transaksi, misalnya saat mengubah
// kapasitas bersama perubahan lain, memakai SetCapacity tingkat paket.
func (s *Slots) SetCapacity(ctx context.Context, id uuid.UUID, capacity int32) error {
	if capacity < 0 {
		return ErrCapacityNegative
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := SetCapacity(ctx, tx, id, capacity); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
