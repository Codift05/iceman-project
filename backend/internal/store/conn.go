package store

import (
	"github.com/jackc/pgx/v5"
)

type pgxConnConfig = pgx.ConnConfig

func parseConn(dsn string) (*pgx.ConnConfig, error) {
	return pgx.ParseConfig(dsn)
}
