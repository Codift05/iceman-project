package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/iceman/backend/internal/identity"
)

func TestAuthenticate_Berhasil(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")

	u, err := svc.Authenticate(context.Background(), email, "rahasia-yang-panjang")
	if err != nil {
		t.Fatalf("masuk gagal: %v", err)
	}
	if u.RoleCode != "ADMIN_OPS" {
		t.Fatalf("peran = %s, seharusnya ADMIN_OPS", u.RoleCode)
	}
}

// Surel tidak dikenal dan kata sandi salah harus menghasilkan galat yang sama,
// agar tidak membocorkan surel mana yang terdaftar (SRS-AUT-005).
func TestAuthenticate_GalatTidakMembedakan(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	_, errA := svc.Authenticate(ctx, email, "kata-sandi-keliru")
	_, errB := svc.Authenticate(ctx, "tidak-ada@iceman.test", "kata-sandi-keliru")

	if !errors.Is(errA, identity.ErrCredentialsInvalid) {
		t.Fatalf("kata sandi salah = %v, seharusnya ErrCredentialsInvalid", errA)
	}
	if !errors.Is(errB, identity.ErrCredentialsInvalid) {
		t.Fatalf("surel tidak dikenal = %v, seharusnya ErrCredentialsInvalid", errB)
	}
}

// TC-AUT-02: lima percobaan gagal mengunci akun selama 15 menit.
func TestAuthenticate_TerkunciSetelahLimaKaliGagal(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	for i := 1; i <= identity.MaxFailedAttempts; i++ {
		_, err := svc.Authenticate(ctx, email, "salah")
		if !errors.Is(err, identity.ErrCredentialsInvalid) {
			t.Fatalf("percobaan ke-%d = %v, seharusnya ErrCredentialsInvalid", i, err)
		}
	}

	// Percobaan berikutnya ditolak karena terkunci, bukan karena kata sandi,
	// bahkan ketika kata sandinya benar.
	_, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang")
	if !errors.Is(err, identity.ErrAccountLocked) {
		t.Fatalf("setelah terkunci = %v, seharusnya ErrAccountLocked", err)
	}
}

func TestAuthenticate_BerhasilMengulangPenghitung(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	for i := 0; i < identity.MaxFailedAttempts-1; i++ {
		_, _ = svc.Authenticate(ctx, email, "salah")
	}
	if _, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang"); err != nil {
		t.Fatalf("masuk benar seharusnya berhasil: %v", err)
	}
	// Penghitung kembali nol, jadi empat percobaan gagal berikutnya belum mengunci.
	for i := 0; i < identity.MaxFailedAttempts-1; i++ {
		_, _ = svc.Authenticate(ctx, email, "salah")
	}
	if _, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang"); err != nil {
		t.Fatalf("akun seharusnya belum terkunci: %v", err)
	}
}

func TestAuthenticate_AkunNonaktif(t *testing.T) {
	svc, pool := newService(t)
	id, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET status = 'INACTIVE' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Authenticate(context.Background(), email, "rahasia-yang-panjang")
	if !errors.Is(err, identity.ErrAccountInactive) {
		t.Fatalf("galat = %v, seharusnya ErrAccountInactive", err)
	}
}

// TC-AUT-07: peran berisiko tinggi tidak memperoleh sesi tanpa faktor kedua.
//
// Selama faktor kedua belum didaftarkan, yang dikembalikan adalah permintaan
// mendaftar. Setelah didaftarkan, yang diminta adalah kodenya. Keduanya sama
// sama tidak menerbitkan sesi.
func TestAuthenticate_PeranBerisikoWajibFaktorKedua(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()

	for _, role := range []string{"SUPER_ADMIN", "FINANCE"} {
		_, email := seedUser(t, pool, role, "rahasia-yang-panjang")
		_, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang")
		if !errors.Is(err, identity.ErrMFAEnrollRequired) {
			t.Fatalf("%s = %v, seharusnya ErrMFAEnrollRequired", role, err)
		}
	}

	// Peran lain tidak terkena kewajiban ini.
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	if _, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang"); err != nil {
		t.Fatalf("ADMIN_OPS seharusnya bisa masuk: %v", err)
	}
}

func TestHashPassword_HashBerbedaTiapKali(t *testing.T) {
	a, err := identity.HashPassword("kata-sandi-sama")
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.HashPassword("kata-sandi-sama")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("dua hash kata sandi yang sama seharusnya berbeda karena garamnya acak")
	}
	for _, h := range []string{a, b} {
		ok, err := identity.VerifyPassword("kata-sandi-sama", h)
		if err != nil || !ok {
			t.Fatalf("verifikasi gagal: ok=%v err=%v", ok, err)
		}
	}
	ok, _ := identity.VerifyPassword("kata-sandi-lain", a)
	if ok {
		t.Fatal("kata sandi keliru seharusnya ditolak")
	}
}
