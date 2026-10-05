package payment_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/payment"
	"github.com/iceman/backend/internal/scheduling"
	"github.com/iceman/backend/internal/store"
)

func dsn() string { return os.Getenv("ICEMAN_TEST_DSN") }

type lingkungan struct {
	pool      *pgxpool.Pool
	produk    *catalog.Products
	pelanggan *customer.Customers
	keranjang *cart.Carts
	pesanan   *order.Orders
	bayar     *payment.Service
}

func siapkan(t *testing.T) *lingkungan {
	t.Helper()
	ctx := context.Background()
	if dsn() == "" {
		t.Skip("ICEMAN_TEST_DSN belum diisi, jalankan lewat make test")
	}
	if err := store.Migrate(ctx, dsn()); err != nil {
		t.Skipf("basis data uji tidak tersedia, lewati: %v", err)
	}
	pool, err := store.Connect(ctx, dsn())
	if err != nil {
		t.Skipf("basis data uji tidak tersedia, lewati: %v", err)
	}
	t.Cleanup(pool.Close)

	produk := catalog.NewProducts(pool)
	pelanggan := customer.NewCustomers(pool)
	keranjang := cart.NewCarts(pool, produk)

	return &lingkungan{
		pool: pool, produk: produk, pelanggan: pelanggan, keranjang: keranjang,
		pesanan: order.NewOrders(order.Deps{
			Pool: pool, Carts: keranjang, Customers: pelanggan,
			Slots: scheduling.NewSlots(pool),
		}),
		bayar: payment.NewService(payment.Deps{Pool: pool}),
	}
}

// jendelaBerikut memberi jam mulai slot yang berbeda tiap pemanggilan, karena
// DB-09 melarang dua slot pada wilayah, tanggal, dan jam mulai yang sama.
var jendelaBerikut int

func jamSlot() string {
	jam := []string{"00:00", "01:00", "02:00", "03:00", "04:00", "05:00", "06:00",
		"07:00", "08:00", "09:00", "10:00", "11:00", "12:00", "13:00", "14:00",
		"15:00", "16:00", "17:00", "18:00", "19:00", "20:00", "21:00", "22:00"}
	j := jam[jendelaBerikut%len(jam)]
	jendelaBerikut++
	return j
}

// buatPesananRitel membuat pesanan berstatus WAITING_PAYMENT, yaitu pesanan
// yang memang perlu ditagih.
func (l *lingkungan) buatPesananRitel(t *testing.T) *order.Order {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var depotID, areaID, slotID uuid.UUID
	err := l.pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Bayar', 1.4748, 124.8421, 8) RETURNING id`,
		"UJI-"+suffix).Scan(&depotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = l.pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 15000) RETURNING id`, depotID, "Area "+suffix).Scan(&areaID)
	if err != nil {
		t.Fatalf("menyiapkan wilayah: %v", err)
	}
	err = l.pool.QueryRow(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end, capacity, cutoff_at)
		VALUES ($1, current_date + 1, $2, '23:00', 20, now() + interval '20 hours')
		RETURNING id`, areaID, jamSlot()).Scan(&slotID)
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}

	cust, err := l.pelanggan.Create(ctx, customer.Input{
		Phone: "0811" + suffix, Name: "Pembeli Ritel", Type: customer.TypeRetail,
	})
	if err != nil {
		t.Fatalf("membuat pelanggan: %v", err)
	}
	alamat, err := l.pelanggan.AddAddress(ctx, cust.ID, customer.AddressInput{
		ServiceAreaID: areaID, RecipientName: "Pak Budi", Phone: "0899",
		AddressLine: "Jl. Uji 1", Latitude: 1.475, Longitude: 124.843,
	})
	if err != nil {
		t.Fatalf("menambah alamat: %v", err)
	}
	prod, err := l.produk.Create(ctx, catalog.ProductInput{
		SKU: "UJI-" + suffix, Name: "Es Balok", BasePriceCents: 2500000, MinOrderQty: 1,
	})
	if err != nil {
		t.Fatalf("membuat produk: %v", err)
	}
	if _, err := l.keranjang.Add(ctx, cust.ID, prod.ID, 1); err != nil {
		t.Fatalf("mengisi keranjang: %v", err)
	}

	o, err := l.pesanan.Checkout(ctx, order.CheckoutInput{
		CustomerID: cust.ID, AddressID: alamat.ID, SlotID: slotID,
	})
	if err != nil {
		t.Fatalf("membuat pesanan: %v", err)
	}
	if o.Status != order.StatusWaitingPayment {
		t.Fatalf("pesanan uji berstatus %s, seharusnya WAITING_PAYMENT", o.Status)
	}
	return o
}

