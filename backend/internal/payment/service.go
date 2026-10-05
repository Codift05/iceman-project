package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/order"
)

// DefaultExpiry adalah masa berlaku tagihan bawaan.
//
// Dua jam dipilih agar pelanggan punya waktu menyelesaikan pembayaran tanpa
// membuat kuota slot tertahan terlalu lama oleh pesanan yang mungkin tidak
// dibayar.
const DefaultExpiry = 2 * time.Hour

// Deps adalah apa yang dibutuhkan layanan pembayaran.
type Deps struct {
	Pool     *pgxpool.Pool
	Provider Provider
	// Expiry mengatur masa berlaku tagihan. Nol berarti DefaultExpiry.
	Expiry time.Duration
}

// Service menangani pembayaran.
type Service struct{ d Deps }

// NewService membuat layanan pembayaran.
func NewService(d Deps) *Service {
	if d.Provider == nil {
		d.Provider = ManualProvider{}
	}
	if d.Expiry == 0 {
		d.Expiry = DefaultExpiry
	}
	return &Service{d: d}
}

// Charge membuat tagihan untuk sebuah pesanan.
//
// Pesanan hanya dapat ditagih bila masih menunggu pembayaran. Pesanan yang
// sudah dibayar, sudah diproses, atau batal menolak dengan galat yang berbeda
// supaya antarmuka dapat menjelaskan sebabnya.
//
// Penyedia dipanggil di luar transaksi basis data. Memanggilnya di dalam
// transaksi berarti transaksi menganggur selama permintaan jaringan
// berlangsung, dan baris pesanan terkunci selama itu.
func (s *Service) Charge(ctx context.Context, orderID uuid.UUID) (*Payment, error) {
	var (
		statusPesanan string
		nomor         string
		total         int64
	)
	err := s.d.Pool.QueryRow(ctx,
		`SELECT status::text, order_no, total_cents FROM orders WHERE id = $1`, orderID).
		Scan(&statusPesanan, &nomor, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, order.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membaca pesanan: %w", err)
	}

	// Pesanan yang sudah punya pembayaran berhasil tidak boleh ditagih lagi.
	// Diperiksa sebelum memanggil penyedia, supaya tidak ada tagihan yang
	// terbuat di sisi penyedia lalu ditolak di sini.
	var sudahLunas bool
	err = s.d.Pool.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM payments
			WHERE order_id = $1 AND status IN ('SUCCESS', 'REFUNDED'))`, orderID).Scan(&sudahLunas)
	if err != nil {
		return nil, fmt.Errorf("memeriksa pembayaran yang sudah ada: %w", err)
	}
	if sudahLunas {
		return nil, ErrAlreadyPaid
	}
	if statusPesanan != order.StatusWaitingPayment {
		return nil, fmt.Errorf("%w: status pesanan %s", ErrOrderNotPayable, statusPesanan)
	}

	// Tagihan yang masih menunggu dikembalikan apa adanya, bukan dibuat baru.
	// Pelanggan yang membuka kembali halaman pembayaran harus melihat kode QR
	// yang sama, dan indeks keunikan memang hanya mengizinkan satu.
	ada, err := s.pendingFor(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if ada != nil && !ada.Expired(time.Now()) {
		return ada, nil
	}
	// Tagihan yang sudah kedaluwarsa ditandai lebih dahulu, agar tagihan baru
	// tidak bertabrakan dengan indeks satu pending per pesanan.
	if ada != nil {
		if err := s.markExpired(ctx, ada.ID); err != nil {
			return nil, err
		}
	}

	hasil, err := s.d.Provider.Charge(ctx, ChargeRequest{
		OrderNo: nomor, AmountCents: total, ExpiresIn: s.d.Expiry,
	})
	if err != nil {
		// Kegagalan penyedia tidak membatalkan pesanan; pelanggan dapat
		// mencoba lagi (SRS-PAY-001). Galatnya dibungkus agar lapisan HTTP
		// dapat menjawab 502 tanpa menebak.
		return nil, fmt.Errorf("%w: %v", ErrProvider, err)
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO payments
		       (order_id, amount_cents, method, provider, provider_ref, qr_payload, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		orderID, total, metodeDari(s.d.Provider), s.d.Provider.Name(),
		nullifKosong(hasil.Ref), hasil.QRPayload, hasil.ExpiresAt).Scan(&id)
	if isUniqueViolation(err) {
		// Dua permintaan bersamaan sampai di sini. Yang kalah membaca tagihan
		// milik yang menang, karena dari sisi pelanggan permintaannya memang
		// berhasil: tagihannya ada.
		_ = tx.Rollback(ctx)
		lagi, errBaca := s.pendingFor(ctx, orderID)
		if errBaca != nil {
			return nil, errBaca
		}
		if lagi != nil {
			return lagi, nil
		}
		return nil, fmt.Errorf("menyimpan tagihan: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan tagihan: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "payments", EntityID: &id, Action: audit.ActionCreate,
		After: map[string]any{
			"order_id": orderID, "amount_cents": total,
			"provider": s.d.Provider.Name(), "provider_ref": hasil.Ref,
		},
		Detail: fmt.Sprintf("tagihan dibuat lewat %s, nominal %d sen",
			s.d.Provider.Name(), total),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan tagihan: %w", err)
	}
	return s.Get(ctx, id)
}

// metodeDari menyimpulkan metode pembayaran dari penyedianya.
//
// Penyedia manual mencatat pembayaran di luar sistem, sehingga metodenya bukan
// QRIS. Membedakannya penting bagi rekonsiliasi: pembayaran manual tidak akan
// pernah muncul pada berkas settlement penyedia.
func metodeDari(p Provider) string {
	if p.Name() == "MANUAL" {
		return "MANUAL"
	}
	return "QRIS"
}

func nullifKosong(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pendingFor membaca tagihan yang masih menunggu untuk sebuah pesanan.
func (s *Service) pendingFor(ctx context.Context, orderID uuid.UUID) (*Payment, error) {
	x, err := pindai(s.d.Pool.QueryRow(ctx, `
		SELECT `+kolom+`
		FROM   payments p JOIN orders o ON o.id = p.order_id
		WHERE  p.order_id = $1 AND p.status = 'PENDING'`, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("membaca tagihan menunggu: %w", err)
	}
	return x, nil
}

// markExpired menandai tagihan sebagai kedaluwarsa.
func (s *Service) markExpired(ctx context.Context, id uuid.UUID) error {
	_, err := s.d.Pool.Exec(ctx, `
		UPDATE payments SET status = 'EXPIRED'
		WHERE  id = $1 AND status = 'PENDING'`, id)
	if err != nil {
		return fmt.Errorf("menandai tagihan kedaluwarsa: %w", err)
	}
	return nil
}

// WebhookInput adalah permintaan webhook yang sudah terbaca.
type WebhookInput struct {
	// EventID adalah kunci unik dari penyedia, dasar idempotensi (DB-02).
	EventID string
	// ProviderRef menunjuk pembayaran yang dimaksud.
	ProviderRef    string
	ProviderStatus string
	AmountCents    *int64
	// Payload adalah muatan mentah, disimpan apa adanya untuk penelusuran.
	Payload []byte
}

// AcceptWebhook menyimpan satu event webhook, tanpa memprosesnya.
//
// Pemrosesan diserahkan kepada pekerja latar (SRS-PAY-002), sehingga handler
// HTTP menjawab cepat. Penyedia pembayaran biasanya memberi batas waktu
// beberapa detik dan mengirim ulang bila terlampaui; memproses di dalam
// handler berarti pengiriman ulang terjadi hanya karena kita lambat.
//
// Mengembalikan duplikat bernilai true bila event sudah pernah diterima. Itu
// bukan galat: penyedia mengirim ulang event yang belum dijawab berhasil, dan
// jawabannya tetap berhasil tanpa efek samping.
func (s *Service) AcceptWebhook(ctx context.Context, in WebhookInput) (eventID uuid.UUID, duplikat bool, err error) {
	if in.EventID == "" {
		return uuid.Nil, false, fmt.Errorf("event tanpa pengenal dari penyedia")
	}

	muatan := in.Payload
	if !json.Valid(muatan) {
		// Muatan yang bukan JSON tetap disimpan, dibungkus agar kolom jsonb
		// menerimanya. Membuangnya berarti kehilangan bukti ketika nanti ada
		// selisih dengan penyedia.
		dibungkus, _ := json.Marshal(map[string]string{"raw": string(muatan)})
		muatan = dibungkus
	}

	// Pembayaran dicari dari referensi penyedia. Tidak ditemukan bukan alasan
	// menolak event: eventnya tetap disimpan dan ditandai untuk ditinjau,
	// karena event yang dibuang tidak dapat ditelusuri lagi.
	var paymentID *uuid.UUID
	var nominalTagihan int64
	if in.ProviderRef != "" {
		var id uuid.UUID
		errBaca := s.d.Pool.QueryRow(ctx,
			`SELECT id, amount_cents FROM payments WHERE provider_ref = $1`,
			in.ProviderRef).Scan(&id, &nominalTagihan)
		if errBaca == nil {
			paymentID = &id
		} else if !errors.Is(errBaca, pgx.ErrNoRows) {
			return uuid.Nil, false, fmt.Errorf("mencari pembayaran: %w", errBaca)
		}
	}

	dipetakan, dikenal := MapStatus(in.ProviderStatus)
	var mapped *string
	if dikenal {
		mapped = &dipetakan
	}

	perluTinjau, catatan := alasanTinjau(paymentID, dikenal, in.ProviderStatus,
		dipetakan, nominalTagihan, in.AmountCents)

	var id uuid.UUID
	err = s.d.Pool.QueryRow(ctx, `
		INSERT INTO payment_events
		       (event_id, payment_id, provider, provider_status, mapped_status,
		        amount_cents, payload, needs_review, review_note)
		VALUES ($1, $2, $3, $4, $5::payment_status, $6, $7, $8, $9)
		RETURNING id`,
		in.EventID, paymentID, s.d.Provider.Name(), in.ProviderStatus, mapped,
		in.AmountCents, muatan, perluTinjau, catatan).Scan(&id)
	if isUniqueViolation(err) {
		// Event sudah pernah diterima. Pengenal barisnya dibaca agar pemanggil
		// tetap dapat merujuknya.
		errBaca := s.d.Pool.QueryRow(ctx,
			`SELECT id FROM payment_events WHERE event_id = $1`, in.EventID).Scan(&id)
		if errBaca != nil {
			return uuid.Nil, false, fmt.Errorf("membaca event terdahulu: %w", errBaca)
		}
		return id, true, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("menyimpan event webhook: %w", err)
	}
	return id, false, nil
}

// alasanTinjau menyimpulkan apakah sebuah event perlu ditinjau manusia.
//
// Tiga keadaan menuntut tinjauan, dan ketiganya menyangkut uang: pembayarannya
// tidak ditemukan, status penyedianya belum dikenal, atau nominalnya tidak
// sama dengan tagihan. Semuanya dicatat, bukan ditolak, karena event yang
// ditolak hilang dan tidak dapat ditelusuri lagi (SRS-PAY-003).
func alasanTinjau(paymentID *uuid.UUID, dikenal bool, statusPenyedia, dipetakan string, nominalTagihan int64, nominalEvent *int64) (bool, string) {
	switch {
	case paymentID == nil:
		return true, "pembayaran dengan referensi itu tidak ditemukan"
	case !dikenal:
		return true, fmt.Sprintf("status penyedia %q belum dikenal", statusPenyedia)
	case dipetakan == StatusSuccess && (nominalEvent == nil || *nominalEvent != nominalTagihan):
		return true, fmt.Sprintf("nominal event %s tidak sama dengan tagihan %d sen",
			tampilNominal(nominalEvent), nominalTagihan)
	}
	return false, ""
}

func tampilNominal(n *int64) string {
	if n == nil {
		return "(tidak disebutkan)"
	}
	return fmt.Sprintf("%d sen", *n)
}

// RecordReject mencatat percobaan webhook yang tanda tangannya salah.
//
// Dicatat, bukan diabaikan, karena lonjakan percobaan dengan tanda tangan
// salah adalah tanda seseorang sedang mencoba memalsukan pembayaran
// (SRS-PAY-002).
func (s *Service) RecordReject(ctx context.Context, alasan, remoteAddr, bodySHA string) error {
	_, err := s.d.Pool.Exec(ctx, `
		INSERT INTO payment_webhook_rejects (provider, reason, remote_addr, body_sha256)
		VALUES ($1, $2, $3, $4)`,
		s.d.Provider.Name(), alasan, remoteAddr, bodySHA)
	if err != nil {
		return fmt.Errorf("mencatat penolakan webhook: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
