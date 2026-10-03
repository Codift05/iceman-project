package audit_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/store"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

func newPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func seedActor(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (role_id, email, name)
		SELECT r.id, $1, 'Pelaku Uji' FROM roles r WHERE r.code = 'ADMIN_OPS'
		RETURNING id`, "audit-"+uuid.NewString()[:8]+"@iceman.test").Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pelaku: %v", err)
	}
	return id
}

// Catatan audit ditulis dalam transaksi pemanggil, sehingga membatalkan
// transaksi juga membatalkan catatannya. Tanpa sifat ini, jejak audit dapat
// memuat perubahan yang sebenarnya tidak pernah terjadi.
func TestRecord_IkutBatalSaatTransaksiDibatalkan(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	entity := "uji-" + uuid.NewString()[:8]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: entity, Action: audit.ActionUpdate,
		Before: map[string]any{"x": 1}, After: map[string]any{"x": 2},
	}); err != nil {
		t.Fatalf("menulis jejak: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_trail WHERE entity = $1`, entity).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("jejak tersisa %d baris setelah rollback, seharusnya 0", n)
	}
}

func TestRecord_TersimpanSaatTransaksiBerhasil(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	actor := seedActor(t, pool)
	entity := "uji-" + uuid.NewString()[:8]
	target := uuid.New()

	ctx = audit.WithActor(ctx, actor)
	ctx = audit.WithRequestID(ctx, "req-abc-123")

	tx, _ := pool.Begin(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: entity, EntityID: &target, Action: audit.ActionUpdate,
		Before: map[string]any{"capacity": 20},
		After:  map[string]any{"capacity": 25},
	}); err != nil {
		t.Fatalf("menulis jejak: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		gotActor  uuid.UUID
		gotReq    string
		gotBefore []byte
		gotAfter  []byte
		outcome   string
	)
	err := pool.QueryRow(ctx, `
		SELECT actor_id, request_id, before, after, outcome
		FROM   audit_trail WHERE entity = $1`, entity).
		Scan(&gotActor, &gotReq, &gotBefore, &gotAfter, &outcome)
	if err != nil {
		t.Fatalf("membaca jejak: %v", err)
	}
	if gotActor != actor {
		t.Fatalf("pelaku = %s, seharusnya %s", gotActor, actor)
	}
	if gotReq != "req-abc-123" {
		t.Fatalf("request_id = %q, seharusnya req-abc-123", gotReq)
	}
	if outcome != audit.OutcomeApplied {
		t.Fatalf("outcome = %s, seharusnya APPLIED", outcome)
	}

	var before, after map[string]any
	_ = json.Unmarshal(gotBefore, &before)
	_ = json.Unmarshal(gotAfter, &after)
	if before["capacity"] != float64(20) || after["capacity"] != float64(25) {
		t.Fatalf("nilai lama dan baru tidak tersimpan benar: %v -> %v", before, after)
	}
}

// Tindakan oleh sistem, misalnya pekerjaan latar terjadwal, tetap tercatat
// walau tidak ada pelaku manusia.
func TestRecord_TanpaPelakuTetapTercatat(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	entity := "uji-" + uuid.NewString()[:8]

	tx, _ := pool.Begin(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Entity: entity, Action: audit.ActionCreate}); err != nil {
		t.Fatalf("menulis jejak: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var actor *uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT actor_id FROM audit_trail WHERE entity = $1`, entity).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != nil {
		t.Fatalf("pelaku = %v, seharusnya kosong untuk tindakan sistem", actor)
	}
}

// Jejak audit tidak boleh diubah maupun dihapus lewat aplikasi. Tanpa
// penjagaan ini, catatan yang paling penting justru yang paling mudah
// dilenyapkan oleh pihak yang ingin menutupi perbuatannya.
func TestAudit_TidakDapatDiubahMaupunDihapus(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	entity := "uji-" + uuid.NewString()[:8]

	tx, _ := pool.Begin(ctx)
	_ = audit.Record(ctx, tx, audit.Entry{Entity: entity, Action: audit.ActionCreate})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE audit_trail SET action = 'DIPALSUKAN' WHERE entity = $1`, entity); err == nil {
		t.Fatal("mengubah jejak audit seharusnya ditolak basis data")
	} else {
		t.Logf("perubahan ditolak seperti seharusnya: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`DELETE FROM audit_trail WHERE entity = $1`, entity); err == nil {
		t.Fatal("menghapus jejak audit seharusnya ditolak basis data")
	} else {
		t.Logf("penghapusan ditolak seperti seharusnya: %v", err)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_trail WHERE entity = $1`, entity).Scan(&n)
	if n != 1 {
		t.Fatalf("jejak tersisa %d baris, seharusnya tetap 1", n)
	}
}

func TestReader_MenyaringMenurutEntitasDanHasil(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	entity := "uji-" + uuid.NewString()[:8]

	tx, _ := pool.Begin(ctx)
	_ = audit.Record(ctx, tx, audit.Entry{Entity: entity, Action: audit.ActionCreate})
	_ = audit.Record(ctx, tx, audit.Entry{
		Entity: entity, Action: audit.ActionAccess,
		Outcome: audit.OutcomeDenied, Detail: "GET /v1/depots butuh depot.view",
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	r := audit.NewReader(pool)
	all, err := r.List(ctx, audit.Filter{Entity: entity})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("jejak entitas = %d, seharusnya 2", len(all))
	}

	denied, err := r.List(ctx, audit.Filter{Entity: entity, Outcome: audit.OutcomeDenied})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 1 || denied[0].Detail == "" {
		t.Fatalf("penolakan = %d baris, detail %q", len(denied), denied[0].Detail)
	}
}
