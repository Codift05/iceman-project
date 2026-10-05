package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/order"
)

// ApplyResult merangkum hasil pemrosesan satu event.
type ApplyResult struct {
	// Applied bernilai benar bila status pembayaran berpindah.
	Applied bool `json:"applied"`
	// AlreadyProcessed bernilai benar bila event sudah pernah diproses.
	AlreadyProcessed bool `json:"already_processed"`
	// NeedsReview bernilai benar bila event diserahkan kepada manusia.
	NeedsReview bool   `json:"needs_review"`
	Note        string `json:"note,omitempty"`
	// PaymentStatus adalah status pembayaran sesudah pemrosesan.
	PaymentStatus string `json:"payment_status,omitempty"`
}

// ApplyEvent memproses satu event webhook yang sudah tersimpan.
//
// Dipanggil pekerja latar, bukan handler HTTP (SRS-PAY-002). Pesanan berpindah
// ke PAID hanya setelah fungsi ini selesai.
//
// Seluruh perubahan terjadi dalam satu transaksi: status pembayaran, status
// pesanan, nomor invoice, penandaan event sebagai selesai, dan jejak audit.
// Pemrosesan yang berhasil sebagian akan meninggalkan pembayaran lunas tanpa
// pesanan yang ikut berpindah, atau sebaliknya.
func (s *Service) ApplyEvent(ctx context.Context, eventRowID uuid.UUID) (*ApplyResult, error) {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Baris event dikunci lebih dahulu. Penyedia dapat mengirim ulang event
	// yang sama, dan dua pekerja dapat mengambilnya bersamaan; kunci inilah
	// yang membuat pemrosesannya tepat sekali.
	var (
		paymentID      *uuid.UUID
		providerStatus string
		mapped         *string
		nominalEvent   *int64
		sudahDiproses  *time.Time
		perluTinjau    bool
		catatan        string
	)
	err = tx.QueryRow(ctx, `
		SELECT payment_id, provider_status, mapped_status::text, amount_cents,
		       processed_at, needs_review, review_note
		FROM   payment_events WHERE id = $1 FOR UPDATE`, eventRowID).
		Scan(&paymentID, &providerStatus, &mapped, &nominalEvent,
			&sudahDiproses, &perluTinjau, &catatan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("event webhook tidak ditemukan")
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci event: %w", err)
	}

	if sudahDiproses != nil {
		return &ApplyResult{AlreadyProcessed: true}, nil
	}

	// Event yang sudah ditandai perlu ditinjau sejak diterima tidak diproses.
	// Yang menandainya adalah keadaan yang menyangkut uang: pembayaran tidak
	// ditemukan, status belum dikenal, atau nominal tidak cocok.
	if perluTinjau || paymentID == nil || mapped == nil {
		if err := tandaiSelesai(ctx, tx, eventRowID); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("menandai event: %w", err)
		}
		return &ApplyResult{NeedsReview: true, Note: catatan}, nil
	}

	var (
		statusLama string
		nominal    int64
		orderID    uuid.UUID
		berakhir   *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, amount_cents, order_id, expires_at
		FROM   payments WHERE id = $1 FOR UPDATE`, *paymentID).
		Scan(&statusLama, &nominal, &orderID, &berakhir)
	if err != nil {
		return nil, fmt.Errorf("mengunci pembayaran: %w", err)
	}

	tujuan := *mapped

	// Tagihan yang sudah kedaluwarsa tidak dapat dipakai menandai pesanan
	// sebagai dibayar (SRS-PAY-001). Ini ditegakkan walau penyedia menyatakan
	// pembayarannya berhasil, karena kuota slot pesanan mungkin sudah
	// dilepaskan dan menandainya lunas menghasilkan pesanan tanpa jadwal.
	//
	// Uangnya nyata, jadi eventnya tidak dibuang: ditandai untuk ditinjau agar
	// seseorang memutuskan antara mengembalikan dana atau menghormati pesanan
	// secara manual. Membiarkannya lewat tanpa penanda adalah yang paling
	// buruk, karena uang masuk tanpa ada yang tahu.
	if tujuan == StatusSuccess && berakhir != nil && !time.Now().Before(*berakhir) {
		alasan := fmt.Sprintf(
			"pembayaran berhasil diterima setelah tagihan kedaluwarsa pada %s, perlu diputuskan manusia",
			berakhir.Format(time.RFC3339))
		if err := tandaiPerluTinjau(ctx, tx, eventRowID, alasan); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("menandai event: %w", err)
		}
		return &ApplyResult{NeedsReview: true, Note: alasan, PaymentStatus: statusLama}, nil
	}

	// Status yang sudah sama tidak diproses ulang. Penyedia mengirim beberapa
	// event untuk satu pembayaran, dan tidak semuanya berarti perubahan.
	if statusLama == tujuan {
		if err := tandaiSelesai(ctx, tx, eventRowID); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("menandai event: %w", err)
		}
		return &ApplyResult{PaymentStatus: statusLama}, nil
	}

	if err := CheckTransition(statusLama, tujuan, nominal, nominalEvent); err != nil {
		// Perpindahan yang tidak sah bukan kegagalan teknis: penyedia dapat
		// mengirim event lama setelah event baru. Ditandai untuk ditinjau,
		// bukan dicoba ulang terus menerus.
		alasan := err.Error()
		if err := tandaiPerluTinjau(ctx, tx, eventRowID, alasan); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("menandai event: %w", err)
		}
		return &ApplyResult{NeedsReview: true, Note: alasan, PaymentStatus: statusLama}, nil
	}

	var waktuBayar *time.Time
	if tujuan == StatusSuccess {
		n := time.Now()
		waktuBayar = &n
	}
	_, err = tx.Exec(ctx, `
		UPDATE payments
		SET    status = $2::payment_status,
		       paid_at = coalesce(paid_at, $3)
		WHERE  id = $1`, *paymentID, tujuan, waktuBayar)
	if err != nil {
		return nil, fmt.Errorf("mengubah status pembayaran: %w", err)
	}

	if err := ikutkanPesanan(ctx, tx, orderID, tujuan, catatanPesanan(tujuan)); err != nil {
		return nil, err
	}

	if err := tandaiSelesai(ctx, tx, eventRowID); err != nil {
		return nil, err
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "payments", EntityID: paymentID, Action: audit.ActionUpdate,
		Before: map[string]any{"status": statusLama},
		After:  map[string]any{"status": tujuan},
		Detail: fmt.Sprintf("status pembayaran %s menjadi %s, status penyedia %q",
			statusLama, tujuan, providerStatus),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("memproses event: %w", err)
	}
	return &ApplyResult{Applied: true, PaymentStatus: tujuan}, nil
}

func tandaiSelesai(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE payment_events SET processed_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("menandai event selesai: %w", err)
	}
	return nil
}

func tandaiPerluTinjau(ctx context.Context, tx pgx.Tx, id uuid.UUID, alasan string) error {
	_, err := tx.Exec(ctx, `
		UPDATE payment_events
		SET    needs_review = true, review_note = $2, processed_at = now()
		WHERE  id = $1`, id, alasan)
	if err != nil {
		return fmt.Errorf("menandai event perlu ditinjau: %w", err)
	}
	return nil
}

func catatanPesanan(statusBayar string) string {
	switch statusBayar {
	case StatusSuccess:
		return "pembayaran diterima"
	case StatusExpired:
		return "tagihan kedaluwarsa"
	case StatusFailed:
		return "pembayaran gagal"
	case StatusCancelled:
		return "pembayaran dibatalkan"
	default:
		return "status pembayaran berubah"
	}
}

// ikutkanPesanan menyesuaikan status pesanan dengan status pembayaran.
//
// Hanya pembayaran berhasil yang memajukan pesanan. Tagihan yang kedaluwarsa
// atau gagal tidak membatalkan pesanan: pelanggan masih dapat mencoba lagi,
// dan pembatalan karena tidak dibayar adalah keputusan terpisah yang
// mengembalikan kuota slot (SRS-ORD-003).
func ikutkanPesanan(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, statusBayar, catatan string) error {
	if statusBayar != StatusSuccess {
		return nil
	}

	var dari string
	err := tx.QueryRow(ctx,
		`SELECT status::text FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&dari)
	if err != nil {
		return fmt.Errorf("membaca status pesanan: %w", err)
	}
	if _, ok := order.Allowed(dari, order.StatusPaid); !ok {
		// Pesanan sudah bergerak, misalnya dibatalkan admin sebelum
		// pembayarannya masuk. Statusnya dibiarkan; yang menanganinya adalah
		// penandaan event untuk ditinjau pada pemanggil.
		return nil
	}

	// Nomor invoice dibuat saat pembayaran diterima, bukan saat pesanan
	// dibuat. Pesanan yang tidak pernah dibayar tidak perlu nomor invoice, dan
	// penomoran yang melompat lompat sulit dijelaskan kepada pemeriksa pajak.
	_, err = tx.Exec(ctx, `
		UPDATE orders
		SET    status = 'PAID',
		       invoice_no = coalesce(invoice_no,
		           'INV-' || to_char(now(), 'YYMMDD') || '-' ||
		           lpad(nextval('order_no_seq')::text, 5, '0'))
		WHERE  id = $1`, orderID)
	if err != nil {
		return fmt.Errorf("menaikkan status pesanan: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO order_status_history (order_id, from_status, to_status, reason)
		VALUES ($1, $2::order_status, 'PAID', $3)`, orderID, dari, catatan)
	if err != nil {
		return fmt.Errorf("mencatat riwayat status pesanan: %w", err)
	}
	return nil
}
