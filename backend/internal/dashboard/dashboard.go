// Package dashboard menghitung indikator operasional harian.
//
// Setiap indikator di sini punya satu definisi yang ditulis di tempatnya, dan
// definisi itulah yang nanti dipakai laporan. SRS-ADM-001 mewajibkan angka
// omzet pada dashboard sama dengan angka pada laporan penjualan periode yang
// sama, dan satu satunya cara menjaminnya adalah menghitungnya dari definisi
// yang sama, bukan dari dua query yang kebetulan mirip.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Galat domain dashboard.
var (
	ErrPeriodInvalid = errors.New("periode tidak sah")
)

// Filter menyaring indikator.
type Filter struct {
	// From dan Until berbentuk YYYY-MM-DD, keduanya inklusif. Kosong keduanya
	// berarti hari ini.
	From  string
	Until string
	// DepotID membatasi indikator pada satu depo. Kosong berarti seluruh depo.
	DepotID *uuid.UUID
}

// Indicators adalah indikator operasional.
//
// Indikator keuangan dipisahkan ke dalam Finance, yang hanya diisi bila
// pemanggil berwenang melihatnya (SRS-ADM-001). Memisahkannya di sini, bukan
// menyaringnya di lapisan HTTP, membuat angka keuangan tidak mungkin ikut
// terkirim karena ada yang lupa menyaring satu kolom.
type Indicators struct {
	From string `json:"from"`
	To   string `json:"to"`

	// NewOrders adalah pesanan yang dibuat dalam periode, apa pun statusnya
	// sekarang. Dihitung menurut waktu pembuatan, bukan tanggal pengiriman,
	// karena yang diukur adalah masuknya permintaan.
	NewOrders int `json:"new_orders"`

	// ScheduledToday adalah pesanan yang dijadwalkan diantar dalam periode,
	// belum dibatalkan. Dihitung menurut tanggal pengiriman, karena yang
	// diukur adalah beban operasional hari itu.
	ScheduledToday int `json:"scheduled_today"`

	// PendingPayments adalah pesanan yang masih menunggu pembayaran, tanpa
	// batasan periode. Pesanan yang menunggu bayar sejak kemarin tetap perlu
	// terlihat hari ini, dan menyaringnya menurut periode akan
	// menyembunyikannya.
	PendingPayments int `json:"pending_payments"`

	// ActiveDeliveries adalah pengiriman yang sedang berjalan, yaitu sudah
	// ditugaskan dan belum selesai maupun gagal. Tanpa batasan periode,
	// karena yang diukur adalah keadaan sekarang.
	ActiveDeliveries int `json:"active_deliveries"`

	// Late adalah pesanan yang tanggal pengirimannya sudah lewat namun belum
	// selesai dan belum dibatalkan. Inilah yang paling perlu dilihat admin
	// pagi hari.
	Late int `json:"late"`

	// FailedDeliveries adalah pengiriman yang gagal dalam periode.
	FailedDeliveries int `json:"failed_deliveries"`

	// Finance hanya terisi bagi peran yang berwenang.
	Finance *FinanceIndicators `json:"finance,omitempty"`
}

// FinanceIndicators adalah indikator yang menyangkut uang.
type FinanceIndicators struct {
	// RevenueCents adalah omzet periode: jumlah total pesanan yang tidak
	// dibatalkan, dihitung menurut waktu pembuatan pesanan.
	//
	// Definisi ini yang dipakai laporan penjualan. Yang dihitung adalah nilai
	// pesanan, bukan uang yang sudah diterima, karena penjualan bertermin
	// sudah menjadi penjualan walau pembayarannya belakangan. Uang yang benar
	// benar diterima ada pada CollectedCents.
	RevenueCents int64 `json:"revenue_cents"`

	// CollectedCents adalah pembayaran berhasil dalam periode, dihitung
	// menurut waktu pembayaran. Berbeda dari omzet, dan bedanya memang ada:
	// pesanan bertermin menambah omzet tanpa menambah penerimaan.
	CollectedCents int64 `json:"collected_cents"`

	// OutstandingCents adalah piutang penjualan bertermin yang belum
	// dilunasi, tanpa batasan periode. Piutang lama justru yang paling perlu
	// terlihat.
	OutstandingCents int64 `json:"outstanding_cents"`

	// RefundedCents adalah refund dalam periode.
	RefundedCents int64 `json:"refunded_cents"`

	// CancelledOrders dan CancelledCents adalah pesanan yang dibatalkan dalam
	// periode beserta nilainya. Ditaruh di bagian keuangan karena nilainya
	// termasuk angka uang.
	CancelledOrders int   `json:"cancelled_orders"`
	CancelledCents  int64 `json:"cancelled_cents"`
}

