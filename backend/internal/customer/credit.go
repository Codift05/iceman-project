package customer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreditStatus adalah keadaan piutang seorang pelanggan kontrak.
type CreditStatus struct {
	// LimitCents adalah plafon piutang. Nol berarti tanpa batas.
	LimitCents int64 `json:"limit_cents"`
	// OutstandingCents adalah piutang yang belum dilunasi.
	OutstandingCents int64 `json:"outstanding_cents"`
	// AvailableCents adalah sisa yang masih dapat dipakai. Bernilai nol bila
	// plafonnya sudah terlampaui, bukan negatif, karena sisa negatif tidak
	// berarti apa pun bagi yang membacanya.
	AvailableCents int64 `json:"available_cents"`
	// Unlimited menandai pelanggan tanpa plafon.
	Unlimited bool `json:"unlimited"`
}

// outstandingSQL menghitung piutang pelanggan.
//
// Yang dihitung adalah pesanan yang dibuat dengan termin, yaitu yang kolom
// payment_term_days-nya terisi, belum dibatalkan, dan belum punya pembayaran
// berhasil.
//
// Tiga syarat itu perlu semuanya. Pesanan tanpa termin sudah dibayar di muka
// sehingga bukan piutang. Pesanan yang dibatalkan tidak perlu dibayar. Dan
// pesanan bertermin yang sudah dilunasi, misalnya pelanggan membayar lebih
// awal, juga bukan piutang lagi.
//
// Pesanan yang sudah selesai diantar tetap dihitung bila belum dibayar: itu
// justru inti dari penjualan dengan termin.
const outstandingSQL = `
	SELECT coalesce(sum(o.total_cents), 0)
	FROM   orders o
	WHERE  o.customer_id = $1
	  AND  o.payment_term_days IS NOT NULL
	  AND  o.status <> 'CANCELLED'
	  AND  NOT EXISTS (
	       SELECT 1 FROM payments p
	       WHERE p.order_id = o.id AND p.status IN ('SUCCESS', 'REFUNDED'))`

// Credit mengembalikan keadaan piutang seorang pelanggan.
//
// Dipakai tampilan admin agar petugas dapat melihat sisa plafon sebelum
// menerima pesanan lewat telepon, bukan menunggu penolakan saat menyimpannya.
func (c *Customers) Credit(ctx context.Context, customerID uuid.UUID) (*CreditStatus, error) {
	term, err := c.ActiveTerm(ctx, customerID)
	if err != nil {
		return nil, err
	}

	var piutang int64
	if err := c.pool.QueryRow(ctx, outstandingSQL, customerID).Scan(&piutang); err != nil {
		return nil, fmt.Errorf("menghitung piutang: %w", err)
	}

	st := &CreditStatus{OutstandingCents: piutang}
	if term == nil || term.CreditLimitCents <= 0 {
		st.Unlimited = true
		return st, nil
	}
	st.LimitCents = term.CreditLimitCents
	if sisa := term.CreditLimitCents - piutang; sisa > 0 {
		st.AvailableCents = sisa
	}
	return st, nil
}

// CheckCreditTx memeriksa apakah pesanan baru masih di dalam plafon, di dalam
// transaksi pemanggil.
//
// Dipanggil checkout dengan baris pelanggan terkunci. Tanpa penguncian, dua
// pesanan bersamaan dapat sama sama membaca piutang yang sama lalu keduanya
// merasa cukup, dan piutangnya melampaui plafon.
//
// Plafon nol berarti tanpa batas, bukan nol rupiah. Itu nilai bawaan kolomnya,
// dan menafsirkannya sebagai nol rupiah akan menolak seluruh pesanan setiap
// pelanggan kontrak yang plafonnya belum diisi.
func (c *Customers) CheckCreditTx(ctx context.Context, tx pgx.Tx, customerID uuid.UUID, tambahanCents int64) error {
	var plafon int64
	err := tx.QueryRow(ctx, `
		SELECT credit_limit_cents
		FROM   contract_terms
		WHERE  customer_id = $1
		  AND  is_active
		  AND  valid_from <= current_date
		  AND  (valid_until IS NULL OR valid_until >= current_date)
		FOR    UPDATE`, customerID).Scan(&plafon)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Tanpa termin aktif, pesanan ini bukan penjualan bertermin dan tidak
		// menambah piutang. Pemeriksaan plafon tidak berlaku.
		return nil
	case err != nil:
		// Galat lain harus menggagalkan checkout, bukan dianggap "tanpa
		// termin". Menyamakan keduanya membuat satu gangguan sesaat pada basis
		// data mematikan kendali plafon tanpa meninggalkan jejak apa pun.
		return fmt.Errorf("membaca termin kontrak: %w", err)
	}
	if plafon <= 0 {
		return nil
	}

	var piutang int64
	if err := tx.QueryRow(ctx, outstandingSQL, customerID).Scan(&piutang); err != nil {
		return fmt.Errorf("menghitung piutang: %w", err)
	}

	if piutang+tambahanCents > plafon {
		return fmt.Errorf("%w: piutang %d sen ditambah pesanan ini %d sen melampaui plafon %d sen",
			ErrCreditLimit, piutang, tambahanCents, plafon)
	}
	return nil
}
