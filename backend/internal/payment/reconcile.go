package payment

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
)

// Jenis selisih rekonsiliasi.
const (
	// FlagNoSettlement: pembayaran berhasil namun belum muncul pada settlement.
	FlagNoSettlement = "NO_SETTLEMENT"
	// FlagAmountMismatch: nominal pada settlement berbeda dari nominal tagihan.
	FlagAmountMismatch = "AMOUNT_MISMATCH"
	// FlagOrphanSettlement: baris settlement yang tidak cocok dengan pembayaran
	// mana pun. Biasanya salah referensi atau berkas dari akun lain.
	FlagOrphanSettlement = "ORPHAN_SETTLEMENT"
)

// Galat rekonsiliasi.
var (
	ErrFlagNotFound   = errors.New("penandaan selisih tidak ditemukan")
	ErrPeriodInvalid  = errors.New("periode tidak sah")
	ErrFlagNoSubject  = errors.New("penandaan selisih harus menyebut pembayaran atau settlement")
	ErrAlreadyFlagged = errors.New("selisih itu sudah ditandai dan belum diselesaikan")
)

// SettlementRow adalah satu baris berkas settlement dari penyedia.
type SettlementRow struct {
	ProviderRef string
	GrossCents  int64
	FeeCents    int64
	SettledDate time.Time
}

// ImportResult merangkum hasil pemasukan berkas settlement.
type ImportResult struct {
	Inserted int `json:"inserted"`
	// Duplicate menghitung baris yang sudah pernah dimasukkan. Bukan galat:
	// berkas settlement sering dikirim ulang dengan tambahan baris baru.
	Duplicate int `json:"duplicate"`
	// Unmatched menghitung baris yang tidak cocok dengan pembayaran mana pun.
	// Baris itu tetap disimpan dan ditandai sebagai selisih, karena
	// membuangnya menghilangkan bukti bahwa uang itu pernah masuk.
	Unmatched int      `json:"unmatched"`
	Rejected  []string `json:"rejected,omitempty"`
}

