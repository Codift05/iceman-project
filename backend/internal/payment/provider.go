package payment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"time"
)

// ChargeRequest adalah permintaan pembuatan tagihan ke penyedia.
type ChargeRequest struct {
	OrderNo     string
	AmountCents int64
	// ExpiresIn adalah masa berlaku yang diminta. Penyedia dapat memberi masa
	// berlaku yang berbeda, dan yang dipakai adalah jawaban penyedia.
	ExpiresIn time.Duration
}

// ChargeResult adalah jawaban penyedia atas pembuatan tagihan.
type ChargeResult struct {
	// Ref adalah referensi penyedia, wajib unik.
	Ref string
	// QRPayload adalah muatan yang digambar menjadi kode QR oleh klien.
	QRPayload string
	ExpiresAt *time.Time
}

// RefundRequest adalah permintaan refund ke penyedia.
type RefundRequest struct {
	PaymentRef  string
	AmountCents int64
	Reason      string
}

// Provider adalah antarmuka ke penyedia pembayaran.
//
// Dibuat sebagai antarmuka karena penyedianya belum diputuskan. Seluruh aturan
// pembayaran pada paket ini tidak bergantung padanya; yang menunggu keputusan
// hanya satu implementasi.
type Provider interface {
	// Name mengembalikan nama penyedia, disimpan pada baris pembayaran agar
	// data lama tetap dapat dijelaskan setelah berpindah penyedia.
	Name() string

	// Charge membuat tagihan. Kegagalan di sini tidak boleh membatalkan
	// pesanan; pelanggan dapat mencoba lagi (SRS-PAY-001).
	Charge(ctx context.Context, in ChargeRequest) (*ChargeResult, error)

	// SupportsRefund menjawab apakah refund otomatis didukung. Bila tidak,
	// refund dicatat manual: dana dikembalikan di luar sistem lalu dicatat
	// (SRS-PAY-004).
	SupportsRefund() bool

	// Refund mengembalikan dana. Hanya dipanggil bila SupportsRefund benar.
	Refund(ctx context.Context, in RefundRequest) (ref string, err error)
}

// ManualProvider adalah penyedia bawaan selama penyedia sungguhan belum
// dipilih.
//
// Tagihan dibuat sebagai catatan tanpa kode QR, dan pembayarannya dikonfirmasi
// admin. Ini bukan penyangga kosong: sebagian pelanggan Iceman membayar lewat
// transfer dan tunai, sehingga jalur ini tetap dipakai setelah QRIS aktif.
type ManualProvider struct{}

// Name mengembalikan nama penyedia.
func (ManualProvider) Name() string { return "MANUAL" }

// Charge mencatat tagihan tanpa memanggil layanan luar.
func (ManualProvider) Charge(_ context.Context, in ChargeRequest) (*ChargeResult, error) {
	var berakhir *time.Time
	if in.ExpiresIn > 0 {
		t := time.Now().Add(in.ExpiresIn)
		berakhir = &t
	}
	// Referensi memuat nomor pesanan agar dapat ditelusuri tanpa membuka
	// basis data, dan sebuah pembeda acak agar tetap unik.
	//
	// Pembeda itu perlu karena satu pesanan dapat punya beberapa tagihan
	// berurutan: tagihan yang kedaluwarsa diganti yang baru. Tanpa pembeda,
	// tagihan pengganti bertabrakan dengan kekangan keunikan referensi
	// penyedia, dan pelanggan tidak dapat mencoba lagi.
	pembeda, err := pembedaAcak()
	if err != nil {
		return nil, fmt.Errorf("membangkitkan pembeda referensi: %w", err)
	}
	return &ChargeResult{
		Ref:       "MANUAL-" + in.OrderNo + "-" + pembeda,
		ExpiresAt: berakhir,
	}, nil
}

// pembedaAcak membangkitkan pembeda pendek untuk referensi pembayaran.
//
// Dibangkitkan acak, bukan dari penghitung atau cap waktu, agar dua tagihan
// yang dibuat pada saat yang sama tetap berbeda tanpa perlu penguncian.
func pembedaAcak() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SupportsRefund menjawab tidak: refund manual dicatat, bukan diotomatiskan.
func (ManualProvider) SupportsRefund() bool { return false }

