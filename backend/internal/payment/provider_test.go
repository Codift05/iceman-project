package payment_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/iceman/backend/internal/payment"
)

func tandaTangan(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestVerifikasi_TandaTanganSahDanTidakSah adalah uji keamanan. Tanpa
// verifikasi, siapa pun yang tahu alamat webhook dapat menyatakan pesanan
// sudah dibayar (SRS-PAY-002).
func TestVerifikasi_TandaTanganSahDanTidakSah(t *testing.T) {
	const secret = "kunci-rahasia-penyedia"
	const body = `{"event_id":"evt-1","status":"settlement","amount":25000}`

	v := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Signature"}

	if err := v.Verify([]byte(body), map[string]string{
		"X-Signature": tandaTangan(secret, body),
	}); err != nil {
		t.Fatalf("tanda tangan sah seharusnya diterima: %v", err)
	}

	kasus := []struct {
		nama   string
		header map[string]string
		body   string
	}{
		{"tanda tangan salah", map[string]string{"X-Signature": tandaTangan("kunci-lain", body)}, body},
		{"tanpa header", map[string]string{}, body},
		{"header kosong", map[string]string{"X-Signature": ""}, body},
		{"tanda tangan bukan heksadesimal", map[string]string{"X-Signature": "bukan-hex"}, body},
		{"badan diubah sesudah ditandatangani",
			map[string]string{"X-Signature": tandaTangan(secret, body)},
			`{"event_id":"evt-1","status":"settlement","amount":2500000}`},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if err := v.Verify([]byte(k.body), k.header); !errors.Is(err, payment.ErrSignatureInvalid) {
				t.Fatalf("seharusnya ditolak, dapat %v", err)
			}
		})
	}
}

// TestVerifikasi_BadanDiubahSatuAngkaDitolak memisahkan kasus yang paling
// berbahaya: penyerang mengambil webhook yang sah lalu menaikkan nominalnya.
func TestVerifikasi_BadanDiubahSatuAngkaDitolak(t *testing.T) {
	const secret = "kunci"
	asli := `{"amount":25000}`
	palsu := `{"amount":25001}`

	v := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Signature"}
	sah := tandaTangan(secret, asli)

	if err := v.Verify([]byte(asli), map[string]string{"X-Signature": sah}); err != nil {
		t.Fatalf("badan asli seharusnya diterima: %v", err)
	}
	if err := v.Verify([]byte(palsu), map[string]string{"X-Signature": sah}); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("badan yang diubah seharusnya ditolak, dapat %v", err)
	}
}

// TestVerifikasi_NamaHeaderTidakMembedakanHuruf menjaga kesesuaian dengan
// HTTP, yang menyatakan nama header tidak membedakan huruf besar kecil.
func TestVerifikasi_NamaHeaderTidakMembedakanHuruf(t *testing.T) {
	const secret = "kunci"
	const body = `{"a":1}`
	v := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Signature"}
	sah := tandaTangan(secret, body)

	for _, nama := range []string{"X-Signature", "x-signature", "X-SIGNATURE", "x-SiGnAtUrE"} {
		if err := v.Verify([]byte(body), map[string]string{nama: sah}); err != nil {
			t.Fatalf("header %q seharusnya dikenali: %v", nama, err)
		}
	}
}

// TestVerifikasi_AwalanDanHurufBesarPadaNilai menjaga dua kebiasaan penyedia:
// menambahkan awalan algoritma, dan menulis heksadesimal dengan huruf besar.
func TestVerifikasi_AwalanDanHurufBesarPadaNilai(t *testing.T) {
	const secret = "kunci"
	const body = `{"a":1}`
	sah := tandaTangan(secret, body)

	v := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Sig", Prefix: "sha256="}
	if err := v.Verify([]byte(body), map[string]string{"X-Sig": "sha256=" + sah}); err != nil {
		t.Fatalf("awalan seharusnya dilepas: %v", err)
	}

	tanpaAwalan := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Sig"}
	if err := tanpaAwalan.Verify([]byte(body), map[string]string{
		"X-Sig": strings.ToUpper(sah),
	}); err != nil {
		t.Fatalf("heksadesimal huruf besar seharusnya diterima: %v", err)
	}
}

