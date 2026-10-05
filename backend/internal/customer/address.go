package customer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// Address adalah satu alamat pengiriman milik pelanggan.
//
// Alamat terikat pada satu wilayah layanan, karena ongkos kirim dan depo
// pelayannya mengikuti wilayah itu (SRS-DEP-003).
type Address struct {
	ID            uuid.UUID `json:"id"`
	CustomerID    uuid.UUID `json:"customer_id"`
	ServiceAreaID uuid.UUID `json:"service_area_id"`
	Label         string    `json:"label,omitempty"`
	RecipientName string    `json:"recipient_name"`
	Phone         string    `json:"phone"`
	AddressLine   string    `json:"address_line"`
	Notes         string    `json:"notes,omitempty"`
	Latitude      float64   `json:"latitude"`
	Longitude     float64   `json:"longitude"`
	IsPrimary     bool      `json:"is_primary"`
	IsActive      bool      `json:"is_active"`
}

// AddressInput adalah data yang diterima saat menambah atau mengubah alamat.
type AddressInput struct {
	ServiceAreaID uuid.UUID
	Label         string
	RecipientName string
	Phone         string
	AddressLine   string
	Notes         string
	Latitude      float64
	Longitude     float64
}

func (in AddressInput) validate() error {
	switch {
	case strings.TrimSpace(in.RecipientName) == "":
		return fmt.Errorf("nama penerima wajib diisi")
	case strings.TrimSpace(in.Phone) == "":
		return fmt.Errorf("nomor telepon penerima wajib diisi")
	case strings.TrimSpace(in.AddressLine) == "":
		return fmt.Errorf("alamat wajib diisi")
	case in.Latitude < -90 || in.Latitude > 90:
		return ErrCoordRange
	case in.Longitude < -180 || in.Longitude > 180:
		return ErrCoordRange
	}
	return nil
}

const kolomAlamat = `id, customer_id, service_area_id, label, recipient_name,
	       phone, address_line, notes, latitude, longitude, is_primary, is_active`

