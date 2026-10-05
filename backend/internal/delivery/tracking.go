package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// StaleAfter adalah batas waktu sebuah posisi masih dianggap terpantau.
//
// Driver yang posisinya tidak diperbarui lebih lama dari ini ditandai sebagai
// tidak terpantau, bukan ditampilkan pada posisi usang (SRS-TRK-002).
// Menampilkan posisi lama seolah terkini membuat admin menelepon driver yang
// disangka berhenti, padahal yang hilang hanya sinyalnya.
const StaleAfter = 15 * time.Minute

// Fix adalah satu posisi yang dikirim perangkat driver.
type Fix struct {
	Latitude   float64
	Longitude  float64
	AccuracyM  *float64
	DeviceTime time.Time
}

// RecordResult merangkum hasil penyimpanan sekumpulan posisi.
type RecordResult struct {
	Accepted int `json:"accepted"`
	// Duplicate menghitung posisi yang sudah pernah diterima. Bukan galat:
	// perangkat memang mengirim ulang antreannya dari awal, dan angka ini
	// memberi tahu klien bahwa kirimannya sudah tersimpan sebelumnya.
	Duplicate int `json:"duplicate,omitempty"`
	// Rejected memuat posisi yang dilewati beserta alasannya. Posisi yang
	// rusak tidak membuat seluruh kumpulan gagal, karena satu koordinat buruk
	// dari sensor tidak boleh membuang perjalanan yang sudah terekam.
	Rejected []string `json:"rejected,omitempty"`
}

