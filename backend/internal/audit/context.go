package audit

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const (
	keyActor ctxKey = iota
	keyRequestID
)

// WithActor melekatkan pelaku pada konteks permintaan.
//
// Pelaku dibawa lewat konteks, bukan lewat parameter setiap fungsi, karena ia
// bersifat melekat pada permintaan dan dibutuhkan jauh di dalam lapisan
// layanan. Tanpa ini, setiap fungsi di jalur perubahan data harus menambah
// parameter yang sama hanya untuk diteruskan.
func WithActor(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, keyActor, userID)
}

// WithRequestID melekatkan pengenal permintaan pada konteks.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// ActorFrom membaca pelaku. Mengembalikan nil bila tindakan dilakukan sistem,
// misalnya oleh pekerjaan latar terjadwal.
func ActorFrom(ctx context.Context) *uuid.UUID {
	if v, ok := ctx.Value(keyActor).(uuid.UUID); ok && v != uuid.Nil {
		return &v
	}
	return nil
}

// RequestIDFrom membaca pengenal permintaan. Kosong bila di luar permintaan HTTP.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(keyRequestID).(string); ok {
		return v
	}
	return ""
}