func (l *lingkungan) statusPesanan(t *testing.T, orderID uuid.UUID) string {
	t.Helper()
	var s string
	if err := l.pool.QueryRow(context.Background(),
		`SELECT status::text FROM orders WHERE id = $1`, orderID).Scan(&s); err != nil {
		t.Fatalf("membaca status pesanan: %v", err)
	}
	return s
}

// webhook mengirim satu event sukses untuk sebuah tagihan.
func (l *lingkungan) webhook(t *testing.T, p *payment.Payment, status string, nominal *int64) uuid.UUID {
	t.Helper()
	muatan, _ := json.Marshal(map[string]any{
		"ref": p.ProviderRef, "status": status,
	})
	id, dup, err := l.bayar.AcceptWebhook(context.Background(), payment.WebhookInput{
		EventID:        "evt-" + uuid.NewString(),
		ProviderRef:    p.ProviderRef,
		ProviderStatus: status,
		AmountCents:    nominal,
		Payload:        muatan,
	})
	if err != nil {
		t.Fatalf("menerima webhook: %v", err)
	}
	if dup {
		t.Fatal("event baru seharusnya bukan duplikat")
	}
	return id
}

func TestTagihan_DibuatSesuaiTotalPesanan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)

	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}
	// Nominal QRIS sama persis dengan total tagihan pesanan (SRS-PAY-001).
	if p.AmountCents != o.TotalCents {
		t.Fatalf("nominal tagihan %d, seharusnya %d", p.AmountCents, o.TotalCents)
	}
	if p.Status != payment.StatusPending {
		t.Fatalf("status tagihan %q, seharusnya PENDING", p.Status)
	}
	if p.ProviderRef == "" {
		t.Fatal("referensi penyedia kosong")
	}
	if p.ExpiresAt == nil {
		t.Fatal("masa berlaku tagihan seharusnya ditetapkan")
	}
}

// TestTagihan_PermintaanKeduaMengembalikanYangSama menjaga agar pelanggan yang
// membuka kembali halaman pembayaran melihat kode yang sama, bukan tagihan
// baru. Dua tagihan aktif membuat ia dapat membayar dua kali.
func TestTagihan_PermintaanKeduaMengembalikanYangSama(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)

	pertama, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("tagihan pertama: %v", err)
	}
	kedua, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("tagihan kedua: %v", err)
	}
	if pertama.ID != kedua.ID {
		t.Fatalf("dua tagihan berbeda dibuat: %s dan %s", pertama.ID, kedua.ID)
	}

	var jumlah int
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM payments WHERE order_id = $1`, o.ID).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung pembayaran: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("baris pembayaran %d, seharusnya 1", jumlah)
	}
}

// TestTagihan_PesananSudahLunasDitolak menjaga SRS-PAY-001.
func TestTagihan_PesananSudahLunasDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)

	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}
	nominal := p.AmountCents
	ev := l.webhook(t, p, "settlement", &nominal)
	if _, err := l.bayar.ApplyEvent(ctx, ev); err != nil {
		t.Fatalf("memproses event: %v", err)
	}

	if _, err := l.bayar.Charge(ctx, o.ID); !errors.Is(err, payment.ErrAlreadyPaid) {
		t.Fatalf("galat %v, seharusnya ErrAlreadyPaid", err)
	}
}

// TestTagihan_PesananTidakDapatDibayarDitolak menjaga agar pesanan kontrak
// yang langsung diproses tidak ditagih di muka.
func TestTagihan_PesananTidakDapatDibayarDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)

	if _, err := l.pool.Exec(ctx,
		`UPDATE orders SET status = 'PROCESSING' WHERE id = $1`, o.ID); err != nil {
		t.Fatalf("mengubah status pesanan: %v", err)
	}
	if _, err := l.bayar.Charge(ctx, o.ID); !errors.Is(err, payment.ErrOrderNotPayable) {
		t.Fatalf("galat %v, seharusnya ErrOrderNotPayable", err)
	}
}

// TestWebhook_EventSamaDuaKaliSatuCatatan menjaga DB-02 dan janji SRS-PAY-002:
// mengirim event yang sama dua kali menghasilkan satu catatan pembayaran.
func TestWebhook_EventSamaDuaKaliSatuCatatan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}

	nominal := p.AmountCents
	in := payment.WebhookInput{
		EventID: "evt-" + uuid.NewString(), ProviderRef: p.ProviderRef,
		ProviderStatus: "settlement", AmountCents: &nominal,
		Payload: []byte(`{"status":"settlement"}`),
	}

	id1, dup1, err := l.bayar.AcceptWebhook(ctx, in)
	if err != nil {
		t.Fatalf("webhook pertama: %v", err)
	}
	if dup1 {
		t.Fatal("event pertama ditandai duplikat")
	}

	id2, dup2, err := l.bayar.AcceptWebhook(ctx, in)
	if err != nil {
		t.Fatalf("webhook kedua: %v", err)
	}
	if !dup2 {
		t.Fatal("event kedua seharusnya ditandai duplikat")
	}
	if id1 != id2 {
		t.Fatal("event duplikat seharusnya menunjuk baris yang sama")
	}

	var jumlah int
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_events WHERE event_id = $1`, in.EventID).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung event: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("baris event %d, seharusnya 1", jumlah)
	}
}

