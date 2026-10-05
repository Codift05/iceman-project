package payment_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/payment"
)

// lunas menyiapkan satu pembayaran yang sudah berhasil.
func (l *lingkungan) lunas(t *testing.T) *payment.Payment {
	t.Helper()
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}
	nominal := p.AmountCents
	ev := l.webhook(t, p, "settlement", &nominal)
	if _, err := l.bayar.ApplyEvent(ctx, ev); err != nil {
		t.Fatalf("memproses pembayaran: %v", err)
	}
	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.Status != payment.StatusSuccess {
		t.Fatalf("pembayaran uji berstatus %q, seharusnya SUCCESS", lagi.Status)
	}
	return lagi
}

// TestRefund_PenuhMengubahStatusMenjadiRefunded menjaga SRS-PAY-004.
func TestRefund_PenuhMengubahStatusMenjadiRefunded(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	r, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{Reason: "pesanan dibatalkan"})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	// Nominal nol berarti refund penuh atas sisanya.
	if r.AmountCents != p.AmountCents {
		t.Fatalf("nominal refund %d, seharusnya %d", r.AmountCents, p.AmountCents)
	}
	if !r.IsManual {
		t.Fatal("penyedia manual seharusnya menghasilkan refund manual")
	}

	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.Status != payment.StatusRefunded {
		t.Fatalf("status %q, seharusnya REFUNDED", lagi.Status)
	}
	if lagi.Refundable() != 0 {
		t.Fatalf("sisa refund %d, seharusnya 0", lagi.Refundable())
	}
}

// TestRefund_SebagianTidakMengubahStatus menjaga agar sisa yang masih dapat
// dikembalikan tidak tersembunyi oleh status REFUNDED.
func TestRefund_SebagianTidakMengubahStatus(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		AmountCents: 1000000, Reason: "satu item tidak terkirim",
	}); err != nil {
		t.Fatalf("refund sebagian: %v", err)
	}

	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.Status != payment.StatusSuccess {
		t.Fatalf("status %q, refund sebagian seharusnya tidak mengubah status", lagi.Status)
	}
	if lagi.RefundedCents != 1000000 {
		t.Fatalf("jumlah terefund %d, seharusnya 1000000", lagi.RefundedCents)
	}
	if lagi.Refundable() != p.AmountCents-1000000 {
		t.Fatalf("sisa refund %d, seharusnya %d", lagi.Refundable(), p.AmountCents-1000000)
	}
}

// TestRefund_KeduaYangMelebihiSisaDitolak menjaga janji yang disebut langsung
// pada SRS-PAY-004.
func TestRefund_KeduaYangMelebihiSisaDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	separuh := p.AmountCents / 2
	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		AmountCents: separuh, Reason: "refund pertama",
	}); err != nil {
		t.Fatalf("refund pertama: %v", err)
	}

	// Refund kedua yang melebihi sisanya ditolak.
	_, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		AmountCents: p.AmountCents, Reason: "refund kedua",
	})
	if !errors.Is(err, payment.ErrRefundExceeds) {
		t.Fatalf("galat %v, seharusnya ErrRefundExceeds", err)
	}

	// Jumlah yang terefund tidak berubah akibat percobaan yang gagal.
	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.RefundedCents != separuh {
		t.Fatalf("jumlah terefund %d, seharusnya tetap %d", lagi.RefundedCents, separuh)
	}
}

// TestRefund_AlasanWajib menjaga SRS-PAY-004.
func TestRefund_AlasanWajib(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	for _, alasan := range []string{"", "   "} {
		if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
			Reason: alasan,
		}); !errors.Is(err, payment.ErrReasonRequired) {
			t.Fatalf("alasan %q seharusnya ditolak, dapat %v", alasan, err)
		}
	}
}

// TestRefund_HanyaAtasPembayaranBerhasil menjaga SRS-PAY-004. Pembayaran yang
// belum berhasil tidak punya dana untuk dikembalikan.
func TestRefund_HanyaAtasPembayaranBerhasil(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}

	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		Reason: "coba refund yang belum dibayar",
	}); !errors.Is(err, payment.ErrNotRefundable) {
		t.Fatalf("galat %v, seharusnya ErrNotRefundable", err)
	}
}

