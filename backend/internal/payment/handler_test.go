package payment_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/payment"
)

const secretUji = "kunci-penyedia-uji"

// antreanPalsu mencatat event yang diantre, supaya uji dapat memastikan
// pemrosesan benar benar diserahkan ke pekerja latar.
type antreanPalsu struct{ masuk []uuid.UUID }

func (a *antreanPalsu) QueuePaymentEvent(_ *http.Request, id uuid.UUID) error {
	a.masuk = append(a.masuk, id)
	return nil
}

// kirimWebhook mengirim satu permintaan webhook melalui handler sungguhan.
func (l *lingkungan) kirimWebhook(t *testing.T, h *payment.Handler, muatan map[string]any, tandaTanganPalsu bool) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(muatan)
	if err != nil {
		t.Fatalf("menyusun muatan: %v", err)
	}

	sig := tandaTangan(secretUji, string(body))
	if tandaTanganPalsu {
		sig = tandaTangan("kunci-salah", string(body))
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/payment", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Signature", sig)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Webhook(c); err != nil {
		t.Fatalf("handler webhook: %v", err)
	}

	var jawab map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &jawab)
	}
	return rec.Code, jawab
}

func (l *lingkungan) handler(q payment.EventQueuer) *payment.Handler {
	return payment.NewHandler(l.bayar,
		payment.HMACVerifier{Secret: []byte(secretUji), Header: "X-Signature"}, q)
}

// TestHandlerWebhook_TandaTanganSalahDitolakDanTercatat adalah uji keamanan
// utama domain ini. Tanpa verifikasi, siapa pun yang tahu alamat webhook dapat
// menyatakan pesanan sudah dibayar.
func TestHandlerWebhook_TandaTanganSalahDitolakDanTercatat(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}

	q := &antreanPalsu{}
	h := l.handler(q)

	sebelum := l.jumlahPenolakan(t)
	kode, jawab := l.kirimWebhook(t, h, map[string]any{
		"event_id":     "evt-" + uuid.NewString(),
		"provider_ref": p.ProviderRef,
		"status":       "settlement",
		"amount":       25150,
	}, true)

	if kode != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, seharusnya 401", kode)
	}
	if jawab["error"] == nil {
		t.Fatal("jawaban seharusnya memuat galat")
	}

	// Percobaannya tercatat, karena lonjakan tanda tangan salah adalah tanda
	// seseorang sedang mencoba memalsukan pembayaran.
	if n := l.jumlahPenolakan(t); n != sebelum+1 {
		t.Fatalf("catatan penolakan %d, seharusnya %d", n, sebelum+1)
	}
	// Dan tidak ada event yang tersimpan maupun diantre.
	if len(q.masuk) != 0 {
		t.Fatalf("event diantre %d padahal tanda tangannya salah", len(q.masuk))
	}
	if s := l.statusPesanan(t, o.ID); s != "WAITING_PAYMENT" {
		t.Fatalf("status pesanan %q berubah akibat webhook palsu", s)
	}
}

// TestHandlerWebhook_SahDiterimaDanDiantre menjaga SRS-PAY-002: handler
// menjawab cepat dan menyerahkan pemrosesan kepada pekerja latar.
func TestHandlerWebhook_SahDiterimaDanDiantre(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	q := &antreanPalsu{}
	h := l.handler(q)

	kode, jawab := l.kirimWebhook(t, h, map[string]any{
		"event_id":     "evt-" + uuid.NewString(),
		"provider_ref": p.ProviderRef,
		"status":       "settlement",
		"gross_amount": nominalRupiah(p.AmountCents),
	}, false)

	if kode != http.StatusOK {
		t.Fatalf("HTTP %d, seharusnya 200", kode)
	}
	if jawab["status"] != "diterima" {
		t.Fatalf("jawaban %v", jawab)
	}
	if len(q.masuk) != 1 {
		t.Fatalf("event diantre %d, seharusnya 1", len(q.masuk))
	}

	// Pesanan belum berpindah: itu tugas pekerja latar.
	if s := l.statusPesanan(t, o.ID); s != "WAITING_PAYMENT" {
		t.Fatalf("status pesanan %q, seharusnya belum berpindah", s)
	}

	// Setelah pekerja memprosesnya, pesanan lunas.
	if _, err := l.bayar.ApplyEvent(ctx, q.masuk[0]); err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if s := l.statusPesanan(t, o.ID); s != "PAID" {
		t.Fatalf("status pesanan %q, seharusnya PAID", s)
	}
}

