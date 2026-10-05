package payment_test

import (
	"errors"
	"testing"
	"time"

	"github.com/iceman/backend/internal/payment"
)

// TestMatriksBayar_SesuaiTabelSRS mengunci daftar perpindahan status
// pembayaran. Daftarnya ditulis ulang di sini dari tabel SRS Bab 5.2, bukan
// dibaca dari kode yang diujinya.
func TestMatriksBayar_SesuaiTabelSRS(t *testing.T) {
	mau := map[string][]string{
		payment.StatusPending: {
			payment.StatusSuccess, payment.StatusExpired,
			payment.StatusFailed, payment.StatusCancelled,
		},
		payment.StatusSuccess:   {payment.StatusRefunded},
		payment.StatusExpired:   {payment.StatusPending},
		payment.StatusFailed:    {payment.StatusPending},
		payment.StatusRefunded:  nil,
		payment.StatusCancelled: nil,
	}
	for dari, tujuan := range mau {
		got := payment.AllowedFrom(dari)
		if len(got) != len(tujuan) {
			t.Fatalf("dari %s ada %d tujuan (%v), seharusnya %d (%v)",
				dari, len(got), got, len(tujuan), tujuan)
		}
		for _, ke := range tujuan {
			if _, ok := payment.Allowed(dari, ke); !ok {
				t.Fatalf("%s ke %s seharusnya diizinkan", dari, ke)
			}
		}
	}
}

// TestPemetaanStatus_DiujiBukanHanyaDidokumentasikan menjaga SRS-PAY-003, yang
// mewajibkan pemetaan status penyedia didokumentasikan dan diuji.
//
// Yang diperiksa dua hal: status yang disebut SRS memetakan sebagaimana
// mestinya, dan seluruh entri pada daftar pemetaan menunjuk status internal
// yang sah. Yang kedua menangkap salah tulis pada daftarnya.
func TestPemetaanStatus_DiujiBukanHanyaDidokumentasikan(t *testing.T) {
	kasus := map[string]string{
		"settlement": payment.StatusSuccess,
		"capture":    payment.StatusSuccess,
		"paid":       payment.StatusSuccess,
		"pending":    payment.StatusPending,
		"expire":     payment.StatusExpired,
		"expired":    payment.StatusExpired,
		"deny":       payment.StatusFailed,
		"failed":     payment.StatusFailed,
		"cancel":     payment.StatusCancelled,
		"refund":     payment.StatusRefunded,
	}
	for penyedia, mau := range kasus {
		got, ok := payment.MapStatus(penyedia)
		if !ok {
			t.Fatalf("status penyedia %q seharusnya dikenal", penyedia)
		}
		if got != mau {
			t.Fatalf("status %q memetakan ke %q, seharusnya %q", penyedia, got, mau)
		}
	}

	// Seluruh entri pada daftar pemetaan wajib menunjuk status internal yang
	// sah. Tanpa ini, satu salah tulis pada daftarnya baru terlihat saat
	// penyedia mengirim status itu, yaitu saat uang sudah berpindah.
	for _, penyedia := range payment.KnownProviderStatuses() {
		got, ok := payment.MapStatus(penyedia)
		if !ok {
			t.Fatalf("status %q terdaftar namun tidak terpetakan", penyedia)
		}
		if !payment.ValidStatus(got) {
			t.Fatalf("status %q memetakan ke %q yang bukan status internal", penyedia, got)
		}
	}
}

// TestPemetaanStatus_TidakMembedakanHurufBesarKecil menjaga agar penyedia yang
// tidak konsisten soal huruf tidak membuat statusnya tampak tidak dikenal.
func TestPemetaanStatus_TidakMembedakanHurufBesarKecil(t *testing.T) {
	for _, s := range []string{"SETTLEMENT", "Settlement", "  settlement  ", "sEtTlEmEnT"} {
		got, ok := payment.MapStatus(s)
		if !ok || got != payment.StatusSuccess {
			t.Fatalf("status %q seharusnya memetakan ke SUCCESS, dapat %q ok=%v", s, got, ok)
		}
	}
}

// TestPemetaanStatus_TidakDikenalTidakDitebak menjaga SRS-PAY-003: status yang
// tidak dikenal dicatat dan ditandai untuk tinjauan, tidak diabaikan dan tidak
// ditebak. Menebaknya berarti pesanan dapat dinyatakan lunas karena status
// yang belum dipahami.
func TestPemetaanStatus_TidakDikenalTidakDitebak(t *testing.T) {
	for _, s := range []string{"", "   ", "chargeback", "on_hold", "status_baru_penyedia"} {
		if got, ok := payment.MapStatus(s); ok {
			t.Fatalf("status %q seharusnya tidak dikenal, dapat %q", s, got)
		}
	}
}