func pindaiAlamat(row pgx.Row) (*Address, error) {
	var x Address
	err := row.Scan(&x.ID, &x.CustomerID, &x.ServiceAreaID, &x.Label, &x.RecipientName,
		&x.Phone, &x.AddressLine, &x.Notes, &x.Latitude, &x.Longitude,
		&x.IsPrimary, &x.IsActive)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// ListAddresses mengembalikan alamat aktif milik satu pelanggan, alamat utama
// lebih dahulu.
func (c *Customers) ListAddresses(ctx context.Context, customerID uuid.UUID) ([]Address, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT `+kolomAlamat+`
		FROM   customer_addresses
		WHERE  customer_id = $1 AND is_active
		ORDER  BY is_primary DESC, label, created_at`, customerID)
	if err != nil {
		return nil, fmt.Errorf("membaca alamat: %w", err)
	}
	defer rows.Close()

	out := []Address{}
	for rows.Next() {
		x, err := pindaiAlamat(rows)
		if err != nil {
			return nil, fmt.Errorf("membaca baris alamat: %w", err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// GetAddress membaca satu alamat milik pelanggan tertentu.
//
// Kepemilikan menjadi bagian dari query, bukan pemeriksaan terpisah sesudahnya.
// Dengan begitu tidak ada jalur kode yang dapat lupa memeriksanya, dan alamat
// milik pelanggan lain tidak terbaca sama sekali. Alamat orang lain pun
// menghasilkan ErrAddressNotFound, bukan galat yang membedakan "ada tapi bukan
// milikmu" dari "tidak ada", karena perbedaan itu sendiri membocorkan
// keberadaan alamat tersebut.
func (c *Customers) GetAddress(ctx context.Context, customerID, addressID uuid.UUID) (*Address, error) {
	x, err := pindaiAlamat(c.pool.QueryRow(ctx, `
		SELECT `+kolomAlamat+`
		FROM   customer_addresses
		WHERE  id = $1 AND customer_id = $2 AND is_active`, addressID, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAddressNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca alamat: %w", err)
	}
	return x, nil
}

// DeliverableAddress adalah alamat yang sudah dipastikan dapat dikirimi,
// lengkap dengan depo pelayan dan ongkos kirim wilayahnya.
type DeliverableAddress struct {
	Address
	DepotID          uuid.UUID `json:"depot_id"`
	DeliveryFeeCents int64     `json:"delivery_fee_cents"`
}

// ResolveForDelivery membaca alamat beserta depo dan ongkos kirimnya, dan
// menolak bila wilayah layanan atau deponya sudah tidak aktif.
//
// Ini pemeriksaan "alamat milik pelanggan dan area layanan aktif" pada alur
// checkout (SRS-ORD-002). Ongkos kirim dibaca di sini juga supaya checkout
// tidak perlu bertanya dua kali, dan supaya nilai yang dipakai menghitung
// total berasal dari baris yang sama dengan yang baru saja diperiksa.
func (c *Customers) ResolveForDelivery(ctx context.Context, customerID, addressID uuid.UUID) (*DeliverableAddress, error) {
	var (
		x     DeliverableAddress
		aktif bool
	)
	err := c.pool.QueryRow(ctx, `
		SELECT a.id, a.customer_id, a.service_area_id, a.label, a.recipient_name,
		       a.phone, a.address_line, a.notes, a.latitude, a.longitude,
		       a.is_primary, a.is_active,
		       sa.depot_id, sa.delivery_fee_cents,
		       sa.is_active AND d.is_active
		FROM   customer_addresses a
		JOIN   service_areas sa ON sa.id = a.service_area_id
		JOIN   depots d         ON d.id  = sa.depot_id
		WHERE  a.id = $1 AND a.customer_id = $2 AND a.is_active`,
		addressID, customerID).
		Scan(&x.ID, &x.CustomerID, &x.ServiceAreaID, &x.Label, &x.RecipientName,
			&x.Phone, &x.AddressLine, &x.Notes, &x.Latitude, &x.Longitude,
			&x.IsPrimary, &x.IsActive, &x.DepotID, &x.DeliveryFeeCents, &aktif)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAddressNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca alamat pengiriman: %w", err)
	}
	if !aktif {
		return nil, ErrAreaInactive
	}
	return &x, nil
}

// AddAddress menambah alamat untuk pelanggan.
//
// Alamat pertama seorang pelanggan otomatis menjadi alamat utama. Tanpa itu,
// pelanggan dapat memiliki alamat tanpa ada satu pun yang utama, dan checkout
// tidak punya alamat bawaan untuk ditawarkan.
func (c *Customers) AddAddress(ctx context.Context, customerID uuid.UUID, in AddressInput) (*Address, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Pelanggan harus ada. Tanpa pemeriksaan ini, galat yang muncul adalah
	// pelanggaran foreign key yang tidak dapat dibaca pengguna.
	var ada bool
	if err := tx.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM customers WHERE id = $1)`, customerID).Scan(&ada); err != nil {
		return nil, fmt.Errorf("memeriksa pelanggan: %w", err)
	}
	if !ada {
		return nil, ErrNotFound
	}

	var wilayahAktif bool
	err = tx.QueryRow(ctx, `
		SELECT sa.is_active AND d.is_active
		FROM   service_areas sa JOIN depots d ON d.id = sa.depot_id
		WHERE  sa.id = $1`, in.ServiceAreaID).Scan(&wilayahAktif)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAreaInactive
	}
	if err != nil {
		return nil, fmt.Errorf("memeriksa wilayah layanan: %w", err)
	}
	if !wilayahAktif {
		return nil, ErrAreaInactive
	}

	var punyaAlamat bool
	err = tx.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM customer_addresses
			WHERE customer_id = $1 AND is_active AND is_primary)`, customerID).Scan(&punyaAlamat)
	if err != nil {
		return nil, fmt.Errorf("memeriksa alamat utama: %w", err)
	}

	x, err := pindaiAlamat(tx.QueryRow(ctx, `
		INSERT INTO customer_addresses
		       (customer_id, service_area_id, label, recipient_name, phone,
		        address_line, notes, latitude, longitude, is_primary)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+kolomAlamat,
		customerID, in.ServiceAreaID, in.Label, strings.TrimSpace(in.RecipientName),
		strings.TrimSpace(in.Phone), strings.TrimSpace(in.AddressLine), in.Notes,
		in.Latitude, in.Longitude, !punyaAlamat))
	if err != nil {
		return nil, fmt.Errorf("menyimpan alamat: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "customer_addresses", EntityID: &x.ID, Action: audit.ActionCreate, After: x,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan alamat: %w", err)
	}
	return x, nil
}

