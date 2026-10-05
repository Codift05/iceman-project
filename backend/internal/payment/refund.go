package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// RefundInput adalah permintaan pengembalian dana.
type RefundInput struct {
	// AmountCents nol berarti refund penuh atas sisa yang dapat dikembalikan.
	// Itu bentuk yang paling sering dipakai, dan membuat pemanggil tidak perlu
	// menghitung sisanya sendiri lalu salah.
	AmountCents int64
	Reason      string
}

// Refund mengembalikan dana atas sebuah pembayaran.
//
// Refund hanya atas pembayaran berhasil, nominalnya tidak boleh melebihi sisa,
// dan alasannya wajib (SRS-PAY-004). Status pembayaran dan pesanan diperbarui
// konsisten dalam satu transaksi.
//
// Penyedia dipanggil sebelum transaksi dibuka bila refund otomatis didukung.
// Memanggilnya di dalam transaksi berarti baris pembayaran terkunci selama
// permintaan jaringan berlangsung, dan refund yang lambat menahan pembacaan
// pembayaran itu.
func (s *Service) Refund(ctx context.Context, paymentID uuid.UUID, in RefundInput) (*Refund, error) {
	alasan := strings.TrimSpace(in.Reason)
	if alasan == "" {
		return nil, ErrReasonRequired
	}

	p, err := s.Get(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	// Pembayaran yang belum berhasil tidak punya dana untuk dikembalikan.
	if p.Status != StatusSuccess && p.Status != StatusRefunded {
		return nil, fmt.Errorf("%w: status pembayaran %s", ErrNotRefundable, p.Status)
	}

	sisa := p.Refundable()
	if sisa <= 0 {
		return nil, fmt.Errorf("%w: seluruh nominal sudah dikembalikan", ErrNotRefundable)
	}
	nominal := in.AmountCents
	if nominal == 0 {
		nominal = sisa
	}
	if nominal < 0 {
		return nil, fmt.Errorf("%w: nominal refund tidak boleh negatif", ErrRefundExceeds)
	}
	if nominal > sisa {
		return nil, fmt.Errorf("%w: sisa yang dapat dikembalikan %d sen", ErrRefundExceeds, sisa)
	}

	// Refund otomatis dipakai bila didukung, selain itu dicatat manual: dana
	// dikembalikan di luar sistem lalu dicatat di sini (SRS-PAY-004).
	var (
		ref    string
		manual = true
	)
	if s.d.Provider.SupportsRefund() && p.ProviderRef != "" {
		r, err := s.d.Provider.Refund(ctx, RefundRequest{
			PaymentRef: p.ProviderRef, AmountCents: nominal, Reason: alasan,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProvider, err)
		}
		ref, manual = r, false
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)
	// Baris pembayaran dikunci lebih dahulu, dalam pernyataan tersendiri.
	//
	// Tanpa kunci, dua refund bersamaan dapat sama sama membaca sisa yang sama
	// lalu keduanya merasa cukup, dan jumlah yang dikembalikan melebihi yang
	// pernah masuk.
	var (
		statusKini   string
		nominalBayar int64
		orderID      uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, amount_cents, order_id
		FROM   payments WHERE id = $1 FOR UPDATE`, paymentID).
		Scan(&statusKini, &nominalBayar, &orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pembayaran: %w", err)
	}

	// Jumlah yang sudah direfund dibaca dalam pernyataan terpisah, sesudah
	// kunci didapat. Ini bukan kerapian, melainkan syarat kebenaran.
	//
	// Bila penjumlahan itu ikut di dalam pernyataan yang mengunci, ia
	// dievaluasi memakai snapshot awal pernyataan tersebut. Transaksi yang
	// menunggu kunci tetap memakai snapshot lama itu dan tidak melihat refund
	// yang baru di-commit transaksi pemegang kunci, sehingga sisanya terbaca
	// masih utuh. Pada READ COMMITTED setiap pernyataan baru mengambil
	// snapshot baru, jadi penjumlahan di pernyataan tersendiri inilah yang
	// melihat keadaan terkini.
	//
	// Tanpa pemisahan ini, delapan refund bersamaan semuanya lolos dan uang
	// yang dikembalikan menjadi empat kali yang pernah masuk. Ada ujinya.
	var sudahRefund int64
	err = tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount_cents), 0) FROM refunds WHERE payment_id = $1`,
		paymentID).Scan(&sudahRefund)
	if err != nil {
		return nil, fmt.Errorf("menghitung refund terdahulu: %w", err)
	}

	if statusKini != StatusSuccess && statusKini != StatusRefunded {
		return nil, fmt.Errorf("%w: status pembayaran %s", ErrNotRefundable, statusKini)
	}
	if nominal > nominalBayar-sudahRefund {
		return nil, fmt.Errorf("%w: sisa yang dapat dikembalikan %d sen",
			ErrRefundExceeds, nominalBayar-sudahRefund)
	}

	pelaku := audit.ActorFrom(ctx)
	var x Refund
	err = tx.QueryRow(ctx, `
		INSERT INTO refunds (payment_id, amount_cents, reason, provider_ref, is_manual, actor_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, payment_id, amount_cents, reason, coalesce(provider_ref, ''),
		          is_manual, actor_id, created_at`,
		paymentID, nominal, alasan, nullifKosong(ref), manual, pelaku).
		Scan(&x.ID, &x.PaymentID, &x.AmountCents, &x.Reason, &x.ProviderRef,
			&x.IsManual, &x.ActorID, &x.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("menyimpan refund: %w", err)
	}

	// Refund penuh mengubah status pembayaran menjadi REFUNDED. Refund
	// sebagian tidak, karena pembayarannya masih sebagian berlaku dan
	// menandainya REFUNDED akan menyembunyikan sisa yang masih dapat
	// dikembalikan.
	if sudahRefund+nominal >= nominalBayar {
		if _, err := tx.Exec(ctx, `
			UPDATE payments SET status = 'REFUNDED' WHERE id = $1`, paymentID); err != nil {
			return nil, fmt.Errorf("mengubah status pembayaran: %w", err)
		}
	}

	jenis := "manual"
	if !manual {
		jenis = "otomatis lewat penyedia"
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "refunds", EntityID: &x.ID, Action: audit.ActionCreate,
		After: map[string]any{
			"payment_id": paymentID, "amount_cents": nominal,
			"is_manual": manual, "provider_ref": ref,
		},
		Detail: fmt.Sprintf("refund %s sebesar %d sen, alasan: %s", jenis, nominal, alasan),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan refund: %w", err)
	}
	return &x, nil
}