// RecordPositions menyimpan sekumpulan posisi driver.
//
// Posisi dikirim sebagai kumpulan, bukan satu per satu (SRS-TRK-001), karena
// perangkat merekam setiap sepuluh detik dan satu permintaan HTTP per rekaman
// menghabiskan baterai dan kuota.
//
// Tiga penjaga berlaku: driver harus pemilik tugasnya, persetujuan pelacakan
// harus sudah diberikan, dan pelacakan hanya aktif saat driver sedang menuju
// lokasi atau sudah tiba.
func (d *Deliveries) RecordPositions(ctx context.Context, deliveryID, driverID uuid.UUID, fixes []Fix) (*RecordResult, error) {
	if len(fixes) == 0 {
		return &RecordResult{}, nil
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		status      string
		pemilik     uuid.UUID
		persetujuan *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT d.status::text, d.driver_id, dr.tracking_consent_at
		FROM   deliveries d
		LEFT JOIN drivers dr ON dr.user_id = d.driver_id
		WHERE  d.id = $1 FOR UPDATE OF d`, deliveryID).
		Scan(&status, &pemilik, &persetujuan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pengiriman: %w", err)
	}
	if pemilik != driverID {
		return nil, ErrNotAssignedDriver
	}
	// Persetujuan diperiksa sebelum penyimpanan, bukan sesudah. Posisi yang
	// sudah tersimpan tidak dapat ditarik kembali, jadi persetujuan yang
	// dicabut harus menghentikan perekaman seketika (SEC-012).
	if persetujuan == nil {
		return nil, ErrConsentRequired
	}
	if !TrackingActive(status) {
		return nil, fmt.Errorf("%w: status pengiriman sekarang %s", ErrTrackingInactive, status)
	}

	hasil := &RecordResult{}
	var (
		terbaru     *Fix
		terbaruWakt time.Time
	)

	for i := range fixes {
		f := fixes[i]
		if f.Latitude < -90 || f.Latitude > 90 || f.Longitude < -180 || f.Longitude > 180 {
			hasil.Rejected = append(hasil.Rejected,
				fmt.Sprintf("koordinat ke-%d di luar rentang yang sah", i+1))
			continue
		}
		if f.DeviceTime.IsZero() {
			hasil.Rejected = append(hasil.Rejected,
				fmt.Sprintf("posisi ke-%d tanpa waktu perangkat", i+1))
			continue
		}

		// ON CONFLICT DO NOTHING membuat pengiriman ulang seluruh kumpulan
		// tidak menggandakan rekaman. Perangkat yang kehilangan sinyal
		// mengirim ulang antreannya dari awal, dan antrean itu memuat posisi
		// yang sebagian sudah diterima. Yang menjaganya kekangan keunikan
		// pada (device_time, delivery_id), yaitu kunci alami sebuah posisi.
		tag, err := tx.Exec(ctx, `
			INSERT INTO driver_locations
			       (delivery_id, driver_id, latitude, longitude, accuracy_m, device_time)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (device_time, delivery_id) DO NOTHING`,
			deliveryID, driverID, f.Latitude, f.Longitude, f.AccuracyM, f.DeviceTime)
		if err != nil {
			return nil, fmt.Errorf("menyimpan posisi: %w", err)
		}
		if tag.RowsAffected() == 1 {
			hasil.Accepted++
		} else {
			hasil.Duplicate++
		}

		if terbaru == nil || f.DeviceTime.After(terbaruWakt) {
			terbaru = &f
			terbaruWakt = f.DeviceTime
		}
	}

	// Posisi terakhir disalin ke baris pengiriman agar tetap tersedia setelah
	// rekaman mentah dihapus karena masa simpan (SRS-TRK-004). Hanya diperbarui
	// bila lebih baru, supaya kumpulan yang datang terlambat tidak menarik
	// posisi mundur.
	if terbaru != nil {
		_, err := tx.Exec(ctx, `
			UPDATE deliveries
			SET    last_latitude = $2, last_longitude = $3, last_position_at = $4
			WHERE  id = $1
			  AND  (last_position_at IS NULL OR last_position_at < $4)`,
			deliveryID, terbaru.Latitude, terbaru.Longitude, terbaruWakt)
		if err != nil {
			return nil, fmt.Errorf("menyimpan posisi terakhir: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan posisi: %w", err)
	}
	return hasil, nil
}

// GrantConsent mencatat persetujuan pelacakan seorang driver.
//
// Bentuk persetujuannya disiapkan Iceman sebagai pemberi kerja; yang disimpan
// di sini hanya waktu pemberiannya (SEC-012).
func (d *Deliveries) GrantConsent(ctx context.Context, driverID uuid.UUID) error {
	return d.setConsent(ctx, driverID, true)
}

// RevokeConsent mencabut persetujuan pelacakan.
//
// Pencabutan menghentikan perekaman seketika karena RecordPositions
// memeriksanya setiap kali. Rekaman yang sudah ada tidak dihapus: itu bukti
// pengiriman yang sudah berlangsung, dan masa simpannya diurus pembersihan
// berkala.
func (d *Deliveries) RevokeConsent(ctx context.Context, driverID uuid.UUID) error {
	return d.setConsent(ctx, driverID, false)
}

func (d *Deliveries) setConsent(ctx context.Context, driverID uuid.UUID, setuju bool) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Profil driver dibuat bila belum ada, supaya persetujuan dapat diberikan
	// tanpa langkah pendaftaran terpisah.
	tag, err := tx.Exec(ctx, `
		INSERT INTO drivers (user_id, tracking_consent_at)
		VALUES ($1, CASE WHEN $2 THEN now() ELSE NULL END)
		ON CONFLICT (user_id) DO UPDATE
		SET tracking_consent_at = CASE WHEN $2 THEN now() ELSE NULL END`,
		driverID, setuju)
	if err != nil {
		return fmt.Errorf("menyimpan persetujuan pelacakan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	kata := "persetujuan pelacakan dicabut"
	if setuju {
		kata = "persetujuan pelacakan diberikan"
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "drivers", EntityID: &driverID, Action: audit.ActionUpdate,
		After: map[string]any{"tracking_consent": setuju}, Detail: kata,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HasConsent menjawab apakah driver sudah menyetujui pelacakan.
func (d *Deliveries) HasConsent(ctx context.Context, driverID uuid.UUID) (bool, error) {
	var ada bool
	err := d.pool.QueryRow(ctx, `
		SELECT coalesce(tracking_consent_at IS NOT NULL, false)
		FROM   drivers WHERE user_id = $1`, driverID).Scan(&ada)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("membaca persetujuan pelacakan: %w", err)
	}
	return ada, nil
}

// LivePosition adalah posisi driver untuk ditampilkan pada peta dashboard.
type LivePosition struct {
	DeliveryID uuid.UUID  `json:"delivery_id"`
	OrderNo    string     `json:"order_no"`
	DriverID   uuid.UUID  `json:"driver_id"`
	DriverName string     `json:"driver_name"`
	Status     string     `json:"status"`
	Latitude   *float64   `json:"latitude,omitempty"`
	Longitude  *float64   `json:"longitude,omitempty"`
	PositionAt *time.Time `json:"position_at,omitempty"`
	ETAAt      *time.Time `json:"eta_at,omitempty"`
	// Stale menandai posisi sudah terlalu lama tidak diperbarui. Peta
	// menampilkannya sebagai tidak terpantau, bukan pada posisi usang.
	Stale bool `json:"stale"`
}

// LivePositions mengembalikan posisi driver yang sedang mengantar pada satu
// depo.
//
// Dibatasi per depo karena admin hanya boleh melihat driver dari depo yang
// menjadi kewenangannya (SRS-TRK-002).
//
// Dibaca dari baris pengiriman, bukan dari tabel posisi mentah, karena yang
// dibutuhkan peta hanya posisi terakhir. Membaca tabel mentah berarti satu
// query agregat atas puluhan juta baris setiap kali peta disegarkan.
func (d *Deliveries) LivePositions(ctx context.Context, depotID uuid.UUID, now time.Time) ([]LivePosition, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT d.id, o.order_no, d.driver_id, u.name, d.status::text,
		       d.last_latitude, d.last_longitude, d.last_position_at, d.eta_at
		FROM   deliveries d
		JOIN   orders o ON o.id = d.order_id
		JOIN   users  u ON u.id = d.driver_id
		WHERE  d.depot_id = $1
		  AND  d.status IN ('ON_THE_WAY', 'ARRIVED')
		ORDER  BY d.sequence_no, d.created_at`, depotID)
	if err != nil {
		return nil, fmt.Errorf("membaca posisi driver: %w", err)
	}
	defer rows.Close()

	out := []LivePosition{}
	for rows.Next() {
		var p LivePosition
		err := rows.Scan(&p.DeliveryID, &p.OrderNo, &p.DriverID, &p.DriverName, &p.Status,
			&p.Latitude, &p.Longitude, &p.PositionAt, &p.ETAAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris posisi: %w", err)
		}
		p.Stale = p.PositionAt == nil || now.Sub(*p.PositionAt) > StaleAfter
		out = append(out, p)
	}
	return out, rows.Err()
}