// SetPrimaryAddress menjadikan satu alamat sebagai alamat utama pelanggan.
//
// Penandaan lama dilepas lebih dahulu dalam pernyataan tersendiri, bukan
// sekaligus dalam satu UPDATE. Indeks keunikan alamat utama diperiksa per baris
// saat baris diperbarui, sehingga satu pernyataan yang menukar penanda dapat
// melanggarnya di tengah jalan walau keadaan akhirnya sah.
func (c *Customers) SetPrimaryAddress(ctx context.Context, customerID, addressID uuid.UUID) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var ada bool
	err = tx.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM customer_addresses
			WHERE id = $1 AND customer_id = $2 AND is_active)`,
		addressID, customerID).Scan(&ada)
	if err != nil {
		return fmt.Errorf("memeriksa alamat: %w", err)
	}
	if !ada {
		return ErrAddressNotFound
	}

	if _, err := tx.Exec(ctx, `
		UPDATE customer_addresses SET is_primary = false
		WHERE  customer_id = $1 AND is_primary`, customerID); err != nil {
		return fmt.Errorf("melepas alamat utama lama: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE customer_addresses SET is_primary = true WHERE id = $1`, addressID); err != nil {
		return fmt.Errorf("menetapkan alamat utama: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "customer_addresses", EntityID: &addressID, Action: audit.ActionUpdate,
		After:  map[string]any{"is_primary": true},
		Detail: "ditetapkan sebagai alamat utama",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeactivateAddress menonaktifkan alamat.
//
// Alamat tidak dihapus karena pesanan historis merujuk padanya. Bila yang
// dinonaktifkan adalah alamat utama, alamat aktif tertua menggantikannya
// supaya pelanggan tidak kehilangan alamat bawaan.
func (c *Customers) DeactivateAddress(ctx context.Context, customerID, addressID uuid.UUID) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE customer_addresses
		SET    is_active = false, is_primary = false
		WHERE  id = $1 AND customer_id = $2 AND is_active
		RETURNING id`, addressID, customerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAddressNotFound
	}
	if err != nil {
		return fmt.Errorf("menonaktifkan alamat: %w", err)
	}

	// Perlu tidaknya alamat utama baru dibaca dari ketiadaan alamat utama
	// sesudah penonaktifan, bukan dari penanda baris yang baru saja dihapus.
	var masihAdaUtama bool
	err = tx.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM customer_addresses
			WHERE customer_id = $1 AND is_active AND is_primary)`, customerID).Scan(&masihAdaUtama)
	if err != nil {
		return fmt.Errorf("memeriksa alamat utama: %w", err)
	}
	if !masihAdaUtama {
		if _, err := tx.Exec(ctx, `
			UPDATE customer_addresses SET is_primary = true
			WHERE  id = (
				SELECT id FROM customer_addresses
				WHERE  customer_id = $1 AND is_active
				ORDER  BY created_at
				LIMIT  1)`, customerID); err != nil {
			return fmt.Errorf("menunjuk alamat utama baru: %w", err)
		}
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "customer_addresses", EntityID: &addressID, Action: audit.ActionUpdate,
		After:  map[string]any{"is_active": false},
		Detail: "alamat dinonaktifkan",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