// TestTransisiBayar_MenujuSuksesWajibNominalCocok menjaga janji yang paling
// berdampak pada uang: pesanan hanya dinyatakan lunas bila yang dibayar memang
// sebesar tagihannya.
func TestTransisiBayar_MenujuSuksesWajibNominalCocok(t *testing.T) {
	const tagihan = 7515000

	cocok := int64(tagihan)
	if err := payment.CheckTransition(payment.StatusPending, payment.StatusSuccess,
		tagihan, &cocok); err != nil {
		t.Fatalf("nominal cocok seharusnya diterima: %v", err)
	}

	kurang := int64(tagihan - 1)
	if err := payment.CheckTransition(payment.StatusPending, payment.StatusSuccess,
		tagihan, &kurang); !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("nominal kurang seharusnya ditolak, dapat %v", err)
	}

	lebih := int64(tagihan + 1)
	if err := payment.CheckTransition(payment.StatusPending, payment.StatusSuccess,
		tagihan, &lebih); !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("nominal lebih seharusnya ditolak, dapat %v", err)
	}

	// Nominal yang tidak disebutkan penyedia tidak dianggap cocok.
	if err := payment.CheckTransition(payment.StatusPending, payment.StatusSuccess,
		tagihan, nil); !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("nominal yang tidak disebutkan seharusnya ditolak, dapat %v", err)
	}
}

// TestTransisiBayar_SelainSuksesTidakMemeriksaNominal menjaga agar tagihan
// yang kedaluwarsa atau gagal tetap dapat dicatat walau penyedia tidak
// menyebutkan nominalnya.
func TestTransisiBayar_SelainSuksesTidakMemeriksaNominal(t *testing.T) {
	for _, ke := range []string{payment.StatusExpired, payment.StatusFailed, payment.StatusCancelled} {
		if err := payment.CheckTransition(payment.StatusPending, ke, 25000, nil); err != nil {
			t.Fatalf("perpindahan ke %s seharusnya tidak memeriksa nominal: %v", ke, err)
		}
	}
}

func TestTransisiBayar_YangTidakTerdaftarDitolak(t *testing.T) {
	kasus := [][2]string{
		{payment.StatusSuccess, payment.StatusPending},
		{payment.StatusRefunded, payment.StatusSuccess},
		{payment.StatusCancelled, payment.StatusSuccess},
		{payment.StatusExpired, payment.StatusSuccess},
	}
	for _, k := range kasus {
		if err := payment.CheckTransition(k[0], k[1], 1000, nil); !errors.Is(err, payment.ErrInvalidStatus) {
			t.Fatalf("%s ke %s seharusnya ditolak, dapat %v", k[0], k[1], err)
		}
	}
}

// TestRefundable_HanyaAtasPembayaranBerhasil menjaga SRS-PAY-004.
func TestRefundable_HanyaAtasPembayaranBerhasil(t *testing.T) {
	for _, s := range []string{
		payment.StatusPending, payment.StatusExpired,
		payment.StatusFailed, payment.StatusCancelled,
	} {
		p := &payment.Payment{Status: s, AmountCents: 100000}
		if got := p.Refundable(); got != 0 {
			t.Fatalf("status %s seharusnya tidak dapat direfund, sisa %d", s, got)
		}
	}

	p := &payment.Payment{Status: payment.StatusSuccess, AmountCents: 100000}
	if got := p.Refundable(); got != 100000 {
		t.Fatalf("sisa refund %d, seharusnya 100000", got)
	}

	// Refund sebagian mengurangi sisanya.
	p.RefundedCents = 30000
	if got := p.Refundable(); got != 70000 {
		t.Fatalf("sisa refund %d, seharusnya 70000", got)
	}

	// Pembayaran yang sudah direfund penuh masih berstatus REFUNDED dan
	// sisanya nol, bukan negatif.
	p.Status = payment.StatusRefunded
	p.RefundedCents = 100000
	if got := p.Refundable(); got != 0 {
		t.Fatalf("sisa refund %d, seharusnya 0", got)
	}
}

// TestExpired_TagihanKedaluwarsaTidakDapatMenandaiLunas menjaga SRS-PAY-001.
func TestExpired_TagihanKedaluwarsaTidakDapatMenandaiLunas(t *testing.T) {
	now := waktuTetap()

	tanpaBatas := &payment.Payment{}
	if tanpaBatas.Expired(now) {
		t.Fatal("tagihan tanpa masa berlaku seharusnya tidak pernah kedaluwarsa")
	}

	nanti := now.Add(time.Minute)
	belum := &payment.Payment{ExpiresAt: &nanti}
	if belum.Expired(now) {
		t.Fatal("tagihan yang masih berlaku ditandai kedaluwarsa")
	}

	// Tepat pada saat batasnya, tagihan sudah tidak berlaku. Batas yang
	// inklusif membuat ada satu saat di mana dua tafsiran mungkin, dan untuk
	// uang lebih baik yang ketat.
	tepat := &payment.Payment{ExpiresAt: &now}
	if !tepat.Expired(now) {
		t.Fatal("tagihan tepat pada batasnya seharusnya sudah kedaluwarsa")
	}

	lalu := now.Add(-time.Minute)
	sudah := &payment.Payment{ExpiresAt: &lalu}
	if !sudah.Expired(now) {
		t.Fatal("tagihan yang sudah lewat seharusnya kedaluwarsa")
	}
}
