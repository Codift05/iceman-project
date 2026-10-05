package scheduling

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Template adalah satu jendela pengiriman harian beserta kuotanya.
//
// Nilai ini sementara ditetapkan di kode sampai Iceman memutuskan pola jam
// operasional dan cara menghitung kapasitas (OQ-007). Memindahkannya ke tabel
// konfigurasi adalah pekerjaan kecil yang menunggu keputusan itu.
type Template struct {
	WindowStart string
	WindowEnd   string
	Capacity    int32
	// CutoffBefore adalah jarak waktu sebelum jendela mulai, batas terakhir
	// pelanggan masih boleh memesan slot tersebut.
	CutoffBefore time.Duration
}

// DefaultTemplates adalah pola slot harian bawaan: tiga jendela, dengan batas
// pemesanan tiga jam sebelum jendela dimulai.
var DefaultTemplates = []Template{
	{WindowStart: "08:00", WindowEnd: "11:00", Capacity: 20, CutoffBefore: 14 * time.Hour},
	{WindowStart: "11:00", WindowEnd: "14:00", Capacity: 20, CutoffBefore: 3 * time.Hour},
	{WindowStart: "14:00", WindowEnd: "17:00", Capacity: 15, CutoffBefore: 3 * time.Hour},
}

// GenerateInput adalah masukan pembuatan slot.
type GenerateInput struct {
	From      time.Time
	Days      int
	AreaID    *uuid.UUID
	Templates []Template
}

// GenerateResult merangkum hasil pembuatan slot.
type GenerateResult struct {
	Areas   int
	Created int
	Skipped int
}

// Generator membuat slot pengiriman untuk hari hari ke depan.
type Generator struct{ pool *pgxpool.Pool }

// NewGenerator membuat pembangkit slot.
func NewGenerator(pool *pgxpool.Pool) *Generator { return &Generator{pool: pool} }

// EnsureSlots memastikan setiap area aktif memiliki slot untuk rentang hari
// yang diminta.
//
// Operasi ini idempoten. Slot yang sudah ada dilewati lewat ON CONFLICT, bukan
// diperiksa lebih dahulu lalu disisipkan, karena pemeriksaan terpisah membuka
// peluang dua proses menyisipkan slot yang sama secara bersamaan.
func (g *Generator) EnsureSlots(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	if in.Days <= 0 || in.Days > 90 {
		in.Days = 30
	}
	if len(in.Templates) == 0 {
		in.Templates = DefaultTemplates
	}
	if in.From.IsZero() {
		in.From = time.Now()
	}

	rows, err := g.pool.Query(ctx, `
		SELECT a.id
		FROM   service_areas a
		JOIN   depots d ON d.id = a.depot_id
		WHERE  a.is_active AND d.is_active
		  AND  ($1::uuid IS NULL OR a.id = $1)
		ORDER  BY a.id`, in.AreaID)
	if err != nil {
		return nil, fmt.Errorf("membaca area aktif: %w", err)
	}
	var areas []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("membaca baris area: %w", err)
		}
		areas = append(areas, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membaca area aktif: %w", err)
	}

	res := &GenerateResult{Areas: len(areas)}
	// Awal hari dihitung di zona waktu setempat, bukan dengan Truncate, karena
	// Truncate memotong relatif tengah malam UTC. Di WITA itu jatuh pukul
	// delapan pagi, sehingga tanggal slot bisa bergeser sehari.
	start := time.Date(in.From.Year(), in.From.Month(), in.From.Day(), 0, 0, 0, 0, time.Local)

	for _, areaID := range areas {
		for d := 0; d < in.Days; d++ {
			day := start.AddDate(0, 0, d)
			for _, tpl := range in.Templates {
				created, err := g.ensureOne(ctx, areaID, day, tpl)
				if err != nil {
					return nil, err
				}
				if created {
					res.Created++
				} else {
					res.Skipped++
				}
			}
		}
	}
	return res, nil
}

func (g *Generator) ensureOne(ctx context.Context, areaID uuid.UUID, day time.Time, tpl Template) (bool, error) {
	mulai, err := time.Parse("15:04", tpl.WindowStart)
	if err != nil {
		return false, fmt.Errorf("jam mulai %q tidak sah: %w", tpl.WindowStart, err)
	}
	// Batas pemesanan dihitung mundur dari saat jendela dimulai.
	cutoff := time.Date(day.Year(), day.Month(), day.Day(),
		mulai.Hour(), mulai.Minute(), 0, 0, time.Local).Add(-tpl.CutoffBefore)

	tag, err := g.pool.Exec(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end, capacity, cutoff_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (service_area_id, slot_date, window_start) DO NOTHING`,
		areaID, day, tpl.WindowStart, tpl.WindowEnd, tpl.Capacity, cutoff)
	if err != nil {
		return false, fmt.Errorf("menyisipkan slot: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
