// Package audit mencatat perubahan data kritis beserta pelaku dan waktunya.
//
// Pencatatan dilakukan di dalam transaksi milik pemanggil, bukan transaksi
// sendiri. Dengan begitu membatalkan perubahan juga membatalkan catatannya,
// dan tidak mungkin ada catatan audit atas perubahan yang sebenarnya gagal
// (SRS-AUD-001, BR-005).
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Hasil sebuah tindakan.
const (
	OutcomeApplied = "APPLIED" // perubahan benar benar terjadi
	OutcomeDenied  = "DENIED"  // ditolak karena hak akses
	OutcomeFailed  = "FAILED"  // gagal karena aturan bisnis
)

// Tindakan yang lazim dicatat.
const (
	ActionCreate       = "CREATE"
	ActionUpdate       = "UPDATE"
	ActionDelete       = "DELETE"
	ActionStatusChange = "STATUS_CHANGE"
	ActionAccess       = "ACCESS"
	ActionLogin        = "LOGIN"
)

// Entry adalah satu baris jejak audit.
type Entry struct {
	Entity   string     // nama tabel atau sumber daya, misalnya "delivery_slots"
	EntityID *uuid.UUID // boleh kosong untuk tindakan yang tidak menyentuh satu baris
	Action   string
	Before   any // keadaan sebelum, boleh nil
	After    any // keadaan sesudah, boleh nil
	Outcome  string
	Detail   string
}

// Record menulis satu baris jejak audit memakai transaksi yang diberikan.
func Record(ctx context.Context, tx pgx.Tx, e Entry) error {
	before, err := toJSON(e.Before)
	if err != nil {
		return fmt.Errorf("menyusun keadaan sebelum: %w", err)
	}
	after, err := toJSON(e.After)
	if err != nil {
		return fmt.Errorf("menyusun keadaan sesudah: %w", err)
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeApplied
	}

	actor := ActorFrom(ctx)
	const q = `
		INSERT INTO audit_trail
		       (entity, entity_id, action, before, after, actor_id, request_id, outcome, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''))`

	if _, err := tx.Exec(ctx, q, e.Entity, e.EntityID, e.Action,
		before, after, actor, RequestIDFrom(ctx), e.Outcome, e.Detail); err != nil {
		return fmt.Errorf("menulis jejak audit: %w", err)
	}
	return nil
}

// RecordOutside menulis jejak audit di luar transaksi perubahan.
//
// Dipakai hanya untuk kejadian yang memang tidak mengubah data, seperti
// penolakan akses dan percobaan masuk. Untuk perubahan data, selalu pakai
// Record agar catatan ikut batal bila perubahannya batal.
func RecordOutside(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, e Entry) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi audit: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := Record(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