// TestWebhook_PesananBerpindahKePaidSetelahPekerjaSelesai menjaga janji
// SRS-PAY-002: pesanan berpindah ke PAID hanya setelah pekerja latar selesai
// memproses, bukan saat webhook diterima.
func TestWebhook_PesananBerpindahKePaidSetelahPekerjaSelesai(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("membuat tagihan: %v", err)
	}

	nominal := p.AmountCents
	ev := l.webhook(t, p, "settlement", &nominal)

	// Webhook sudah diterima, namun pesanan belum berpindah.
	if s := l.statusPesanan(t, o.ID); s != order.StatusWaitingPayment {
		t.Fatalf("status pesanan %q, seharusnya belum berpindah sebelum diproses", s)
	}

	hasil, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if !hasil.Applied || hasil.PaymentStatus != payment.StatusSuccess {
		t.Fatalf("hasil pemrosesan salah: %+v", hasil)
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusPaid {
		t.Fatalf("status pesanan %q, seharusnya PAID", s)
	}

	// Nomor invoice dibuat saat pembayaran diterima.
	var invoice *string
	if err := l.pool.QueryRow(ctx,
		`SELECT invoice_no FROM orders WHERE id = $1`, o.ID).Scan(&invoice); err != nil {
		t.Fatalf("membaca nomor invoice: %v", err)
	}
	if invoice == nil || *invoice == "" {
		t.Fatal("nomor invoice seharusnya terbuat saat pembayaran diterima")
	}
}

// TestWebhook_PemrosesanUlangTidakMengubahApaPun menjaga agar pekerja yang
// mengambil event yang sama dua kali tidak menerapkannya dua kali.
func TestWebhook_PemrosesanUlangTidakMengubahApaPun(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)
	nominal := p.AmountCents
	ev := l.webhook(t, p, "settlement", &nominal)

	if _, err := l.bayar.ApplyEvent(ctx, ev); err != nil {
		t.Fatalf("pemrosesan pertama: %v", err)
	}
	kedua, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("pemrosesan kedua: %v", err)
	}
	if !kedua.AlreadyProcessed {
		t.Fatalf("pemrosesan kedua seharusnya ditandai sudah diproses: %+v", kedua)
	}

	// Riwayat pesanan hanya memuat satu perpindahan ke PAID.
	riwayat, err := l.pesanan.History(ctx, o.ID)
	if err != nil {
		t.Fatalf("membaca riwayat: %v", err)
	}
	var n int
	for _, e := range riwayat {
		if e.ToStatus == order.StatusPaid {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("perpindahan ke PAID tercatat %d kali, seharusnya 1", n)
	}
}

