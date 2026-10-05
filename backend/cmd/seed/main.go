// Command seed membuat pengguna internal untuk pengembangan dan pengujian.
//
//	go run ./cmd/seed -role ADMIN_OPS -email admin@iceman.test -password rahasia
//	go run ./cmd/seed -role DRIVER -email drv@iceman.test -password rahasia -depot MDO-01
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
		depot = flag.String("depot", "", "kode depo, wajib untuk peran DRIVER")
	)
	flag.Parse()

	if *email == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "email dan password wajib diisi")
		os.Exit(2)
	}
	// Driver wajib terikat pada satu depo. Tanpa itu ia tidak dapat ditugaskan
	// mengantar apa pun, karena DB-10 mewajibkan driver dan pesanan berasal
	// dari depo yang sama. Lebih baik ditolak di sini daripada akunnya jadi
	// lalu penugasannya gagal tanpa sebab yang jelas.
	if strings.ToUpper(*role) == "DRIVER" && *depot == "" {
		fmt.Fprintln(os.Stderr, "peran DRIVER wajib menyertakan -depot berisi kode depo")
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
		INSERT INTO users (role_id, depot_id, email, name, password_hash)
		SELECT r.id, d.id, $1, $2, $3
		FROM   roles r
		LEFT JOIN depots d ON $5 <> '' AND d.code = $5
		WHERE  r.code = $4
		ON CONFLICT (email) DO UPDATE
		SET    password_hash = EXCLUDED.password_hash,
		       depot_id = coalesce(EXCLUDED.depot_id, users.depot_id)
		RETURNING id`,
		strings.ToLower(*email), *name, hash, strings.ToUpper(*role), *depot).Scan(&id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "menyimpan pengguna gagal:", err)
		os.Exit(1)
	}

	// Depo yang disebut harus benar benar ada. LEFT JOIN membuat kode depo
	// yang salah ketik menghasilkan pengguna tanpa depo, bukan galat, jadi
	// hasilnya diperiksa di sini.
	if *depot != "" {
		var kode *string
		err := pool.QueryRow(ctx, `
			SELECT d.code FROM users u LEFT JOIN depots d ON d.id = u.depot_id
			WHERE u.id = $1`, id).Scan(&kode)
		if err != nil {
			fmt.Fprintln(os.Stderr, "memeriksa depo gagal:", err)
			os.Exit(1)
		}
		if kode == nil {
			fmt.Fprintf(os.Stderr, "depo dengan kode %q tidak ditemukan\n", *depot)
			os.Exit(1)
		}
	}

	// Profil driver disiapkan sekalian, supaya driver dapat langsung memberi
	// persetujuan pelacakan tanpa langkah pendaftaran terpisah.
	if strings.ToUpper(*role) == "DRIVER" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO drivers (user_id) VALUES ($1)
			ON CONFLICT (user_id) DO NOTHING`, id); err != nil {
			fmt.Fprintln(os.Stderr, "menyiapkan profil driver gagal:", err)
			os.Exit(1)
		}
	}

	fmt.Printf("pengguna siap: %s  peran=%s  depo=%s  id=%s\n",
		*email, strings.ToUpper(*role), tampilDepo(*depot), id)
}

// tampilDepo memberi tanda jelas ketika pengguna tidak terikat depo, supaya
// keluarannya tidak berakhir dengan "depo=" yang kosong dan membingungkan.
func tampilDepo(kode string) string {
	if kode == "" {
		return "(tanpa depo)"
	}
	return kode
}
