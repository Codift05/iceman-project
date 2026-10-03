package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Galat faktor kedua.
var (
	ErrMFANotEnrolled  = errors.New("faktor kedua belum didaftarkan")
	ErrMFAAlreadyOn    = errors.New("faktor kedua sudah aktif")
	ErrMFACodeInvalid  = errors.New("kode faktor kedua salah")
	ErrMFACodeReplayed = errors.New("kode faktor kedua sudah pernah dipakai")
)

// Parameter TOTP. Tiga puluh detik per langkah adalah nilai yang dipakai
// hampir seluruh aplikasi autentikator, sehingga pengguna tidak perlu
// pengaturan khusus.
const (
	totpPeriod = 30
	totpDigits = otp.DigitsSix
	totpSkew   = 1 // menerima satu langkah sebelum dan sesudah, menoleransi jam yang sedikit meleset
	mfaIssuer  = "Iceman Apps"
)

// Enrollment adalah data yang diberikan saat pengguna mulai mendaftarkan
// faktor kedua.
type Enrollment struct {
	Secret string // base32, dimasukkan manual bila kode QR tidak terbaca
	URI    string // otpauth://, dijadikan kode QR oleh klien
}

// BeginMFAEnrollment membuat rahasia baru bagi pengguna.
//
// Rahasia disimpan namun belum berlaku. Faktor kedua baru aktif setelah
// pengguna membuktikan satu kode yang benar lewat ConfirmMFAEnrollment, agar
// tidak ada akun yang terkunci karena rahasianya gagal tersimpan di ponsel.
func (s *Service) BeginMFAEnrollment(ctx context.Context, userID uuid.UUID) (*Enrollment, error) {
	var email string
	var enabledAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT coalesce(email, ''), mfa_enabled_at FROM users WHERE id = $1`, userID).
		Scan(&email, &enabledAt)
	if err != nil {
		return nil, fmt.Errorf("membaca pengguna: %w", err)
	}
	if enabledAt != nil {
		return nil, ErrMFAAlreadyOn
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      mfaIssuer,
		AccountName: email,
		Period:      totpPeriod,
		Digits:      totpDigits,
	})
	if err != nil {
		return nil, fmt.Errorf("membangkitkan rahasia: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_secret = $2, mfa_last_step = NULL WHERE id = $1`,
		userID, key.Secret()); err != nil {
		return nil, fmt.Errorf("menyimpan rahasia: %w", err)
	}

	return &Enrollment{Secret: key.Secret(), URI: key.URL()}, nil
}

// ConfirmMFAEnrollment mengaktifkan faktor kedua setelah satu kode terbukti benar.
func (s *Service) ConfirmMFAEnrollment(ctx context.Context, userID uuid.UUID, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var secret string
	var enabledAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT coalesce(mfa_secret, ''), mfa_enabled_at FROM users WHERE id = $1 FOR UPDATE`,
		userID).Scan(&secret, &enabledAt)
	if err != nil {
		return fmt.Errorf("membaca pengguna: %w", err)
	}
	if enabledAt != nil {
		return ErrMFAAlreadyOn
	}
	if secret == "" {
		return ErrMFANotEnrolled
	}

	now := s.now()
	step, ok := validateTOTP(secret, code, now)
	if !ok {
		return ErrMFACodeInvalid
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET mfa_enabled_at = $2, mfa_last_step = $3 WHERE id = $1`,
		userID, now, step); err != nil {
		return fmt.Errorf("mengaktifkan faktor kedua: %w", err)
	}
	return tx.Commit(ctx)
}

// VerifyMFA memeriksa kode faktor kedua saat masuk.
//
// Baris pengguna dikunci dan langkah waktu yang sudah terpakai dicatat,
// sehingga satu kode tidak dapat dipakai dua kali walau dua permintaan datang
// bersamaan.
func (s *Service) VerifyMFA(ctx context.Context, userID uuid.UUID, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var secret string
	var enabledAt *time.Time
	var lastStep *int64
	err = tx.QueryRow(ctx, `
		SELECT coalesce(mfa_secret, ''), mfa_enabled_at, mfa_last_step
		FROM   users WHERE id = $1 FOR UPDATE`, userID).
		Scan(&secret, &enabledAt, &lastStep)
	if err != nil {
		return fmt.Errorf("membaca pengguna: %w", err)
	}
	if enabledAt == nil || secret == "" {
		return ErrMFANotEnrolled
	}

	step, ok := validateTOTP(secret, code, s.now())
	if !ok {
		return ErrMFACodeInvalid
	}
	if lastStep != nil && step <= *lastStep {
		return ErrMFACodeReplayed
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET mfa_last_step = $2 WHERE id = $1`, userID, step); err != nil {
		return fmt.Errorf("mencatat langkah terpakai: %w", err)
	}
	return tx.Commit(ctx)
}

// MFAEnabled memberi tahu apakah faktor kedua sudah aktif bagi seorang pengguna.
func (s *Service) MFAEnabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	var enabledAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT mfa_enabled_at FROM users WHERE id = $1`, userID).Scan(&enabledAt)
	if err != nil {
		return false, fmt.Errorf("membaca status faktor kedua: %w", err)
	}
	return enabledAt != nil, nil
}

// validateTOTP memeriksa kode terhadap rahasia dan mengembalikan langkah waktu
// yang cocok, agar langkah itu dapat dicatat sebagai sudah terpakai.
func validateTOTP(secret, code string, now time.Time) (int64, bool) {
	opts := totp.ValidateOpts{Period: totpPeriod, Skew: totpSkew, Digits: totpDigits}
	ok, err := totp.ValidateCustom(code, secret, now, opts)
	if err != nil || !ok {
		return 0, false
	}
	// Cari langkah mana yang cocok di dalam rentang toleransi, karena
	// ValidateCustom hanya menjawab cocok atau tidak.
	base := now.Unix() / totpPeriod
	for delta := int64(-totpSkew); delta <= totpSkew; delta++ {
		step := base + delta
		at := time.Unix(step*totpPeriod, 0)
		if ok, err := totp.ValidateCustom(code, secret, at,
			totp.ValidateOpts{Period: totpPeriod, Skew: 0, Digits: totpDigits}); err == nil && ok {
			return step, true
		}
	}
	return base, true
}