// Service menghitung indikator dashboard.
type Service struct{ pool *pgxpool.Pool }

// NewService membuat layanan dashboard.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// rentang menguraikan periode dari saringan.
//
// Kosong berarti hari ini, karena itulah yang dilihat admin saat membuka
// dashboard tanpa memilih apa pun.
func rentang(f Filter) (dari, sampai time.Time, err error) {
	if f.From == "" && f.Until == "" {
		n := time.Now()
		hari := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
		return hari, hari, nil
	}
	if f.From == "" || f.Until == "" {
		return time.Time{}, time.Time{},
			fmt.Errorf("%w: periode awal dan akhir harus diisi bersama", ErrPeriodInvalid)
	}
	dari, err = time.ParseInLocation("2006-01-02", f.From, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: tanggal awal tidak terbaca", ErrPeriodInvalid)
	}
	sampai, err = time.ParseInLocation("2006-01-02", f.Until, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: tanggal akhir tidak terbaca", ErrPeriodInvalid)
	}
	if sampai.Before(dari) {
		return time.Time{}, time.Time{},
			fmt.Errorf("%w: tanggal akhir sebelum tanggal awal", ErrPeriodInvalid)
	}
	return dari, sampai, nil
}

// Compute menghitung indikator operasional.
//
// withFinance menentukan apakah indikator keuangan ikut dihitung. Bila tidak,
// querynya pun tidak dijalankan: selain menjaga kewenangan, itu menghemat
// pekerjaan bagi peran yang memang tidak membutuhkannya.
func (s *Service) Compute(ctx context.Context, f Filter, withFinance bool) (*Indicators, error) {
	dari, sampai, err := rentang(f)
	if err != nil {
		return nil, err
	}

	out := &Indicators{
		From: dari.Format("2006-01-02"),
		To:   sampai.Format("2006-01-02"),
	}

	// Seluruh indikator operasional dihitung dalam satu query. Enam query
	// terpisah berarti enam perjalanan ke basis data untuk satu halaman yang
	// disegarkan berkali kali.
	err = s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM orders o
		   WHERE o.created_at >= $1::date AND o.created_at < ($2::date + 1)
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT count(*) FROM orders o
		   WHERE o.scheduled_date BETWEEN $1::date AND $2::date
		     AND o.status <> 'CANCELLED'
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT count(*) FROM orders o
		   WHERE o.status = 'WAITING_PAYMENT'
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT count(*) FROM deliveries d
		   WHERE d.status IN ('ASSIGNED','ACCEPTED','ON_THE_WAY','ARRIVED')
		     AND ($3::uuid IS NULL OR d.depot_id = $3)),

		  (SELECT count(*) FROM orders o
		   WHERE o.scheduled_date < current_date
		     AND o.status NOT IN ('COMPLETED','CANCELLED')
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT count(*) FROM delivery_status_history h
		   JOIN   deliveries d ON d.id = h.delivery_id
		   WHERE  h.to_status = 'FAILED_DELIVERY'
		     AND  h.occurred_at >= $1::date AND h.occurred_at < ($2::date + 1)
		     AND  ($3::uuid IS NULL OR d.depot_id = $3))`,
		dari, sampai, f.DepotID).
		Scan(&out.NewOrders, &out.ScheduledToday, &out.PendingPayments,
			&out.ActiveDeliveries, &out.Late, &out.FailedDeliveries)
	if err != nil {
		return nil, fmt.Errorf("menghitung indikator operasional: %w", err)
	}

	if !withFinance {
		return out, nil
	}

	var fin FinanceIndicators
	err = s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT coalesce(sum(o.total_cents), 0) FROM orders o
		   WHERE o.created_at >= $1::date AND o.created_at < ($2::date + 1)
		     AND o.status <> 'CANCELLED'
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT coalesce(sum(p.amount_cents), 0) FROM payments p
		   JOIN   orders o ON o.id = p.order_id
		   WHERE  p.status IN ('SUCCESS','REFUNDED')
		     AND  p.paid_at >= $1::date AND p.paid_at < ($2::date + 1)
		     AND  ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT coalesce(sum(o.total_cents), 0) FROM orders o
		   WHERE o.payment_term_days IS NOT NULL
		     AND o.status <> 'CANCELLED'
		     AND NOT EXISTS (SELECT 1 FROM payments p
		                     WHERE p.order_id = o.id
		                       AND p.status IN ('SUCCESS','REFUNDED'))
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT coalesce(sum(r.amount_cents), 0) FROM refunds r
		   JOIN   payments p ON p.id = r.payment_id
		   JOIN   orders o   ON o.id = p.order_id
		   WHERE  r.created_at >= $1::date AND r.created_at < ($2::date + 1)
		     AND  ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT count(*) FROM orders o
		   WHERE o.status = 'CANCELLED'
		     AND o.updated_at >= $1::date AND o.updated_at < ($2::date + 1)
		     AND ($3::uuid IS NULL OR o.depot_id = $3)),

		  (SELECT coalesce(sum(o.total_cents), 0) FROM orders o
		   WHERE o.status = 'CANCELLED'
		     AND o.updated_at >= $1::date AND o.updated_at < ($2::date + 1)
		     AND ($3::uuid IS NULL OR o.depot_id = $3))`,
		dari, sampai, f.DepotID).
		Scan(&fin.RevenueCents, &fin.CollectedCents, &fin.OutstandingCents,
			&fin.RefundedCents, &fin.CancelledOrders, &fin.CancelledCents)
	if err != nil {
		return nil, fmt.Errorf("menghitung indikator keuangan: %w", err)
	}
	out.Finance = &fin
	return out, nil
}