// Refund tidak dipakai pada penyedia manual.
func (ManualProvider) Refund(context.Context, RefundRequest) (string, error) {
	return "", fmt.Errorf("%w: penyedia manual tidak mendukung refund otomatis", ErrNotRefundable)
}

// Verifier memeriksa keaslian permintaan webhook.
//
// Dipisahkan dari Provider karena verifikasi berjalan sebelum muatan dipercaya,
// yaitu sebelum kita tahu pembayaran mana yang dimaksud, sehingga ia tidak
// boleh bergantung pada apa pun dari muatan itu.
type Verifier interface {
	// Verify memeriksa tanda tangan atas badan permintaan. Header diberikan
	// seluruhnya karena setiap penyedia menaruh tanda tangannya di tempat yang
	// berbeda.
	Verify(body []byte, header map[string]string) error
}

// HMACVerifier memeriksa tanda tangan HMAC heksadesimal atas badan permintaan.
//
// Bentuk ini dipakai banyak penyedia pembayaran, namun rinciannya berbeda:
// nama header, algoritma, dan ada tidaknya awalan. Karena itu ketiganya dapat
// diatur, dan nilai yang benar ditetapkan setelah penyedia dipilih.
type HMACVerifier struct {
	// Secret adalah kunci bersama dari penyedia.
	Secret []byte
	// Header adalah nama header yang memuat tanda tangan.
	Header string
	// Algo adalah "sha256" atau "sha512". Kosong berarti sha256.
	Algo string
	// Prefix adalah awalan pada nilai header, misalnya "sha256=". Kosong
	// berarti tanda tangan ditulis apa adanya.
	Prefix string
}

// Verify memeriksa tanda tangan atas badan permintaan.
//
// Perbandingannya memakai hmac.Equal, bukan perbandingan string biasa, agar
// lamanya perbandingan tidak bergantung pada seberapa banyak karakter awal
// yang sudah benar. Perbandingan biasa membocorkan tanda tangan yang benar
// sedikit demi sedikit kepada penyerang yang mengukur waktu jawaban.
func (v HMACVerifier) Verify(body []byte, header map[string]string) error {
	if len(v.Secret) == 0 {
		return fmt.Errorf("%w: kunci verifikasi belum disetel", ErrSignatureInvalid)
	}
	nama := v.Header
	if nama == "" {
		return fmt.Errorf("%w: nama header tanda tangan belum disetel", ErrSignatureInvalid)
	}

	// Nama header dibandingkan tanpa membedakan huruf, sesuai HTTP.
	var diberikan string
	for k, val := range header {
		if strings.EqualFold(k, nama) {
			diberikan = val
			break
		}
	}
	if diberikan == "" {
		return fmt.Errorf("%w: header %s tidak ada", ErrSignatureInvalid, nama)
	}
	diberikan = strings.TrimPrefix(strings.TrimSpace(diberikan), v.Prefix)

	var h func() hash.Hash
	switch strings.ToLower(v.Algo) {
	case "", "sha256":
		h = sha256.New
	case "sha512":
		h = sha512.New
	default:
		return fmt.Errorf("%w: algoritma %q tidak dikenali", ErrSignatureInvalid, v.Algo)
	}

	mac := hmac.New(h, v.Secret)
	mac.Write(body)
	diharapkan := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(strings.ToLower(diberikan)), []byte(diharapkan)) {
		return ErrSignatureInvalid
	}
	return nil
}

// AllowAllVerifier menerima setiap permintaan tanpa memeriksa apa pun.
//
// Hanya untuk pengembangan lokal, dan sengaja diberi nama yang tidak mungkin
// dipakai tanpa sadar. Memakainya di produksi berarti siapa pun yang tahu
// alamat webhook dapat menyatakan pesanan sudah dibayar.
type AllowAllVerifier struct{}

// Verify menerima apa saja.
func (AllowAllVerifier) Verify([]byte, map[string]string) error { return nil }
