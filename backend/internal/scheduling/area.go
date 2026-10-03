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

// Galat domain area layanan.
var (
	ErrAreaNotFound   = errors.New("area layanan tidak ditemukan")
	ErrAreaNameExists = errors.New("nama area sudah dipakai pada depo ini")
	ErrAreaDepotOff   = errors.New("depo tidak aktif")
	ErrFeeNegative    = errors.New("ongkos kirim tidak boleh negatif")
)

// ServiceArea adalah wilayah layanan milik satu depo.
type ServiceArea struct {
	ID               uuid.UUID `json:"id"`
	DepotID          uuid.UUID `json:"depot_id"`
	Name             string    `json:"name"`
	DeliveryFeeCents int64     `json:"delivery_fee_cents"`
	IsActive         bool      `json:"is_active"`
}

// AreaInput adalah data yang diterima saat membuat atau mengubah area.
type AreaInput struct {
	DepotID          uuid.UUID
	Name             string
	DeliveryFeeCents int64
	IsActive         *bool
}

// Areas menangani pengelolaan area layanan.
type Areas struct{ pool *pgxpool.Pool }

// NewAreas membuat pengelola area layanan.
func NewAreas(pool *pgxpool.Pool) *Areas { return &Areas{pool: pool} }

// List mengembalikan area layanan, boleh disaring menurut depo.
func (a *Areas) List(ctx context.Context, depotID *uuid.UUID, activeOnly bool) ([]ServiceArea, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT id, depot_id, name, delivery_fee_cents, is_active
		FROM   service_areas
		WHERE  ($1::uuid IS NULL OR depot_id = $1)
		  AND  (NOT $2 OR is_active)
		ORDER  BY name`, depotID, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("membaca area layanan: %w", err)
	}
	defer rows.Close()

	out := []ServiceArea{}
	for rows.Next() {
		var x ServiceArea
		if err := rows.Scan(&x.ID, &x.DepotID, &x.Name, &x.DeliveryFeeCents, &x.IsActive); err != nil {
			return nil, fmt.Errorf("membaca baris area: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Create menambah area layanan baru.
func (a *Areas) Create(ctx context.Context, in AreaInput) (*ServiceArea, error) {
	if in.DeliveryFeeCents < 0 {
		return nil, ErrFeeNegative
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Area tidak boleh bergantung pada depo yang sudah tidak melayani, karena
	// alamat pelanggan di area itu akan kehilangan depo pelayannya.
	var depotActive bool
	err = tx.QueryRow(ctx, `SELECT is_active FROM depots WHERE id = $1`, in.DepotID).Scan(&depotActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDepotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("memeriksa depo: %w", err)
	}
	if !depotActive {
		return nil, ErrAreaDepotOff
	}

	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}

	var x ServiceArea
	err = tx.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents, is_active)
		VALUES ($1, $2, $3, $4)
		RETURNING id, depot_id, name, delivery_fee_cents, is_active`,
		in.DepotID, in.Name, in.DeliveryFeeCents, active).
		Scan(&x.ID, &x.DepotID, &x.Name, &x.DeliveryFeeCents, &x.IsActive)
	if isUniqueViolation(err) {
		return nil, ErrAreaNameExists
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan area: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "service_areas", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan area: %w", err)
	}
	return &x, nil
}

// Update mengubah area layanan. Ongkos kirim termasuk data kritis karena
// memengaruhi harga yang dibayar pelanggan, sehingga perubahannya tercatat.
//
// Perubahan ongkos kirim tidak mengubah pesanan yang sudah dibuat, karena
// pesanan menyimpan salinan nilainya sendiri (BR-007).
func (a *Areas) Update(ctx context.Context, id uuid.UUID, in AreaInput) (*ServiceArea, error) {
	if in.DeliveryFeeCents < 0 {
		return nil, ErrFeeNegative
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var before ServiceArea
	err = tx.QueryRow(ctx, `
		SELECT id, depot_id, name, delivery_fee_cents, is_active
		FROM   service_areas WHERE id = $1 FOR UPDATE`, id).
		Scan(&before.ID, &before.DepotID, &before.Name, &before.DeliveryFeeCents, &before.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAreaNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci area: %w", err)
	}

	active := before.IsActive
	if in.IsActive != nil {
		active = *in.IsActive
	}
	name := in.Name
	if name == "" {
		name = before.Name
	}

	var after ServiceArea
	err = tx.QueryRow(ctx, `
		UPDATE service_areas
		SET    name = $2, delivery_fee_cents = $3, is_active = $4
		WHERE  id = $1
		RETURNING id, depot_id, name, delivery_fee_cents, is_active`,
		id, name, in.DeliveryFeeCents, active).
		Scan(&after.ID, &after.DepotID, &after.Name, &after.DeliveryFeeCents, &after.IsActive)
	if isUniqueViolation(err) {
		return nil, ErrAreaNameExists
	}
	if err != nil {
		return nil, fmt.Errorf("mengubah area: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "service_areas", EntityID: &id, Action: audit.ActionUpdate,
		Before: before, After: after,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah area: %w", err)
	}
	return &after, nil
}