// TestRefund_SeluruhnyaSudahDikembalikanDitolak menjaga agar pembayaran yang
// sudah habis tidak dapat direfund lagi.
func TestRefund_SeluruhnyaSudahDikembalikanDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{Reason: "penuh"}); err != nil {
		t.Fatalf("refund penuh: %v", err)
	}
	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		AmountCents: 1000, Reason: "sekali lagi",
	}); !errors.Is(err, payment.ErrNotRefundable) {
		t.Fatalf("galat %v, seharusnya ErrNotRefundable", err)
	}
}

// TestRefund_NominalNegatifDitolak menjaga agar refund tidak dapat dipakai
// menambah uang masuk.
func TestRefund_NominalNegatifDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	if _, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
		AmountCents: -5000, Reason: "coba negatif",
	}); !errors.Is(err, payment.ErrRefundExceeds) {
		t.Fatalf("galat %v, seharusnya ditolak", err)
	}
}

// TestRefund_BersamaanTidakMelebihiPembayaran adalah uji konkurensi yang
// paling berdampak pada uang di domain ini.
//
// Tanpa penguncian baris pembayaran, dua refund bersamaan dapat sama sama
// membaca sisa yang sama lalu keduanya merasa cukup, dan jumlah yang
// dikembalikan melebihi yang pernah masuk.
func TestRefund_BersamaanTidakMelebihiPembayaran(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	// Delapan percobaan, masing masing separuh nominal. Hanya dua yang boleh
	// berhasil.
	const penyerbu = 8
	separuh := p.AmountCents / 2

	var berhasil, gagal int32
	mulai := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-mulai
			_, err := l.bayar.Refund(ctx, p.ID, payment.RefundInput{
				AmountCents: separuh, Reason: "refund bersamaan",
			})
			if err == nil {
				atomic.AddInt32(&berhasil, 1)
			} else {
				atomic.AddInt32(&gagal, 1)
			}
		}(i)
	}
	close(mulai)
	wg.Wait()

	if berhasil != 2 {
		t.Fatalf("refund berhasil %d, seharusnya tepat 2", berhasil)
	}
	if gagal != penyerbu-2 {
		t.Fatalf("refund gagal %d, seharusnya %d", gagal, penyerbu-2)
	}

	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	// Inilah yang paling penting: jumlah yang dikembalikan tidak melebihi
	// yang pernah masuk.
	if lagi.RefundedCents > lagi.AmountCents {
		t.Fatalf("jumlah terefund %d melebihi pembayaran %d",
			lagi.RefundedCents, lagi.AmountCents)
	}
	if lagi.RefundedCents != separuh*2 {
		t.Fatalf("jumlah terefund %d, seharusnya %d", lagi.RefundedCents, separuh*2)
	}
	if lagi.Status != payment.StatusRefunded {
		t.Fatalf("status %q, seharusnya REFUNDED karena sudah penuh", lagi.Status)
	}
}

// TestRefund_TercatatBesertaPelakuDanAlasan menjaga SRS-PAY-004: setiap refund
// mencatat referensi, waktu, pelaku, dan alasan.
func TestRefund_TercatatBesertaPelakuDanAlasan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)
	adm := l.buatAdmin(t)

	const alasan = "pelanggan membatalkan setelah membayar"
	r, err := l.bayar.Refund(sebagai(adm), p.ID, payment.RefundInput{Reason: alasan})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if r.ActorID == nil || *r.ActorID != adm {
		t.Fatalf("pelaku refund tidak tercatat: %+v", r.ActorID)
	}
	if r.Reason != alasan {
		t.Fatalf("alasan %q", r.Reason)
	}

	daftar, err := l.bayar.Refunds(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca daftar refund: %v", err)
	}
	if len(daftar) != 1 {
		t.Fatalf("baris refund %d, seharusnya 1", len(daftar))
	}

	var jumlah int
	if err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_trail
		WHERE  entity = 'refunds' AND entity_id = $1 AND actor_id = $2`,
		r.ID, adm).Scan(&jumlah); err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("jejak audit refund %d, seharusnya 1", jumlah)
	}
}

func TestRefund_PembayaranTidakAda(t *testing.T) {
	l := siapkan(t)
	if _, err := l.bayar.Refund(context.Background(), uuid.New(), payment.RefundInput{
		Reason: "coba",
	}); !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
}
