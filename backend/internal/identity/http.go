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

// Challenge menerbitkan token tantangan untuk alur masuk dua tahap.
func (s *Service) Challenge(userID uuid.UUID, purpose string) (string, error) {
	return s.signer.IssueChallenge(userID, purpose)
}

// ParseChallenge memeriksa token tantangan dan mengembalikan pengguna yang dituju.
func (s *Service) ParseChallenge(raw, purpose string) (uuid.UUID, error) {
	cl, err := s.signer.ParseChallenge(raw, purpose)
	if err != nil {
		return uuid.Nil, err
	}
	return cl.UserID, nil
}

// UserByID membaca pengguna beserta peran dan cakupan deponya.
func (s *Service) UserByID(ctx context.Context, userID uuid.UUID) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.name, coalesce(u.email, ''), u.role_id, r.code, u.depot_id, u.status
		FROM   users u JOIN roles r ON r.id = u.role_id
		WHERE  u.id = $1`, userID).
		Scan(&u.ID, &u.Name, &u.Email, &u.RoleID, &u.RoleCode, &u.DepotID, &u.Status)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
