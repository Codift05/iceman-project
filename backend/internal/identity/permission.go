package identity

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// permCacheTTL menahan hasil pembacaan izin agar tidak menanyakan basis data
// pada setiap permintaan. Perubahan peran berlaku paling lambat setelah ini.
const permCacheTTL = time.Minute

type permEntry struct {
	codes    map[string]struct{}
	loadedAt time.Time
}

type permCache struct {
	pool   *pgxpool.Pool
	mu     sync.RWMutex
	byRole map[uuid.UUID]permEntry
	now    func() time.Time
}

func newPermCache(pool *pgxpool.Pool) *permCache {
	return &permCache{pool: pool, byRole: map[uuid.UUID]permEntry{}, now: time.Now}
}

func (c *permCache) codes(ctx context.Context, roleID uuid.UUID) (map[string]struct{}, error) {
	c.mu.RLock()
	e, ok := c.byRole[roleID]
	c.mu.RUnlock()
	if ok && c.now().Sub(e.loadedAt) < permCacheTTL {
		return e.codes, nil
	}

	rows, err := c.pool.Query(ctx, `
		SELECT p.code
		FROM   role_permissions rp
		JOIN   permissions p ON p.id = rp.permission_id
		WHERE  rp.role_id = $1`, roleID)
	if err != nil {
		return nil, fmt.Errorf("membaca izin peran: %w", err)
	}
	defer rows.Close()

	set := map[string]struct{}{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("membaca kode izin: %w", err)
		}
		set[code] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membaca izin peran: %w", err)
	}

	c.mu.Lock()
	c.byRole[roleID] = permEntry{codes: set, loadedAt: c.now()}
	c.mu.Unlock()
	return set, nil
}

// Can memeriksa apakah sebuah peran memiliki izin tertentu.
//
// Pemeriksaan dilakukan pada lapisan middleware, bukan tersebar di tiap
// handler, sesuai SRS-AUT-006.
func (s *Service) Can(ctx context.Context, roleID uuid.UUID, permission string) (bool, error) {
	set, err := s.perms.codes(ctx, roleID)
	if err != nil {
		return false, err
	}
	_, ok := set[permission]
	return ok, nil
}

// Permissions mengembalikan seluruh izin sebuah peran.
func (s *Service) Permissions(ctx context.Context, roleID uuid.UUID) ([]string, error) {
	set, err := s.perms.codes(ctx, roleID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for code := range set {
		out = append(out, code)
	}
	return out, nil
}

// InvalidatePermissions membuang cache izin, dipakai setelah peran diubah.
func (s *Service) InvalidatePermissions() {
	s.perms.mu.Lock()
	s.perms.byRole = map[uuid.UUID]permEntry{}
	s.perms.mu.Unlock()
}