// Definition menjelaskan satu indikator.
type Definition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Finance menandai indikator yang hanya untuk peran berwenang.
	Finance bool `json:"finance"`
}

// Definitions mengembalikan definisi setiap indikator.
//
// SRS-ADM-001 mewajibkan definisi setiap indikator didokumentasikan dan
// dipakai konsisten pada laporan. Dipaparkan lewat API, bukan hanya ditulis
// pada dokumen, supaya yang membaca angkanya dapat melihat definisinya di
// tempat yang sama dan tidak menebak.
func Definitions() []Definition {
	return []Definition{
		{Key: "new_orders", Label: "Pesanan baru",
			Description: "Pesanan yang dibuat dalam periode, apa pun statusnya sekarang. " +
				"Dihitung menurut waktu pembuatan, bukan tanggal pengiriman."},
		{Key: "scheduled_today", Label: "Pengiriman terjadwal",
			Description: "Pesanan yang dijadwalkan diantar dalam periode dan belum dibatalkan. " +
				"Dihitung menurut tanggal pengiriman."},
		{Key: "pending_payments", Label: "Menunggu pembayaran",
			Description: "Pesanan yang masih menunggu pembayaran, tanpa batasan periode. " +
				"Pesanan yang menunggu sejak kemarin tetap terlihat hari ini."},
		{Key: "active_deliveries", Label: "Pengiriman aktif",
			Description: "Pengiriman yang sudah ditugaskan dan belum selesai maupun gagal. " +
				"Menggambarkan keadaan sekarang, bukan periode."},
		{Key: "late", Label: "Keterlambatan",
			Description: "Pesanan yang tanggal pengirimannya sudah lewat namun belum selesai " +
				"dan belum dibatalkan."},
		{Key: "failed_deliveries", Label: "Gagal kirim",
			Description: "Pengiriman yang gagal dalam periode, dihitung dari riwayat status " +
				"pengiriman."},
		{Key: "revenue_cents", Label: "Omzet", Finance: true,
			Description: "Jumlah nilai pesanan yang tidak dibatalkan, dihitung menurut waktu " +
				"pembuatan pesanan. Yang dihitung nilai pesanan, bukan uang yang diterima, " +
				"karena penjualan bertermin sudah menjadi penjualan walau pembayarannya " +
				"belakangan."},
		{Key: "collected_cents", Label: "Penerimaan", Finance: true,
			Description: "Pembayaran berhasil dalam periode, dihitung menurut waktu " +
				"pembayaran. Berbeda dari omzet, dan bedanya memang ada."},
		{Key: "outstanding_cents", Label: "Piutang", Finance: true,
			Description: "Nilai pesanan bertermin yang belum dilunasi dan belum dibatalkan, " +
				"tanpa batasan periode."},
		{Key: "refunded_cents", Label: "Refund", Finance: true,
			Description: "Jumlah pengembalian dana dalam periode."},
		{Key: "cancelled_orders", Label: "Pesanan dibatalkan", Finance: true,
			Description: "Pesanan yang dibatalkan dalam periode, dihitung menurut waktu " +
				"perubahan terakhirnya."},
		{Key: "cancelled_cents", Label: "Nilai pembatalan", Finance: true,
			Description: "Jumlah nilai pesanan yang dibatalkan dalam periode."},
	}
}
