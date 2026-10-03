package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Record yang dibaca kembali untuk penelusuran.
type Row struct {
	ID         int64      `json:"id"`
	Entity     string     `json:"entity"`
	EntityID   *uuid.UUID `json:"entity_id,omitempty"`
	Action     string     `json:"action"`
	Outcome    string     `json:"outcome"`
	Detail     string     `json:"detail,omitempty"`
	Before     []byte     `json:"before,omitempty"`
	After      []byte     `json:"after,omitempty"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	ActorName  string     `json:"actor_name,omitempty"`
	RequestID  string     `json:"request_id,omitempty"`
	OccurredAt time.Time  `json:"occurred_at"`
}

// Filter membatasi penelusuran jejak audit.
type Filter struct {
	Entity   string
	EntityID *uuid.UUID
	ActorID  *uuid.UUID
	Outcome  string
	Limit    int
}

// Reader membaca jejak audit.
type Reader struct{ pool *pgxpool.Pool }

// NewReader membuat pembaca jejak audit.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// List mengembalikan jejak audit terbaru sesuai penyaring.
func (r *Reader) List(ctx context.Context, f Filter) ([]Row, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	const q = `
		SELECT a.id, a.entity, a.entity_id, a.action, a.outcome,
		       coalesce(a.detail, ''), a.before, a.after,
		       a.actor_id, coalesce(u.name, ''), coalesce(a.request_id, ''),
		       a.occurred_at
		FROM   audit_trail a
		LEFT   JOIN users u ON u.id = a.actor_id
		WHERE  ($1 = '' OR a.entity = $1)
		  AND  ($2::uuid IS NULL OR a.entity_id = $2)
		  AND  ($3::uuid IS NULL OR a.actor_id = $3)
		  AND  ($4 = '' OR a.outcome = $4)
		ORDER  BY a.occurred_at DESC, a.id DESC
		LIMIT  $5`

	rows, err := r.pool.Query(ctx, q, f.Entity, f.EntityID, f.ActorID, f.Outcome, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("membaca jejak audit: %w", err)
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		var x Row
		if err := rows.Scan(&x.ID, &x.Entity, &x.EntityID, &x.Action, &x.Outcome,
			&x.Detail, &x.Before, &x.After, &x.ActorID, &x.ActorName,
			&x.RequestID, &x.OccurredAt); err != nil {
			return nil, fmt.Errorf("membaca baris jejak: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
