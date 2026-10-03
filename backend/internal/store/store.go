// Package store menyediakan koneksi basis data dan penerapan migrasi.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	appdb "github.com/iceman/backend/db"
)

// Connect membuka pool koneksi dan memastikan basis data benar benar menjawab.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("membaca DSN: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("membuka pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("menghubungi basis data: %w", err)
	}
	return pool, nil
}

// Migrate menerapkan seluruh migrasi yang belum dijalankan.
//
// Dipanggil saat instans mulai, sebelum menerima lalu lintas, sesuai
// Deployment Guide Bab 4. Migrasi wajib kompatibel mundur satu versi agar
// rollback tidak merusak data.
func Migrate(ctx context.Context, dsn string) error {
	conn := stdlib.OpenDB(*mustParse(dsn))
	defer conn.Close()

	goose.SetBaseFS(appdb.Migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("menetapkan dialek: %w", err)
	}
	if err := goose.UpContext(ctx, conn, "migrations"); err != nil {
		return fmt.Errorf("menjalankan migrasi: %w", err)
	}
	return nil
}

func mustParse(dsn string) *pgxConnConfig {
	cfg, err := parseConn(dsn)
	if err != nil {
		panic(err)
	}
	return cfg
}

var _ = sql.ErrNoRows
