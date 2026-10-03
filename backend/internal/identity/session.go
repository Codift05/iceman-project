package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tokens adalah pasangan token yang diberikan kepada klien.
type Tokens struct {
	Access       string
	Refresh      string
	AccessExpiry time.Time
	SessionID    uuid.UUID
}

// IssueTokens membuat sesi baru dan menerbitkan pasangan token.
func (s *Service) IssueTokens(ctx context.Context, u *User, deviceInfo string) (*Tokens, error) {
	plain, hashed, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	now := s.now()

	var sessionID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, refresh_token_hash, device_info, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		u.ID, hashed, deviceInfo, now.Add(RefreshTokenTTL)).Scan(&sessionID)
	if err != nil {
		return nil, fmt.Errorf("membuat sesi: %w", err)
	}

	access, err := s.signer.IssueAccess(u.ID, u.RoleCode, u.DepotID)
	if err != nil {
		return nil, err
	}
	return &Tokens{
		Access:       access,
		Refresh:      plain,
		AccessExpiry: now.Add(AccessTokenTTL),
		SessionID:    sessionID,
	}, nil
}

// Refresh menukar token penyegar dengan pasangan token baru.
//
// Token penyegar hanya boleh dipakai satu kali. Pemakaian ulang dianggap tanda
// token telah dicuri, sehingga seluruh sesi pengguna itu dibatalkan sekaligus
// (SRS-AUT-003). Baris sesi dikunci agar dua permintaan penyegaran yang datang
// bersamaan tidak keduanya dianggap sah.
func (s *Service) Refresh(ctx context.Context, refreshPlain, deviceInfo string) (*Tokens, error) {
	hashed := HashRefreshToken(refreshPlain)
	now := s.now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		sessionID uuid.UUID
		userID    uuid.UUID
		expiresAt time.Time
		usedAt    *time.Time
		revokedAt *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, expires_at, used_at, revoked_at
		FROM   sessions
		WHERE  refresh_token_hash = $1
		FOR    UPDATE`, hashed).
		Scan(&sessionID, &userID, &expiresAt, &usedAt, &revokedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefreshInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("membaca sesi: %w", err)
	}

	// Token yang sudah dipakai atau sudah dibatalkan berarti ada salinan yang
	// beredar. Seluruh sesi pengguna dibatalkan, bukan hanya sesi ini.
	if usedAt != nil || revokedAt != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = $2
			WHERE  user_id = $1 AND revoked_at IS NULL`, userID, now); err != nil {
			return nil, fmt.Errorf("membatalkan seluruh sesi: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("menyimpan pembatalan sesi: %w", err)
		}
		return nil, ErrRefreshReused
	}

	if !now.Before(expiresAt) {
		return nil, ErrRefreshInvalid
	}

	u, err := s.loadUser(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if u.Status != "ACTIVE" {
		return nil, ErrAccountInactive
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET used_at = $2 WHERE id = $1`, sessionID, now); err != nil {
		return nil, fmt.Errorf("menandai sesi terpakai: %w", err)
	}

	plain, newHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	var newSessionID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO sessions (user_id, refresh_token_hash, rotated_from, device_info, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		userID, newHash, sessionID, deviceInfo, now.Add(RefreshTokenTTL)).Scan(&newSessionID)
	if err != nil {
		return nil, fmt.Errorf("membuat sesi pengganti: %w", err)
	}

	access, err := s.signer.IssueAccess(u.ID, u.RoleCode, u.DepotID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan pemutaran sesi: %w", err)
	}

	return &Tokens{
		Access:       access,
		Refresh:      plain,
		AccessExpiry: now.Add(AccessTokenTTL),
		SessionID:    newSessionID,
	}, nil
}

// Logout membatalkan satu sesi berdasarkan token penyegarnya.
func (s *Service) Logout(ctx context.Context, refreshPlain string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = $2
		WHERE  refresh_token_hash = $1 AND revoked_at IS NULL`,
		HashRefreshToken(refreshPlain), s.now())
	if err != nil {
		return fmt.Errorf("membatalkan sesi: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRefreshInvalid
	}
	return nil
}

// ActiveSessions menghitung sesi yang masih berlaku milik seorang pengguna.
func (s *Service) ActiveSessions(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM sessions
		WHERE  user_id = $1 AND revoked_at IS NULL AND used_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("menghitung sesi aktif: %w", err)
	}
	return n, nil
}

func (s *Service) loadUser(ctx context.Context, q pgx.Tx, userID uuid.UUID) (*User, error) {
	var u User
	err := q.QueryRow(ctx, `
		SELECT u.id, u.name, coalesce(u.email, ''), u.role_id, r.code, u.depot_id, u.status
		FROM   users u JOIN roles r ON r.id = u.role_id
		WHERE  u.id = $1`, userID).
		Scan(&u.ID, &u.Name, &u.Email, &u.RoleID, &u.RoleCode, &u.DepotID, &u.Status)
	if err != nil {
		return nil, fmt.Errorf("membaca pengguna: %w", err)
	}
	return &u, nil
}
