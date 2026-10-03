package identity_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/identity"
	"github.com/iceman/backend/internal/store"
)

// dsn membaca alamat basis data uji dari lingkungan. Tidak ada nilai baku yang
// memuat kredensial, agar repositori tetap bebas dari kata sandi. Jalankan uji
// lewat "make test" yang sudah memuatnya dari berkas .env.
func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

func newService(t *testing.T) (*identity.Service, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if dsn() == "" {
		t.Skip("ICEMAN_TEST_DSN belum diisi, jalankan lewat make test")
	}

	if err := store.Migrate(ctx, dsn()); err != nil {
		t.Skipf("basis data uji tidak tersedia: %v", err)
	}
	pool, err := store.Connect(ctx, dsn())
	if err != nil {
		t.Skipf("basis data uji tidak tersedia: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := identity.NewService(pool, identity.NewSigner([]byte("kunci-uji-jangan-dipakai-produksi")))
	return svc, pool
}

// seedUser membuat pengguna internal dengan peran tertentu.
func seedUser(t *testing.T, pool *pgxpool.Pool, roleCode, password string) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()

	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatalf("membuat hash: %v", err)
	}
	email := "uji-" + uuid.NewString()[:8] + "@iceman.test"

	var id uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO users (role_id, email, name, password_hash)
		SELECT r.id, $1, $2, $3 FROM roles r WHERE r.code = $4
		RETURNING id`, email, "Pengguna Uji", hash, roleCode).Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pengguna peran %s: %v", roleCode, err)
	}
	return id, email
}

func roleID(t *testing.T, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM roles WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("membaca peran %s: %v", code, err)
	}
	return id
}