// TestWebhook_NominalTidakCocokTidakMelunasi adalah uji yang paling berdampak
// pada uang: pesanan hanya lunas bila yang dibayar sebesar tagihannya.
func TestWebhook_NominalTidakCocokTidakMelunasi(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	kurang := p.AmountCents - 100
	ev := l.webhook(t, p, "settlement", &kurang)

	hasil, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if !hasil.NeedsReview {
		t.Fatalf("nominal tidak cocok seharusnya ditandai untuk ditinjau: %+v", hasil)
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusWaitingPayment {
		t.Fatalf("status pesanan %q, seharusnya belum lunas", s)
	}

	// Dan muncul pada daftar tinjauan.
	daftar, err := l.bayar.EventsNeedingReview(ctx, 0)
	if err != nil {
		t.Fatalf("membaca daftar tinjauan: %v", err)
	}
	var ketemu bool
	for _, e := range daftar {
		if e.ID == ev {
			ketemu = true
			if e.ReviewNote == "" {
				t.Fatal("catatan tinjauan kosong")
			}
		}
	}
	if !ketemu {
		t.Fatal("event seharusnya muncul pada daftar tinjauan")
	}
}

// TestWebhook_StatusTidakDikenalMasukDaftarTinjauan menjaga SRS-PAY-003:
// status penyedia yang belum dikenal muncul pada daftar tinjauan, bukan hilang.
func TestWebhook_StatusTidakDikenalMasukDaftarTinjauan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	ev := l.webhook(t, p, "chargeback", nil)
	hasil, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if !hasil.NeedsReview {
		t.Fatalf("status tidak dikenal seharusnya ditinjau: %+v", hasil)
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusWaitingPayment {
		t.Fatalf("status pesanan %q berubah karena status yang belum dipahami", s)
	}
}

// TestWebhook_ReferensiTidakDikenalTetapTersimpan menjaga agar event yang
// tidak cocok dengan pembayaran mana pun tetap dapat ditelusuri. Event yang
// dibuang tidak dapat ditemukan lagi ketika nanti ada selisih dengan penyedia.
func TestWebhook_ReferensiTidakDikenalTetapTersimpan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()

	ev, dup, err := l.bayar.AcceptWebhook(ctx, payment.WebhookInput{
		EventID: "evt-" + uuid.NewString(), ProviderRef: "ref-yang-tidak-ada",
		ProviderStatus: "settlement", Payload: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("menerima webhook: %v", err)
	}
	if dup {
		t.Fatal("event baru ditandai duplikat")
	}

	hasil, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if !hasil.NeedsReview {
		t.Fatalf("event tanpa pembayaran seharusnya ditinjau: %+v", hasil)
	}
}

// TestWebhook_MuatanBukanJSONTetapTersimpan menjaga agar bukti tidak hilang
// hanya karena bentuk muatannya tidak terduga.
func TestWebhook_MuatanBukanJSONTetapTersimpan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	eventID := "evt-" + uuid.NewString()
	if _, _, err := l.bayar.AcceptWebhook(ctx, payment.WebhookInput{
		EventID: eventID, ProviderRef: p.ProviderRef,
		ProviderStatus: "pending", Payload: []byte("ini bukan json"),
	}); err != nil {
		t.Fatalf("menerima webhook: %v", err)
	}

	var muatan string
	if err := l.pool.QueryRow(ctx,
		`SELECT payload::text FROM payment_events WHERE event_id = $1`, eventID).Scan(&muatan); err != nil {
		t.Fatalf("membaca muatan: %v", err)
	}
	if muatan == "" || muatan == "{}" {
		t.Fatalf("muatan mentah seharusnya tersimpan, dapat %q", muatan)
	}
}