// TestVerifikasi_Sha512 menjaga pilihan algoritma yang dapat disetel.
func TestVerifikasi_Sha512(t *testing.T) {
	const secret = "kunci"
	const body = `{"a":1}`
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(body))
	sah := hex.EncodeToString(mac.Sum(nil))

	v := payment.HMACVerifier{Secret: []byte(secret), Header: "X-Sig", Algo: "sha512"}
	if err := v.Verify([]byte(body), map[string]string{"X-Sig": sah}); err != nil {
		t.Fatalf("sha512 seharusnya diterima: %v", err)
	}
	// Tanda tangan sha256 tidak boleh lolos pada verifikator sha512.
	if err := v.Verify([]byte(body), map[string]string{
		"X-Sig": tandaTangan(secret, body),
	}); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("tanda tangan algoritma lain seharusnya ditolak, dapat %v", err)
	}
}

// TestVerifikasi_PenyetelanBelumLengkapDitolak menjaga agar verifikator yang
// belum disetel menolak, bukan menerima. Menerima berarti webhook terbuka bagi
// siapa pun karena ada yang lupa mengisi konfigurasi.
func TestVerifikasi_PenyetelanBelumLengkapDitolak(t *testing.T) {
	const body = `{"a":1}`

	tanpaKunci := payment.HMACVerifier{Header: "X-Sig"}
	if err := tanpaKunci.Verify([]byte(body), map[string]string{"X-Sig": "apa saja"}); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("tanpa kunci seharusnya ditolak, dapat %v", err)
	}

	tanpaHeader := payment.HMACVerifier{Secret: []byte("kunci")}
	if err := tanpaHeader.Verify([]byte(body), map[string]string{"X-Sig": "apa saja"}); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("tanpa nama header seharusnya ditolak, dapat %v", err)
	}

	algoAsing := payment.HMACVerifier{Secret: []byte("kunci"), Header: "X-Sig", Algo: "md5"}
	if err := algoAsing.Verify([]byte(body), map[string]string{"X-Sig": "apa saja"}); !errors.Is(err, payment.ErrSignatureInvalid) {
		t.Fatalf("algoritma tidak dikenal seharusnya ditolak, dapat %v", err)
	}
}

// TestPenyediaManual_MembuatTagihanTanpaLayananLuar menjaga jalur yang dipakai
// selama penyedia sungguhan belum dipilih, dan yang tetap dipakai sesudahnya
// untuk pelanggan yang membayar lewat transfer atau tunai.
func TestPenyediaManual_MembuatTagihanTanpaLayananLuar(t *testing.T) {
	var p payment.Provider = payment.ManualProvider{}
	if p.Name() != "MANUAL" {
		t.Fatalf("nama penyedia %q", p.Name())
	}
	if p.SupportsRefund() {
		t.Fatal("penyedia manual seharusnya tidak mendukung refund otomatis")
	}

	hasil, err := p.Charge(context.Background(), payment.ChargeRequest{
		OrderNo: "ICE-261005-00001", AmountCents: 25000,
	})
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}
	// Referensi memuat nomor pesanan agar dapat ditelusuri, dan sebuah
	// pembeda agar tagihan pengganti tidak bertabrakan dengan yang lama.
	if !strings.HasPrefix(hasil.Ref, "MANUAL-ICE-261005-00001-") {
		t.Fatalf("referensi %q, seharusnya dapat ditelusuri ke pesanannya", hasil.Ref)
	}

	lagi, err := p.Charge(context.Background(), payment.ChargeRequest{
		OrderNo: "ICE-261005-00001", AmountCents: 25000,
	})
	if err != nil {
		t.Fatalf("membuat tagihan kedua: %v", err)
	}
	if lagi.Ref == hasil.Ref {
		t.Fatal("dua tagihan untuk pesanan yang sama seharusnya beda referensi, " +
			"karena referensi penyedia bersifat unik")
	}
	// Tanpa masa berlaku yang diminta, tagihannya tidak kedaluwarsa.
	if hasil.ExpiresAt != nil {
		t.Fatal("tanpa masa berlaku yang diminta seharusnya tanpa batas waktu")
	}

	if _, err := p.Refund(context.Background(), payment.RefundRequest{}); !errors.Is(err, payment.ErrNotRefundable) {
		t.Fatalf("refund otomatis seharusnya ditolak, dapat %v", err)
	}
}