// Trail mengembalikan jejak posisi satu pengiriman.
//
// Dipakai penelusuran sesudah pengiriman selesai, misalnya ketika pelanggan
// mempertanyakan waktu kedatangan. Akses terhadap rekaman posisi tercatat pada
// jejak audit, karena posisi driver adalah data pribadi (SRS-TRK-004).
func (d *Deliveries) Trail(ctx context.Context, deliveryID uuid.UUID, batas int) ([]Position, error) {
	if batas <= 0 || batas > 5000 {
		batas = 1000
	}
	rows, err := d.pool.Query(ctx, `
		SELECT latitude, longitude, accuracy_m, device_time, recorded_at
		FROM   driver_locations
		WHERE  delivery_id = $1
		ORDER  BY device_time
		LIMIT  $2`, deliveryID, batas)
	if err != nil {
		return nil, fmt.Errorf("membaca jejak posisi: %w", err)
	}
	defer rows.Close()

	out := []Position{}
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.Latitude, &p.Longitude, &p.AccuracyM, &p.DeviceTime, &p.RecordedAt); err != nil {
			return nil, fmt.Errorf("membaca baris jejak: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membaca jejak posisi: %w", err)
	}

	if err := audit.RecordOutside(ctx, d.pool, audit.Entry{
		Entity: "driver_locations", EntityID: &deliveryID, Action: audit.ActionAccess,
		Detail: fmt.Sprintf("jejak posisi dibaca, %d titik", len(out)),
	}); err != nil {
		return nil, err
	}
	return out, nil
}