// TestWebhook_TagihanKedaluwarsaTidakMelunasi menjaga SRS-PAY-001. Uangnya
// nyata, jadi eventnya ditandai untuk ditinjau, bukan dibuang.
func TestWebhook_TagihanKedaluwarsaTidakMelunasi(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)
	p, _ := l.bayar.Charge(ctx, o.ID)

	// Masa berlakunya dibuat sudah lewat.
	if _, err := l.pool.Exec(ctx,
		`UPDATE payments SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		p.ID); err != nil {
		t.Fatalf("mengubah masa berlaku: %v", err)
	}

	nominal := p.AmountCents
	ev := l.webhook(t, p, "settlement", &nominal)
	hasil, err := l.bayar.ApplyEvent(ctx, ev)
	if err != nil {
		t.Fatalf("memproses event: %v", err)
	}
	if !hasil.NeedsReview {
		t.Fatalf("pembayaran atas tagihan kedaluwarsa seharusnya ditinjau: %+v", hasil)
	}
	if s := l.statusPesanan(t, o.ID); s != order.StatusWaitingPayment {
		t.Fatalf("status pesanan %q, tagihan kedaluwarsa tidak boleh melunasi", s)
	}
	// Catatan tinjauannya menyebut sebabnya, agar yang meninjau tahu ini soal
	// uang masuk yang perlu diputuskan.
	daftar, err := l.bayar.EventsNeedingReview(ctx, 0)
	if err != nil {
		t.Fatalf("membaca daftar tinjauan: %v", err)
	}
	for _, e := range daftar {
		if e.ID == ev && e.ReviewNote == "" {
			t.Fatal("catatan tinjauan seharusnya menyebut sebabnya")
		}
	}
}

// TestTagihan_KedaluwarsaDapatDigantiYangBaru menjaga SRS-PAY-001: pelanggan
// dapat mencoba lagi.
func TestTagihan_KedaluwarsaDapatDigantiYangBaru(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	o := l.buatPesananRitel(t)

	lama, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("tagihan pertama: %v", err)
	}
	if _, err := l.pool.Exec(ctx,
		`UPDATE payments SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		lama.ID); err != nil {
		t.Fatalf("mengubah masa berlaku: %v", err)
	}

	baru, err := l.bayar.Charge(ctx, o.ID)
	if err != nil {
		t.Fatalf("tagihan pengganti: %v", err)
	}
	if baru.ID == lama.ID {
		t.Fatal("tagihan kedaluwarsa seharusnya diganti yang baru")
	}

	// Yang lama ditandai kedaluwarsa, bukan dibiarkan menunggu.
	var statusLama string
	if err := l.pool.QueryRow(ctx,
		`SELECT status::text FROM payments WHERE id = $1`, lama.ID).Scan(&statusLama); err != nil {
		t.Fatalf("membaca status tagihan lama: %v", err)
	}
	if statusLama != payment.StatusExpired {
		t.Fatalf("status tagihan lama %q, seharusnya EXPIRED", statusLama)
	}
}

func TestWebhook_EventTanpaPengenalDitolak(t *testing.T) {
	l := siapkan(t)
	if _, _, err := l.bayar.AcceptWebhook(context.Background(), payment.WebhookInput{
		ProviderStatus: "settlement",
	}); err == nil {
		t.Fatal("event tanpa pengenal penyedia seharusnya ditolak")
	}
}

func TestWebhook_PenolakanTandaTanganTercatat(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()

	// Penanda dibuat unik per jalannya, karena uji ini menghitung baris pada
	// tabel yang dipakai bersama dan sisa dari jalannya yang lalu akan ikut
	// terhitung.
	penanda := uuid.NewString()
	if err := l.bayar.RecordReject(ctx, "tanda tangan tidak sah", "203.0.113.7", penanda); err != nil {
		t.Fatalf("mencatat penolakan: %v", err)
	}
	var jumlah int
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_webhook_rejects WHERE body_sha256 = $1`,
		penanda).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung penolakan: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("catatan penolakan %d, seharusnya 1", jumlah)
	}
}

func TestTagihan_PesananTidakAda(t *testing.T) {
	l := siapkan(t)
	if _, err := l.bayar.Charge(context.Background(), uuid.New()); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya order.ErrNotFound", err)
	}
}

// buatAdmin membuat satu pengguna internal, dipakai sebagai pelaku refund.
func (l *lingkungan) buatAdmin(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := l.pool.QueryRow(context.Background(), `
		INSERT INTO users (role_id, email, name, password_hash, status)
		SELECT r.id, $1, 'Keuangan Uji', 'x', 'ACTIVE'
		FROM   roles r WHERE r.code = 'FINANCE'
		RETURNING id`, "fin-"+uuid.NewString()[:8]+"@uji.test").Scan(&id)
	if err != nil {
		t.Fatalf("menyiapkan pengguna keuangan: %v", err)
	}
	return id
}

// sebagai menyelipkan pelaku ke konteks, seperti yang dilakukan middleware HTTP.
func sebagai(userID uuid.UUID) context.Context {
	return audit.WithActor(context.Background(), userID)
}

// jumlahPenolakan menghitung catatan penolakan webhook.
func (l *lingkungan) jumlahPenolakan(t *testing.T) int {
	t.Helper()
	var n int
	if err := l.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_webhook_rejects`).Scan(&n); err != nil {
		t.Fatalf("menghitung penolakan: %v", err)
	}
	return n
}

// nominalRupiah mengubah sen menjadi rupiah bulat, bentuk yang dipakai
// penyedia pembayaran Indonesia pada muatan webhooknya.
func nominalRupiah(sen int64) int64 { return sen / 100 }
