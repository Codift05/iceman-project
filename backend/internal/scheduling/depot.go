package scheduling

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
)

// Galat domain depo.
var (
	ErrDepotNotFound   = errors.New("depo tidak ditemukan")
	ErrDepotCodeExists = errors.New("kode depo sudah dipakai")
	ErrDepotInUse      = errors.New("depo masih dipakai area layanan aktif")
	ErrNoDepotServes   = errors.New("tidak ada depo yang melayani alamat ini")
	ErrCoordinateRange = errors.New("koordinat di luar rentang yang sah")
)

// Depot adalah satu lokasi operasional Iceman.
type Depot struct {
	ID              uuid.UUID `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Address         string    `json:"address"`
	Latitude        float64   `json:"latitude"`
	Longitude       float64   `json:"longitude"`
	ServiceRadiusKm float64   `json:"service_radius_km"`
	IsActive        bool      `json:"is_active"`
}

// Resolution adalah hasil penentuan depo terdekat.
type Resolution struct {
	Depot      Depot   `json:"depot"`
	DistanceKm float64 `json:"distance_km"`
}

// DepotInput adalah data yang diterima saat membuat atau mengubah depo.
type DepotInput struct {
	Code            string
	Name            string
	Address         string
	Latitude        float64
	Longitude       float64
	ServiceRadiusKm float64
	IsActive        *bool
}

func (in DepotInput) validate() error {
	switch {
	case in.Latitude < -90 || in.Latitude > 90:
		return ErrCoordinateRange
	case in.Longitude < -180 || in.Longitude > 180:
		return ErrCoordinateRange
	case in.ServiceRadiusKm <= 0:
		return fmt.Errorf("radius layanan harus lebih dari nol")
	}
	return nil
}

// Depots menangani pengelolaan depo.
type Depots struct{ pool *pgxpool.Pool }

// NewDepots membuat pengelola depo.
func NewDepots(pool *pgxpool.Pool) *Depots { return &Depots{pool: pool} }

// List mengembalikan seluruh depo, diurutkan menurut kode.
func (d *Depots) List(ctx context.Context, activeOnly bool) ([]Depot, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, code, name, address, latitude, longitude, service_radius_km, is_active
		FROM   depots
		WHERE  (NOT $1 OR is_active)
		ORDER  BY code`, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("membaca depo: %w", err)
	}
	defer rows.Close()

	out := []Depot{}
	for rows.Next() {
		var x Depot
		if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.Address,
			&x.Latitude, &x.Longitude, &x.ServiceRadiusKm, &x.IsActive); err != nil {
			return nil, fmt.Errorf("membaca baris depo: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Get membaca satu depo.
func (d *Depots) Get(ctx context.Context, id uuid.UUID) (*Depot, error) {
	var x Depot
	err := d.pool.QueryRow(ctx, `
		SELECT id, code, name, address, latitude, longitude, service_radius_km, is_active
		FROM   depots WHERE id = $1`, id).
		Scan(&x.ID, &x.Code, &x.Name, &x.Address,
			&x.Latitude, &x.Longitude, &x.ServiceRadiusKm, &x.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDepotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca depo: %w", err)
	}
	return &x, nil
}

// Create menambah depo baru dan mencatatnya pada jejak audit.
func (d *Depots) Create(ctx context.Context, in DepotInput) (*Depot, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}

	var x Depot
	err = tx.QueryRow(ctx, `
		INSERT INTO depots (code, name, address, latitude, longitude, service_radius_km, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, code, name, address, latitude, longitude, service_radius_km, is_active`,
		in.Code, in.Name, in.Address, in.Latitude, in.Longitude, in.ServiceRadiusKm, active).
		Scan(&x.ID, &x.Code, &x.Name, &x.Address,
			&x.Latitude, &x.Longitude, &x.ServiceRadiusKm, &x.IsActive)
	if isUniqueViolation(err) {
		return nil, ErrDepotCodeExists
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan depo: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "depots", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan depo: %w", err)
	}
	return &x, nil
}

// Update mengubah depo. Koordinat dan radius termasuk data kritis karena
// penentuan depo terdekat bergantung padanya, sehingga perubahannya tercatat.
func (d *Depots) Update(ctx context.Context, id uuid.UUID, in DepotInput) (*Depot, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var before Depot
	err = tx.QueryRow(ctx, `
		SELECT id, code, name, address, latitude, longitude, service_radius_km, is_active
		FROM   depots WHERE id = $1 FOR UPDATE`, id).
		Scan(&before.ID, &before.Code, &before.Name, &before.Address,
			&before.Latitude, &before.Longitude, &before.ServiceRadiusKm, &before.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDepotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci depo: %w", err)
	}

	active := before.IsActive
	if in.IsActive != nil {
		active = *in.IsActive
	}

	// Depo yang masih dipakai area layanan aktif tidak boleh dinonaktifkan,
	// karena alamat pelanggan pada area itu akan kehilangan depo pelayannya.
	if before.IsActive && !active {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM service_areas WHERE depot_id = $1 AND is_active`, id).
			Scan(&n); err != nil {
			return nil, fmt.Errorf("memeriksa area layanan: %w", err)
		}
		if n > 0 {
			return nil, ErrDepotInUse
		}
	}

	var after Depot
	err = tx.QueryRow(ctx, `
		UPDATE depots
		SET    name = $2, address = $3, latitude = $4, longitude = $5,
		       service_radius_km = $6, is_active = $7
		WHERE  id = $1
		RETURNING id, code, name, address, latitude, longitude, service_radius_km, is_active`,
		id, in.Name, in.Address, in.Latitude, in.Longitude, in.ServiceRadiusKm, active).
		Scan(&after.ID, &after.Code, &after.Name, &after.Address,
			&after.Latitude, &after.Longitude, &after.ServiceRadiusKm, &after.IsActive)
	if err != nil {
		return nil, fmt.Errorf("mengubah depo: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "depots", EntityID: &id, Action: audit.ActionUpdate,
		Before: before, After: after,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah depo: %w", err)
	}
	return &after, nil
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return err != nil && errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// Resolve menentukan depo yang melayani sebuah koordinat.
//
// Jarak dihitung sebagai jarak garis lurus di permukaan bumi (haversine),
// bukan jarak tempuh. Untuk wilayah kota selisihnya kecil, sedangkan jarak
// tempuh menuntut mesin routing pada setiap pemilihan alamat.
//
// Depo yang dipilih adalah yang terdekat di antara yang alamatnya masih berada
// di dalam radius layanannya. Bila tidak ada, alamat dinyatakan di luar
// wilayah layanan, bukan diarahkan paksa ke depo terjauh (SRS-DEP-002).
func (d *Depots) Resolve(ctx context.Context, lat, lng float64) (*Resolution, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, ErrCoordinateRange
	}

	// Argumen acos dibatasi ke rentang -1 sampai 1. Tanpa pembatasan ini,
	// galat pembulatan bilangan pecahan dapat menghasilkan nilai sedikit di
	// luar rentang dan membuat acos mengembalikan NaN untuk titik yang sangat
	// berdekatan.
	const q = `
		SELECT id, code, name, address, latitude, longitude,
		       service_radius_km, is_active, distance_km
		FROM (
			SELECT d.id, d.code, d.name, d.address, d.latitude, d.longitude,
			       d.service_radius_km, d.is_active,
			       6371 * acos(LEAST(1, GREATEST(-1,
			           cos(radians($1)) * cos(radians(d.latitude)) *
			           cos(radians(d.longitude) - radians($2)) +
			           sin(radians($1)) * sin(radians(d.latitude))
			       ))) AS distance_km
			FROM   depots d
			WHERE  d.is_active
		) kandidat
		WHERE  distance_km <= service_radius_km
		ORDER  BY distance_km
		LIMIT  1`

	var r Resolution
	err := d.pool.QueryRow(ctx, q, lat, lng).
		Scan(&r.Depot.ID, &r.Depot.Code, &r.Depot.Name, &r.Depot.Address,
			&r.Depot.Latitude, &r.Depot.Longitude, &r.Depot.ServiceRadiusKm,
			&r.Depot.IsActive, &r.DistanceKm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoDepotServes
	}
	if err != nil {
		return nil, fmt.Errorf("menentukan depo terdekat: %w", err)
	}
	return &r, nil
}