// TestHandlerWebhook_DuplikatDijawabBerhasil menjaga janji SRS-PAY-002:
// 200 OK untuk event duplikat. Menjawabnya dengan galat membuat penyedia
// mengirim ulang terus menerus.
func TestHandlerWebhook_DuplikatDijawabBerhasil(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	q := &antreanPalsu{}
	h := l.handler(q)
	muatan := map[string]any{
		"event_id":     "evt-" + uuid.NewString(),
		"provider_ref": p.ProviderRef,
		"status":       "settlement",
		"gross_amount": nominalRupiah(p.AmountCents),
	}

	if kode, _ := l.kirimWebhook(t, h, muatan, false); kode != http.StatusOK {
		t.Fatalf("kiriman pertama HTTP %d", kode)
	}
	kode, jawab := l.kirimWebhook(t, h, muatan, false)
	if kode != http.StatusOK {
		t.Fatalf("kiriman kedua HTTP %d, seharusnya 200", kode)
	}
	if jawab["status"] != "duplikat" {
		t.Fatalf("jawaban %v, seharusnya menandai duplikat", jawab)
	}
	// Duplikat tidak diantre ulang: pemrosesannya sudah terantre sekali.
	if len(q.masuk) != 1 {
		t.Fatalf("event diantre %d, seharusnya 1", len(q.masuk))
	}
	_ = ctx
}

// TestHandlerWebhook_NominalRupiahDibacaSebagaiSen menjaga konversi yang mudah
// salah. Penyedia Indonesia mengirim rupiah bulat, sedangkan tagihan disimpan
// dalam sen; salah konversi membuat nominalnya tampak tidak cocok seratus kali.
func TestHandlerWebhook_NominalRupiahDibacaSebagaiSen(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	q := &antreanPalsu{}
	h := l.handler(q)

	kode, _ := l.kirimWebhook(t, h, map[string]any{
		"event_id":     "evt-" + uuid.NewString(),
		"provider_ref": p.ProviderRef,
		"status":       "settlement",
		// Dikirim sebagai rupiah bulat, sebagaimana penyedia Indonesia.
		"gross_amount": nominalRupiah(p.AmountCents),
	}, false)
	if kode != http.StatusOK {
		t.Fatalf("HTTP %d", kode)
	}

	hasil, err := l.bayar.ApplyEvent(ctx, q.masuk[0])
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if hasil.NeedsReview {
		t.Fatalf("nominal rupiah seharusnya terbaca cocok, bukan ditinjau: %+v", hasil)
	}
	if !hasil.Applied {
		t.Fatalf("event seharusnya diterapkan: %+v", hasil)
	}
}

// TestHandlerWebhook_MuatanTanpaPengenalDitolakDanTercatat menjaga agar
// perubahan bentuk muatan penyedia terlihat, bukan diterima separuh.
func TestHandlerWebhook_MuatanTanpaPengenalDitolakDanTercatat(t *testing.T) {
	l := siapkan(t)
	h := l.handler(&antreanPalsu{})

	sebelum := l.jumlahPenolakan(t)
	kode, _ := l.kirimWebhook(t, h, map[string]any{
		"status": "settlement", "amount": 25000,
	}, false)
	if kode != http.StatusUnprocessableEntity {
		t.Fatalf("HTTP %d, seharusnya 422", kode)
	}
	if n := l.jumlahPenolakan(t); n != sebelum+1 {
		t.Fatalf("catatan penolakan %d, seharusnya bertambah satu", n)
	}
}

// TestHandlerWebhook_TanpaVerifikatorMenolakSemua menjaga sikap gagal tertutup.
// Webhook tanpa verifikasi berarti siapa pun yang tahu alamatnya dapat
// menyatakan pesanan sudah dibayar.
func TestHandlerWebhook_TanpaVerifikatorMenolakSemua(t *testing.T) {
	l := siapkan(t)
	h := payment.NewHandler(l.bayar, nil, &antreanPalsu{})

	kode, _ := l.kirimWebhook(t, h, map[string]any{
		"event_id": "evt-" + uuid.NewString(), "status": "settlement",
	}, false)
	if kode != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, seharusnya 401", kode)
	}
}