// ImportSettlements memasukkan baris settlement dari penyedia.
//
// Satu transaksi dipakai untuk seluruh berkas. Berkas settlement adalah satu
// kesatuan laporan dari penyedia, dan memasukkannya separuh membuat laporan
// rekonsiliasi menunjukkan selisih yang sebenarnya hanya belum terbaca.
//
// Biaya penyedia disalin ke baris pembayaran agar laporan penerimaan dapat
// membandingkan yang ditagihkan dengan yang benar benar diterima tanpa
// menggabungkan dua tabel setiap kali.
func (s *Service) ImportSettlements(ctx context.Context, rows []SettlementRow) (*ImportResult, error) {
	if len(rows) == 0 {
		return &ImportResult{}, nil
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	hasil := &ImportResult{}
	for i, r := range rows {
		if r.ProviderRef == "" {
			hasil.Rejected = append(hasil.Rejected,
				fmt.Sprintf("baris ke-%d tanpa referensi penyedia", i+1))
			continue
		}
		if r.GrossCents <= 0 || r.FeeCents < 0 || r.FeeCents > r.GrossCents {
			hasil.Rejected = append(hasil.Rejected,
				fmt.Sprintf("baris ke-%d bernominal tidak sah: gross %d, biaya %d",
					i+1, r.GrossCents, r.FeeCents))
			continue
		}
		if r.SettledDate.IsZero() {
			hasil.Rejected = append(hasil.Rejected,
				fmt.Sprintf("baris ke-%d tanpa tanggal settlement", i+1))
			continue
		}

		// Pembayaran dicari dari referensi penyedia. Tidak ditemukan bukan
		// alasan menolak barisnya.
		var paymentID *uuid.UUID
		var nominalTagihan int64
		var id uuid.UUID
		errBaca := tx.QueryRow(ctx,
			`SELECT id, amount_cents FROM payments WHERE provider_ref = $1`,
			r.ProviderRef).Scan(&id, &nominalTagihan)
		switch {
		case errBaca == nil:
			paymentID = &id
		case errors.Is(errBaca, pgx.ErrNoRows):
			hasil.Unmatched++
		default:
			return nil, fmt.Errorf("mencari pembayaran: %w", errBaca)
		}

		var settlementID uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO settlements
			       (provider, provider_ref, payment_id, gross_cents, fee_cents,
			        net_cents, settled_date)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (provider, provider_ref) DO NOTHING
			RETURNING id`,
			s.d.Provider.Name(), r.ProviderRef, paymentID,
			r.GrossCents, r.FeeCents, r.GrossCents-r.FeeCents, r.SettledDate).
			Scan(&settlementID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Sudah pernah dimasukkan. Tidak dihitung sebagai tidak cocok
			// walau pembayarannya tidak ditemukan, karena barisnya memang
			// sudah ditangani pada pemasukan sebelumnya.
			hasil.Duplicate++
			if paymentID == nil {
				hasil.Unmatched--
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("menyimpan baris settlement: %w", err)
		}
		hasil.Inserted++

		if paymentID == nil {
			if err := tandaiSelisih(ctx, tx, nil, &settlementID, FlagOrphanSettlement,
				fmt.Sprintf("baris settlement %q tidak cocok dengan pembayaran mana pun",
					r.ProviderRef)); err != nil {
				return nil, err
			}
			continue
		}

		// Biaya penyedia disalin ke pembayaran.
		if _, err := tx.Exec(ctx,
			`UPDATE payments SET provider_fee_cents = $2 WHERE id = $1`,
			*paymentID, r.FeeCents); err != nil {
			return nil, fmt.Errorf("menyalin biaya penyedia: %w", err)
		}

		// Nominal yang berbeda dari tagihan ditandai, karena itu berarti yang
		// diterima tidak sama dengan yang ditagihkan.
		if r.GrossCents != nominalTagihan {
			if err := tandaiSelisih(ctx, tx, paymentID, &settlementID, FlagAmountMismatch,
				fmt.Sprintf("settlement %d sen, tagihan %d sen", r.GrossCents, nominalTagihan)); err != nil {
				return nil, err
			}
		}
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "settlements", Action: audit.ActionCreate,
		After: map[string]any{
			"inserted": hasil.Inserted, "duplicate": hasil.Duplicate,
			"unmatched": hasil.Unmatched,
		},
		Detail: fmt.Sprintf("berkas settlement dimasukkan: %d baru, %d sudah ada, %d tanpa pembayaran",
			hasil.Inserted, hasil.Duplicate, hasil.Unmatched),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan berkas settlement: %w", err)
	}
	return hasil, nil
}

// tandaiSelisih menyisipkan penandaan selisih bila belum ada yang terbuka.
func tandaiSelisih(ctx context.Context, tx pgx.Tx, paymentID, settlementID *uuid.UUID, kind, note string) error {
	pelaku := audit.ActorFrom(ctx)
	_, err := tx.Exec(ctx, `
		INSERT INTO reconciliation_flags (payment_id, settlement_id, kind, note, actor_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, paymentID, settlementID, kind, note, pelaku)
	if err != nil {
		return fmt.Errorf("menandai selisih: %w", err)
	}
	return nil
}

// ParseSettlementCSV membaca berkas settlement berbentuk CSV.
//
// Bentuk kolomnya mengikuti yang paling umum: referensi, gross, biaya,
// tanggal. Nominal dibaca sebagai rupiah lalu dikonversi ke sen, sama seperti
// pada webhook, karena penyedia Indonesia melaporkan rupiah.
//
// Baris pertama dilewati bila tampak sebagai judul kolom. Berkas settlement
// kadang dikirim dengan judul dan kadang tanpa, dan menolak salah satunya
// berarti petugas keuangan harus menyuntingnya lebih dahulu.
func ParseSettlementCSV(r io.Reader) ([]SettlementRow, []string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	rekaman, err := cr.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("membaca berkas settlement: %w", err)
	}

	var (
		out     []SettlementRow
		ditolak []string
	)
	for i, baris := range rekaman {
		if len(baris) < 4 {
			ditolak = append(ditolak, fmt.Sprintf("baris ke-%d kurang dari empat kolom", i+1))
			continue
		}
		ref := strings.TrimSpace(baris[0])
		if ref == "" {
			ditolak = append(ditolak, fmt.Sprintf("baris ke-%d tanpa referensi", i+1))
			continue
		}

		gross, okG := bacaNominal(baris[1])
		biaya, okB := bacaNominal(baris[2])
		tanggal, errT := time.ParseInLocation("2006-01-02", strings.TrimSpace(baris[3]), time.Local)

		if !okG || !okB || errT != nil {
			// Baris pertama yang tidak terbaca angkanya hampir pasti judul
			// kolom, jadi dilewati tanpa dilaporkan sebagai kesalahan.
			if i == 0 {
				continue
			}
			ditolak = append(ditolak, fmt.Sprintf("baris ke-%d tidak terbaca", i+1))
			continue
		}

		out = append(out, SettlementRow{
			ProviderRef: ref, GrossCents: gross, FeeCents: biaya, SettledDate: tanggal,
		})
	}
	return out, ditolak, nil
}

// ReconcileRow adalah satu baris laporan rekonsiliasi.
type ReconcileRow struct {
	PaymentID   *uuid.UUID `json:"payment_id,omitempty"`
	OrderNo     string     `json:"order_no,omitempty"`
	ProviderRef string     `json:"provider_ref"`
	Status      string     `json:"status,omitempty"`
	AmountCents int64      `json:"amount_cents"`
	GrossCents  *int64     `json:"gross_cents,omitempty"`
	FeeCents    *int64     `json:"fee_cents,omitempty"`
	NetCents    *int64     `json:"net_cents,omitempty"`
	SettledDate *string    `json:"settled_date,omitempty"`
	// DiffCents adalah selisih antara yang ditagihkan dan yang disettlement.
	// Pembayaran yang belum muncul pada settlement bernilai sebesar
	// nominalnya, bukan nol, karena seluruh nominalnya memang belum diterima
	// (SRS-PAY-005).
	DiffCents int64 `json:"diff_cents"`
	// Flag menyebut jenis selisihnya, kosong bila cocok.
	Flag string `json:"flag,omitempty"`
	// FlagOpen menandai selisih ini sudah ditandai dan belum diselesaikan.
	FlagOpen bool   `json:"flag_open"`
	FlagNote string `json:"flag_note,omitempty"`
}

// ReconcileSummary merangkum satu periode.
type ReconcileSummary struct {
	From string `json:"from"`
	To   string `json:"to"`

	PaymentCount int   `json:"payment_count"`
	BilledCents  int64 `json:"billed_cents"`
	SettledCents int64 `json:"settled_cents"`
	FeeCents     int64 `json:"fee_cents"`
	NetCents     int64 `json:"net_cents"`
	// DiffCents adalah jumlah seluruh selisih, yaitu yang ditagihkan namun
	// belum terlihat pada settlement.
	DiffCents int64 `json:"diff_cents"`
	// Unsettled menghitung pembayaran yang belum muncul pada settlement.
	Unsettled int `json:"unsettled"`
	// OrphanSettlements menghitung baris settlement tanpa pembayaran.
	OrphanSettlements int `json:"orphan_settlements"`
}

// ReconcileResult adalah laporan rekonsiliasi satu periode.
type ReconcileResult struct {
	Summary ReconcileSummary `json:"summary"`
	Rows    []ReconcileRow   `json:"rows"`
}

// ReconcileFilter menyaring laporan rekonsiliasi.
type ReconcileFilter struct {
	// From dan Until berbentuk YYYY-MM-DD, keduanya inklusif.
	From  string
	Until string
	// OnlyDiff membatasi laporan pada baris yang berselisih. Dipakai tampilan
	// tindak lanjut, yang hanya peduli pada yang belum beres.
	OnlyDiff bool
}

// Reconcile membandingkan pembayaran dengan settlement pada satu periode.
//
// Pembayaran yang belum muncul pada settlement tetap tampil sebagai selisih,
// bukan disembunyikan (SRS-PAY-005). Itu justru kasus yang paling perlu
// dilihat: uang yang sudah ditagihkan namun belum diterima.
//
// Baris settlement tanpa pembayaran juga ikut tampil, karena uang yang masuk
// tanpa diketahui asalnya sama perlunya ditelusuri.
func (s *Service) Reconcile(ctx context.Context, f ReconcileFilter) (*ReconcileResult, error) {
	if f.From == "" || f.Until == "" {
		return nil, fmt.Errorf("%w: periode awal dan akhir wajib diisi", ErrPeriodInvalid)
	}
	dari, err := time.ParseInLocation("2006-01-02", f.From, time.Local)
	if err != nil {
		return nil, fmt.Errorf("%w: tanggal awal tidak terbaca", ErrPeriodInvalid)
	}
	sampai, err := time.ParseInLocation("2006-01-02", f.Until, time.Local)
	if err != nil {
		return nil, fmt.Errorf("%w: tanggal akhir tidak terbaca", ErrPeriodInvalid)
	}
	if sampai.Before(dari) {
		return nil, fmt.Errorf("%w: tanggal akhir sebelum tanggal awal", ErrPeriodInvalid)
	}

	out := &ReconcileResult{
		Summary: ReconcileSummary{From: f.From, To: f.Until},
		Rows:    []ReconcileRow{},
	}

	// Dua bagian digabung dengan UNION ALL, bukan dengan FULL OUTER JOIN,
	// karena keduanya disaring menurut tanggal yang berbeda: pembayaran
	// menurut waktu pembayarannya, settlement menurut tanggal settlementnya.
	// FULL OUTER JOIN memaksa satu syarat tanggal untuk keduanya, dan
	// pembayaran akhir bulan yang disettlement awal bulan berikutnya akan
	// hilang dari kedua periode.
	rows, err := s.d.Pool.Query(ctx, `
		SELECT p.id, o.order_no, coalesce(p.provider_ref, ''), p.status::text,
		       p.amount_cents,
		       st.gross_cents, st.fee_cents, st.net_cents, st.settled_date,
		       fl.kind, fl.note
		FROM   payments p
		JOIN   orders o ON o.id = p.order_id
		LEFT JOIN settlements st ON st.payment_id = p.id
		LEFT JOIN reconciliation_flags fl
		       ON fl.payment_id = p.id AND fl.resolved_at IS NULL
		WHERE  p.status IN ('SUCCESS', 'REFUNDED')
		  AND  p.paid_at >= $1::date
		  AND  p.paid_at < ($2::date + 1)

		UNION ALL

		SELECT NULL, '', st.provider_ref, '', 0,
		       st.gross_cents, st.fee_cents, st.net_cents, st.settled_date,
		       fl.kind, fl.note
		FROM   settlements st
		LEFT JOIN reconciliation_flags fl
		       ON fl.settlement_id = st.id AND fl.resolved_at IS NULL
		WHERE  st.payment_id IS NULL
		  AND  st.settled_date BETWEEN $1::date AND $2::date

		ORDER BY 9 NULLS LAST, 3`, dari, sampai)
	if err != nil {
		return nil, fmt.Errorf("menyusun laporan rekonsiliasi: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			r       ReconcileRow
			tanggal *time.Time
			kind    *string
			note    *string
		)
		err := rows.Scan(&r.PaymentID, &r.OrderNo, &r.ProviderRef, &r.Status,
			&r.AmountCents, &r.GrossCents, &r.FeeCents, &r.NetCents, &tanggal,
			&kind, &note)
		if err != nil {
			return nil, fmt.Errorf("membaca baris rekonsiliasi: %w", err)
		}
		if tanggal != nil {
			t := tanggal.Format("2006-01-02")
			r.SettledDate = &t
		}
		if kind != nil {
			r.Flag, r.FlagOpen = *kind, true
		}
		if note != nil {
			r.FlagNote = *note
		}

		switch {
		case r.PaymentID == nil:
			// Settlement tanpa pembayaran. Selisihnya negatif sebesar
			// grossnya: uang masuk yang belum ada tagihannya.
			if r.GrossCents != nil {
				r.DiffCents = -*r.GrossCents
			}
			if r.Flag == "" {
				r.Flag = FlagOrphanSettlement
			}
			out.Summary.OrphanSettlements++

		case r.GrossCents == nil:
			// Pembayaran yang belum muncul pada settlement.
			r.DiffCents = r.AmountCents
			if r.Flag == "" {
				r.Flag = FlagNoSettlement
			}
			out.Summary.Unsettled++
			out.Summary.PaymentCount++
			out.Summary.BilledCents += r.AmountCents

		default:
			r.DiffCents = r.AmountCents - *r.GrossCents
			if r.DiffCents != 0 && r.Flag == "" {
				r.Flag = FlagAmountMismatch
			}
			out.Summary.PaymentCount++
			out.Summary.BilledCents += r.AmountCents
			out.Summary.SettledCents += *r.GrossCents
			if r.FeeCents != nil {
				out.Summary.FeeCents += *r.FeeCents
			}
			if r.NetCents != nil {
				out.Summary.NetCents += *r.NetCents
			}
		}
		out.Summary.DiffCents += r.DiffCents

		if f.OnlyDiff && r.DiffCents == 0 && r.Flag == "" {
			continue
		}
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("menyusun laporan rekonsiliasi: %w", err)
	}

	// Pembacaan laporan keuangan tercatat. Laporan ini memuat seluruh
	// penerimaan satu periode, dan siapa yang membukanya perlu dapat
	// ditelusuri.
	if err := audit.RecordOutside(ctx, s.d.Pool, audit.Entry{
		Entity: "settlements", Action: audit.ActionAccess,
		Detail: fmt.Sprintf("laporan rekonsiliasi dibaca untuk periode %s sampai %s, %d baris",
			f.From, f.Until, len(out.Rows)),
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ExportCSV menuliskan laporan rekonsiliasi sebagai CSV.
//
// Nominal ditulis dalam rupiah, bukan sen, karena yang membacanya petugas
// keuangan dengan lembar kerja, bukan program.
func (r *ReconcileResult) ExportCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{
		"nomor_pesanan", "referensi_penyedia", "status", "ditagihkan_rupiah",
		"settlement_rupiah", "biaya_rupiah", "diterima_rupiah",
		"tanggal_settlement", "selisih_rupiah", "penanda", "catatan",
	}); err != nil {
		return fmt.Errorf("menulis judul kolom: %w", err)
	}

	for _, row := range r.Rows {
		if err := cw.Write([]string{
			row.OrderNo,
			row.ProviderRef,
			row.Status,
			rupiah(&row.AmountCents),
			rupiah(row.GrossCents),
			rupiah(row.FeeCents),
			rupiah(row.NetCents),
			nilaiAtauKosong(row.SettledDate),
			rupiah(&row.DiffCents),
			row.Flag,
			row.FlagNote,
		}); err != nil {
			return fmt.Errorf("menulis baris: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// rupiah menuliskan sen sebagai rupiah berdesimal dua angka.
func rupiah(sen *int64) string {
	if sen == nil {
		return ""
	}
	n := *sen
	tanda := ""
	if n < 0 {
		tanda, n = "-", -n
	}
	return fmt.Sprintf("%s%d.%02d", tanda, n/100, n%100)
}

func nilaiAtauKosong(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Flag adalah satu penandaan selisih.
type Flag struct {
	ID           uuid.UUID  `json:"id"`
	PaymentID    *uuid.UUID `json:"payment_id,omitempty"`
	SettlementID *uuid.UUID `json:"settlement_id,omitempty"`
	Kind         string     `json:"kind"`
	Note         string     `json:"note,omitempty"`
	ActorID      *uuid.UUID `json:"actor_id,omitempty"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy   *uuid.UUID `json:"resolved_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// FlagInput adalah penandaan selisih yang dibuat manusia.
type FlagInput struct {
	PaymentID    *uuid.UUID
	SettlementID *uuid.UUID
	Kind         string
	Note         string
}

// FlagDiscrepancy menandai sebuah selisih untuk ditindaklanjuti.
func (s *Service) FlagDiscrepancy(ctx context.Context, in FlagInput) (*Flag, error) {
	if in.PaymentID == nil && in.SettlementID == nil {
		return nil, ErrFlagNoSubject
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = FlagAmountMismatch
	}

	pelaku := audit.ActorFrom(ctx)
	var x Flag
	err := s.d.Pool.QueryRow(ctx, `
		INSERT INTO reconciliation_flags (payment_id, settlement_id, kind, note, actor_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, payment_id, settlement_id, kind, note, actor_id,
		          resolved_at, resolved_by, created_at`,
		in.PaymentID, in.SettlementID, kind, strings.TrimSpace(in.Note), pelaku).
		Scan(&x.ID, &x.PaymentID, &x.SettlementID, &x.Kind, &x.Note, &x.ActorID,
			&x.ResolvedAt, &x.ResolvedBy, &x.CreatedAt)
	if isUniqueViolation(err) {
		return nil, ErrAlreadyFlagged
	}
	if err != nil {
		return nil, fmt.Errorf("menandai selisih: %w", err)
	}
	return &x, nil
}

// ResolveFlag menutup sebuah penandaan selisih.
//
// Catatan penyelesaiannya ditambahkan ke catatan yang sudah ada, bukan
// menggantinya, karena catatan awal menjelaskan apa selisihnya dan catatan
// penutup menjelaskan bagaimana diselesaikan. Keduanya dibutuhkan saat
// selisih serupa muncul lagi.
func (s *Service) ResolveFlag(ctx context.Context, id uuid.UUID, catatan string) error {
	pelaku := audit.ActorFrom(ctx)
	if pelaku == nil {
		// Penyelesaian wajib menyebut pelakunya, dan kekangan basis data
		// menegakkannya. Ditolak di sini agar galatnya dapat dibaca.
		return fmt.Errorf("penyelesaian selisih harus dilakukan pengguna yang masuk")
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var sebelum string
	err = tx.QueryRow(ctx, `
		SELECT note FROM reconciliation_flags
		WHERE  id = $1 AND resolved_at IS NULL FOR UPDATE`, id).Scan(&sebelum)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFlagNotFound
	}
	if err != nil {
		return fmt.Errorf("mengunci penandaan: %w", err)
	}

	gabung := sebelum
	if c := strings.TrimSpace(catatan); c != "" {
		if gabung != "" {
			gabung += " | "
		}
		gabung += "diselesaikan: " + c
	}

	if _, err := tx.Exec(ctx, `
		UPDATE reconciliation_flags
		SET    resolved_at = now(), resolved_by = $2, note = $3
		WHERE  id = $1`, id, *pelaku, gabung); err != nil {
		return fmt.Errorf("menyelesaikan penandaan: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "reconciliation_flags", EntityID: &id, Action: audit.ActionUpdate,
		After:  map[string]any{"resolved": true},
		Detail: "selisih rekonsiliasi diselesaikan: " + catatan,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OpenFlags mengembalikan selisih yang belum diselesaikan.
func (s *Service) OpenFlags(ctx context.Context, batas int) ([]Flag, error) {
	if batas <= 0 || batas > 500 {
		batas = 100
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, payment_id, settlement_id, kind, note, actor_id,
		       resolved_at, resolved_by, created_at
		FROM   reconciliation_flags
		WHERE  resolved_at IS NULL
		ORDER  BY created_at DESC
		LIMIT  $1`, batas)
	if err != nil {
		return nil, fmt.Errorf("membaca selisih terbuka: %w", err)
	}
	defer rows.Close()

	out := []Flag{}
	for rows.Next() {
		var x Flag
		err := rows.Scan(&x.ID, &x.PaymentID, &x.SettlementID, &x.Kind, &x.Note,
			&x.ActorID, &x.ResolvedAt, &x.ResolvedBy, &x.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("membaca baris selisih: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// CorrectFee mengoreksi biaya penyedia pada sebuah pembayaran.
//
// Koreksi manual hanya untuk peran berwenang, dan tercatat pada jejak audit
// beserta nilai lama dan nilai barunya (SRS-PAY-005). Kewenangannya dijaga
// lapisan HTTP; yang dijaga di sini adalah tercatatnya perubahan.
func (s *Service) CorrectFee(ctx context.Context, paymentID uuid.UUID, feeCents int64, alasan string) error {
	if feeCents < 0 {
		return fmt.Errorf("%w: tidak boleh negatif", ErrFeeInvalid)
	}
	if strings.TrimSpace(alasan) == "" {
		return ErrReasonRequired
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var lama, nominal int64
	err = tx.QueryRow(ctx, `
		SELECT provider_fee_cents, amount_cents
		FROM   payments WHERE id = $1 FOR UPDATE`, paymentID).Scan(&lama, &nominal)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mengunci pembayaran: %w", err)
	}
	// Biaya yang melebihi nominal pembayaran hampir pasti salah satuan,
	// misalnya rupiah dimasukkan sebagai sen.
	if feeCents > nominal {
		return fmt.Errorf("%w: %d sen melebihi nominal pembayaran %d sen. "+
			"Periksa satuannya, nilai diisi dalam sen bukan rupiah",
			ErrFeeInvalid, feeCents, nominal)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE payments SET provider_fee_cents = $2 WHERE id = $1`,
		paymentID, feeCents); err != nil {
		return fmt.Errorf("mengoreksi biaya penyedia: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "payments", EntityID: &paymentID, Action: audit.ActionUpdate,
		Before: map[string]any{"provider_fee_cents": lama},
		After:  map[string]any{"provider_fee_cents": feeCents},
		Detail: fmt.Sprintf("koreksi manual biaya penyedia dari %d menjadi %d sen, alasan: %s",
			lama, feeCents, alasan),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
