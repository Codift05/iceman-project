// Package db menyimpan migrasi skema sebagai berkas yang ditanam ke dalam binary,
// sehingga rilis tidak bergantung pada berkas yang ikut disalin ke server.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
