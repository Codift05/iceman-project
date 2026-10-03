package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Galat yang dipetakan ke kode galat API pada SRS Bab 8.
var (
	ErrCredentialsInvalid = errors.New("kredensial tidak sah")
	ErrAccountLocked      = errors.New("akun terkunci sementara")
	ErrAccountInactive    = errors.New("akun tidak aktif")
	ErrMFARequired        = errors.New("faktor kedua dibutuhkan")
	ErrRefreshInvalid     = errors.New("token penyegar tidak sah")
	ErrRefreshReused      = errors.New("token penyegar dipakai ulang")
)

// Ambang penguncian mengikuti SRS-AUT-005.
const (
	MaxFailedAttempts = 5
	LockDuration      = 15 * time.Minute
)

// mfaRoles adalah peran yang wajib melewati faktor kedua (SEC-003).
var mfaRoles = map[string]bool{"SUPER_ADMIN": true, "FINANCE": true}

// User adalah pengguna internal beserta peran dan cakupan deponya.
type User struct {
	ID       uuid.UUID
	Name     string
	Email    string
	RoleID   uuid.UUID
	RoleCode string
	DepotID  *uuid.UUID
	Status   string
}

// Service menangani autentikasi, sesi, dan hak akses.
type Service struct {
	pool   *pgxpool.Pool
	signer *Signer
	perms  *permCache
	now    func() time.Time

	roleMu  sync.RWMutex
	roleIDs map[string]uuid.UUID

	// RequireMFA menutup jalur masuk bagi peran berisiko tinggi selama
	// verifikasi faktor kedua belum tersedia. Fails closed: lebih baik
	// menolak daripada memberi akses yang belum lengkap pemeriksaannya.
	RequireMFA bool
}

// NewService membuat layanan identity.
func NewService(pool *pgxpool.Pool, signer *Signer) *Service {
	return &Service{
		pool:       pool,
		signer:     signer,
		perms:      newPermCache(pool),
		now:        time.Now,
		roleIDs:    map[string]uuid.UUID{},
		RequireMFA: true,
	}
}

// Authenticate memeriksa kredensial pengguna internal.
//
// Pesan galat tidak membedakan surel yang tidak dikenal dari kata sandi yang
// salah, agar tidak membocorkan surel mana yang terdaftar (SRS-AUT-005).
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	const q = `
		SELECT u.id, u.name, coalesce(u.email, ''), u.role_id, r.code, u.depot_id,
		       u.status, coalesce(u.password_hash, ''), u.failed_attempts, u.locked_until
		FROM   users u
		JOIN   roles r ON r.id = u.role_id
		WHERE  u.email = $1`

	var (
		u           User
		hash        string
		attempts    int
		lockedUntil *time.Time
	)
	err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Name, &u.Email, &u.RoleID,
		&u.RoleCode, &u.DepotID, &u.Status, &hash, &attempts, &lockedUntil)

	if errors.Is(err, pgx.ErrNoRows) {
		// Tetap jalankan perbandingan agar lamanya jawaban tidak membocorkan
		// apakah surel itu terdaftar.
		_, _ = VerifyPassword(password, dummyHash)
		return nil, ErrCredentialsInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pengguna: %w", err)
	}

	now := s.now()
	if lockedUntil != nil && now.Before(*lockedUntil) {
		return nil, ErrAccountLocked
	}
	if u.Status != "ACTIVE" {
		return nil, ErrAccountInactive
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		if err := s.recordFailure(ctx, u.ID, attempts, now); err != nil {
			return nil, err
		}
		return nil, ErrCredentialsInvalid
	}

	if s.RequireMFA && mfaRoles[u.RoleCode] {
		// Kredensial benar, namun sesi belum diterbitkan sampai faktor kedua
		// diverifikasi. Percobaan gagal tidak ditambahkan karena kata sandinya
		// memang benar.
		return nil, ErrMFARequired
	}

	if err := s.recordSuccess(ctx, u.ID, now); err != nil {
		return nil, err
	}
	return &u, nil
}

// dummyHash dipakai agar perbandingan tetap berjalan untuk surel yang tidak
// terdaftar. Nilainya hash dari kata sandi acak yang tidak dipakai siapa pun.
const dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$" +
	"AAAAAAAAAAAAAAAAAAAAAA$" +
	"ZGVmYXVsdC1kdW1teS1oYXNoLXZhbHVlLXVudXNlZDAwMA"

func (s *Service) recordFailure(ctx context.Context, userID uuid.UUID, attempts int, now time.Time) error {
	next := attempts + 1
	if next >= MaxFailedAttempts {
		_, err := s.pool.Exec(ctx, `
			UPDATE users SET failed_attempts = 0, locked_until = $2 WHERE id = $1`,
			userID, now.Add(LockDuration))
		if err != nil {
			return fmt.Errorf("mengunci akun: %w", err)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET failed_attempts = $2 WHERE id = $1`, userID, next)
	if err != nil {
		return fmt.Errorf("mencatat percobaan gagal: %w", err)
	}
	return nil
}

func (s *Service) recordSuccess(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET    failed_attempts = 0, locked_until = NULL, last_login_at = $2
		WHERE  id = $1`, userID, now)
	if err != nil {
		return fmt.Errorf("mencatat keberhasilan masuk: %w", err)
	}
	return nil
}
