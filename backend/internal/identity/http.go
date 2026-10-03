package identity

import (
	"context"

	"github.com/google/uuid"
)

// RoleIDByCode mencari pengenal peran dari kodenya. Dipakai middleware izin,
// karena token akses hanya membawa kode peran, bukan pengenalnya.
func (s *Service) RoleIDByCode(ctx context.Context, code string) (uuid.UUID, error) {
	s.roleMu.RLock()
	id, ok := s.roleIDs[code]
	s.roleMu.RUnlock()
	if ok {
		return id, nil
	}
	err := s.pool.QueryRow(ctx, `SELECT id FROM roles WHERE code = $1`, code).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	s.roleMu.Lock()
	if s.roleIDs == nil {
		s.roleIDs = map[string]uuid.UUID{}
	}
	s.roleIDs[code] = id
	s.roleMu.Unlock()
	return id, nil
}

// CanCode memeriksa izin berdasarkan kode peran.
func (s *Service) CanCode(ctx context.Context, roleCode, permission string) (bool, error) {
	id, err := s.RoleIDByCode(ctx, roleCode)
	if err != nil {
		return false, err
	}
	return s.Can(ctx, id, permission)
}
