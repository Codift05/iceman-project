// Command seed membuat pengguna internal untuk pengembangan dan pengujian.
//
//	go run ./cmd/seed -role ADMIN_OPS -email admin@iceman.test -password rahasia
//
// Perkakas ini tidak dipakai di produksi. Akun produksi pertama dibuat lewat
// prosedur serah terima, bukan lewat perintah ini.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/iceman/backend/internal/identity"
	"github.com/iceman/backend/internal/store"
)

func main() {
	var (
		role  = flag.String("role", "ADMIN_OPS", "kode peran: SUPER_ADMIN, ADMIN_OPS, FINANCE, MANAGEMENT, DRIVER")
		email = flag.String("email", "", "surel pengguna")
		pass  = flag.String("password", "", "kata sandi")
		name  = flag.String("name", "Pengguna Pengembangan", "nama pengguna")
	)
	flag.Parse()

	if *email == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "email dan password wajib diisi")
		os.Exit(2)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL belum diisi. Salin .env.example menjadi .env lebih dahulu.")
		os.Exit(2)
	}
	ctx := context.Background()

	pool, err := store.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "koneksi gagal:", err)
		os.Exit(1)
	}
	defer pool.Close()

	hash, err := identity.HashPassword(*pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, "membuat hash gagal:", err)
		os.Exit(1)
	}

	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO users (role_id, email, name, password_hash)
		SELECT r.id, $1, $2, $3 FROM roles r WHERE r.code = $4
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash
		RETURNING id`,
		strings.ToLower(*email), *name, hash, *role).Scan(&id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "menyimpan pengguna gagal:", err)
		os.Exit(1)
	}
	fmt.Printf("pengguna siap: %s  peran=%s  id=%s\n", *email, *role, id)
}
