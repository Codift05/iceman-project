package notify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/notify"
)

// TestEmit_SatuBarisPerKanalAktif menjaga bentuk dasarnya: satu catatan per
// kanal, sehingga kegagalan pada satu kanal tidak menyembunyikan keberhasilan
// pada kanal lain.
func TestEmit_SatuBarisPerKanalAktif(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := notify.NewService(notify.Deps{Pool: pool})
	orderID, _, nomor, telepon := pesananUji(t, pool)

	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelLog, notify.ChannelPush)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("memulai transaksi: %v", err)
	}
	if err := svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, ""); err != nil {
		t.Fatalf("mengantre notifikasi: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan: %v", err)
	}

	baris := barisNotifikasi(t, svc, orderID)
	if len(baris) != 2 {
		t.Fatalf("baris notifikasi %d, seharusnya 2 (satu per kanal)", len(baris))
	}
	kanal := map[string]bool{}
	for _, b := range baris {
		kanal[b.Channel] = true
		if b.Status != notify.StatusPending {
			t.Fatalf("status %q, seharusnya PENDING", b.Status)
		}
		if b.Recipient != telepon {
			t.Fatalf("penerima %q, seharusnya %q", b.Recipient, telepon)
		}
		// Isi pesan menyebut nomor pesanan, supaya pelanggan tahu pesanan mana
		// yang dimaksud tanpa membuka aplikasi.
		if !strings.Contains(b.Body, nomor) {
			t.Fatalf("isi pesan tidak menyebut nomor pesanan: %q", b.Body)
		}
	}
	if !kanal[notify.ChannelLog] || !kanal[notify.ChannelPush] {
		t.Fatalf("kanal yang tercatat: %v", kanal)
	}
}

// TestEmit_IkutBatalSaatTransaksiBatal adalah janji yang paling penting di
// paket ini. Notifikasi yang bertahan setelah transaksinya dibatalkan berarti
// pelanggan diberi tahu tentang hal yang tidak pernah terjadi.
func TestEmit_IkutBatalSaatTransaksiBatal(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	antrean := &antreanPalsu{}
	svc := notify.NewService(notify.Deps{Pool: pool, Enqueuer: antrean})
	orderID, _, _, _ := pesananUji(t, pool)

	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelLog)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("memulai transaksi: %v", err)
	}
	if err := svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, ""); err != nil {
		t.Fatalf("mengantre notifikasi: %v", err)
	}
	// Di dalam transaksi, barisnya sudah terlihat.
	var didalam int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE order_id = $1`, orderID).Scan(&didalam); err != nil {
		t.Fatalf("menghitung di dalam transaksi: %v", err)
	}
	if didalam != 1 {
		t.Fatalf("di dalam transaksi seharusnya 1 baris, dapat %d", didalam)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("membatalkan transaksi: %v", err)
	}

	if baris := barisNotifikasi(t, svc, orderID); len(baris) != 0 {
		t.Fatalf("notifikasi bertahan setelah transaksi dibatalkan: %d baris", len(baris))
	}
}

// TestEmit_EventDimatikanDicatatSebagaiDilewati menjaga agar pemantauan dapat
// membedakan notifikasi yang sengaja tidak dikirim dari yang hilang tanpa
// jejak.
func TestEmit_EventDimatikanDicatatSebagaiDilewati(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	antrean := &antreanPalsu{}
	svc := notify.NewService(notify.Deps{Pool: pool, Enqueuer: antrean})
	orderID, _, _, _ := pesananUji(t, pool)

	setEvent(t, pool, notify.EventOrderCompleted, false, notify.ChannelLog)

	tx, _ := pool.Begin(ctx)
	if err := svc.EmitForOrderTx(ctx, tx, notify.EventOrderCompleted, orderID, ""); err != nil {
		t.Fatalf("mengantre notifikasi: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan: %v", err)
	}

	baris := barisNotifikasi(t, svc, orderID)
	if len(baris) != 1 {
		t.Fatalf("baris notifikasi %d, seharusnya 1 catatan dilewati", len(baris))
	}
	if baris[0].Status != notify.StatusSkipped {
		t.Fatalf("status %q, seharusnya SKIPPED", baris[0].Status)
	}
	if baris[0].LastError == "" {
		t.Fatal("alasan dilewati seharusnya dicatat")
	}
	// Dan tidak ada yang diantre: tidak ada yang perlu dikirim.
	if len(antrean.masuk) != 0 {
		t.Fatalf("event yang dimatikan seharusnya tidak diantre, dapat %d", len(antrean.masuk))
	}
}

// TestEmit_EventTanpaKanalDicatatSebagaiDilewati memisahkan sebab yang
// berbeda: event aktif namun belum punya kanal.
func TestEmit_EventTanpaKanalDicatatSebagaiDilewati(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := notify.NewService(notify.Deps{Pool: pool})
	orderID, _, _, _ := pesananUji(t, pool)

	setEvent(t, pool, notify.EventOrderCancelled, true)

	tx, _ := pool.Begin(ctx)
	if err := svc.EmitForOrderTx(ctx, tx, notify.EventOrderCancelled, orderID, ""); err != nil {
		t.Fatalf("mengantre notifikasi: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan: %v", err)
	}

	baris := barisNotifikasi(t, svc, orderID)
	if len(baris) != 1 || baris[0].Status != notify.StatusSkipped {
		t.Fatalf("seharusnya satu catatan dilewati: %+v", baris)
	}
	if !strings.Contains(baris[0].LastError, "kanal") {
		t.Fatalf("alasan seharusnya menyebut kanal: %q", baris[0].LastError)
	}
}

// TestEmit_EventTidakDikenalDitolak menjaga agar salah tulis nama event tidak
// menghasilkan notifikasi yang tidak pernah terkirim tanpa sebab yang jelas.
func TestEmit_EventTidakDikenalDitolak(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := notify.NewService(notify.Deps{Pool: pool})
	orderID, _, _, _ := pesananUji(t, pool)

	tx, _ := pool.Begin(ctx)
	defer tx.Rollback(ctx)
	err := svc.EmitForOrderTx(ctx, tx, "EVENT_KARANGAN", orderID, "")
	if !errors.Is(err, notify.ErrEventUnknown) {
		t.Fatalf("galat %v, seharusnya ErrEventUnknown", err)
	}
}

// TestKirim_BerhasilDanGagalTercatat menjaga SRS-NOT-001: setiap percobaan
// tercatat beserta kanal, status, dan alasan kegagalannya.
func TestKirim_BerhasilDanGagalTercatat(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	orderID, _, _, _ := pesananUji(t, pool)

	berhasil := &kanalPalsu{nama: notify.ChannelLog}
	svc := notify.NewService(notify.Deps{
		Pool: pool, Channels: notify.NewRegistry(berhasil),
	})
	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelLog)

	tx, _ := pool.Begin(ctx)
	if err := svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, ""); err != nil {
		t.Fatalf("mengantre: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan: %v", err)
	}

	baris := barisNotifikasi(t, svc, orderID)
	if len(baris) != 1 {
		t.Fatalf("baris %d, seharusnya 1", len(baris))
	}
	if err := svc.Send(ctx, baris[0].ID); err != nil {
		t.Fatalf("mengirim: %v", err)
	}
	if len(berhasil.terkirim) != 1 {
		t.Fatalf("kanal menerima %d pesan, seharusnya 1", len(berhasil.terkirim))
	}

	lagi := barisNotifikasi(t, svc, orderID)
	if lagi[0].Status != notify.StatusSent {
		t.Fatalf("status %q, seharusnya SENT", lagi[0].Status)
	}
	if lagi[0].SentAt == nil {
		t.Fatal("waktu kirim seharusnya tercatat")
	}
	if lagi[0].Attempt != 1 {
		t.Fatalf("percobaan %d, seharusnya 1", lagi[0].Attempt)
	}
}

// TestKirim_KegagalanDicatatDanDikembalikanUntukDicobaUlang menjaga agar
// kegagalan terlihat pada pemantauan dan jobnya dicoba ulang.
func TestKirim_KegagalanDicatatDanDikembalikanUntukDicobaUlang(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	orderID, _, _, _ := pesananUji(t, pool)

	rusak := &kanalPalsu{nama: notify.ChannelLog, gagal: errors.New("layanan kanal mati")}
	svc := notify.NewService(notify.Deps{
		Pool: pool, Channels: notify.NewRegistry(rusak),
	})
	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelLog)

	tx, _ := pool.Begin(ctx)
	_ = svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, "")
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("menyimpan: %v", err)
	}
	baris := barisNotifikasi(t, svc, orderID)

	// Galat dikembalikan agar jobnya dicoba ulang.
	if err := svc.Send(ctx, baris[0].ID); err == nil {
		t.Fatal("kegagalan kanal seharusnya dikembalikan sebagai galat")
	}

	lagi := barisNotifikasi(t, svc, orderID)
	if lagi[0].Status != notify.StatusFailed {
		t.Fatalf("status %q, seharusnya FAILED", lagi[0].Status)
	}
	if !strings.Contains(lagi[0].LastError, "layanan kanal mati") {
		t.Fatalf("alasan kegagalan tidak tercatat: %q", lagi[0].LastError)
	}
	if lagi[0].Attempt != 1 {
		t.Fatalf("percobaan %d, seharusnya 1", lagi[0].Attempt)
	}

	// Percobaan kedua menambah penghitungnya, bukan menggantinya.
	_ = svc.Send(ctx, baris[0].ID)
	lagi2 := barisNotifikasi(t, svc, orderID)
	if lagi2[0].Attempt != 2 {
		t.Fatalf("percobaan %d sesudah dua kali, seharusnya 2", lagi2[0].Attempt)
	}
}

// TestKirim_YangSudahTerkirimTidakDikirimUlang menjaga agar pekerja yang
// mengambil job yang sama dua kali tidak membuat pelanggan menerima dua pesan
// dan mengira ada dua pesanan.
func TestKirim_YangSudahTerkirimTidakDikirimUlang(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	orderID, _, _, _ := pesananUji(t, pool)

	kanal := &kanalPalsu{nama: notify.ChannelLog}
	svc := notify.NewService(notify.Deps{
		Pool: pool, Channels: notify.NewRegistry(kanal),
	})
	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelLog)

	tx, _ := pool.Begin(ctx)
	_ = svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, "")
	_ = tx.Commit(ctx)
	baris := barisNotifikasi(t, svc, orderID)

	for i := 0; i < 3; i++ {
		if err := svc.Send(ctx, baris[0].ID); err != nil {
			t.Fatalf("pengiriman ke-%d: %v", i+1, err)
		}
	}
	if len(kanal.terkirim) != 1 {
		t.Fatalf("kanal menerima %d pesan, seharusnya 1", len(kanal.terkirim))
	}
}

// TestKirim_KanalBelumDisambungkanDitandaiGagal menjaga agar pengaturan yang
// menyebut kanal tanpa implementasi tidak dicoba ulang terus menerus. Selama
// OQ-012 belum diputuskan, keadaan itu memang terjadi.
func TestKirim_KanalBelumDisambungkanDitandaiGagal(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	orderID, _, _, _ := pesananUji(t, pool)

	// Hanya kanal catatan yang tersedia, sementara pengaturannya menyebut push.
	svc := notify.NewService(notify.Deps{
		Pool:     pool,
		Channels: notify.NewRegistry(&kanalPalsu{nama: notify.ChannelLog}),
	})
	setEvent(t, pool, notify.EventOrderPaid, true, notify.ChannelPush)

	tx, _ := pool.Begin(ctx)
	_ = svc.EmitForOrderTx(ctx, tx, notify.EventOrderPaid, orderID, "")
	_ = tx.Commit(ctx)
	baris := barisNotifikasi(t, svc, orderID)

	// Tidak mengembalikan galat: mengulanginya tidak akan mengubah apa pun
	// sampai kanalnya disambungkan.
	if err := svc.Send(ctx, baris[0].ID); err != nil {
		t.Fatalf("kanal yang belum ada seharusnya tidak dicoba ulang: %v", err)
	}
	lagi := barisNotifikasi(t, svc, orderID)
	if lagi[0].Status != notify.StatusFailed {
		t.Fatalf("status %q, seharusnya FAILED", lagi[0].Status)
	}
	if !strings.Contains(lagi[0].LastError, "belum disambungkan") {
		t.Fatalf("alasan seharusnya menyebut kanal belum disambungkan: %q", lagi[0].LastError)
	}
	// Penghitung percobaan tidak bertambah, karena tidak ada percobaan
	// pengiriman yang benar benar terjadi.
	if lagi[0].Attempt != 0 {
		t.Fatalf("percobaan %d, seharusnya 0", lagi[0].Attempt)
	}
}

// TestPengaturan_DaftarLengkapWalauBelumDiatur menjaga agar admin melihat
// seluruh event yang ada dan tidak perlu menebak.
func TestPengaturan_DaftarLengkapWalauBelumDiatur(t *testing.T) {
	pool := newPool(t)
	svc := notify.NewService(notify.Deps{Pool: pool})

	out, err := svc.Settings(context.Background())
	if err != nil {
		t.Fatalf("membaca pengaturan: %v", err)
	}
	if len(out) != len(notify.KnownEvents()) {
		t.Fatalf("pengaturan terbaca %d, seharusnya %d sesuai daftar event",
			len(out), len(notify.KnownEvents()))
	}
	ada := map[string]bool{}
	for _, s := range out {
		ada[s.Event] = true
	}
	for _, e := range notify.KnownEvents() {
		if !ada[e] {
			t.Fatalf("event %s hilang dari daftar pengaturan", e)
		}
	}
}

// TestPengaturan_KanalTidakDikenalDitolak menjaga agar admin yang salah
// menulis nama kanal tahu saat menyimpannya, bukan nanti saat notifikasinya
// tidak sampai.
func TestPengaturan_KanalTidakDikenalDitolak(t *testing.T) {
	pool := newPool(t)
	svc := notify.NewService(notify.Deps{Pool: pool})

	_, err := svc.SetSetting(context.Background(), notify.Setting{
		Event: notify.EventOrderPaid, Enabled: true,
		Channels: []string{notify.ChannelLog, "TELEGRAM"},
	})
	if !errors.Is(err, notify.ErrChannelUnknown) {
		t.Fatalf("galat %v, seharusnya ErrChannelUnknown", err)
	}

	_, err = svc.SetSetting(context.Background(), notify.Setting{
		Event: "EVENT_KARANGAN", Enabled: true,
	})
	if !errors.Is(err, notify.ErrEventUnknown) {
		t.Fatalf("galat %v, seharusnya ErrEventUnknown", err)
	}
}

// TestPengaturan_PerubahanTercatatPadaAudit menjaga agar notifikasi yang
// berhenti sampai karena eventnya dimatikan dapat ditelusuri.
func TestPengaturan_PerubahanTercatatPadaAudit(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	svc := notify.NewService(notify.Deps{Pool: pool})

	if _, err := svc.SetSetting(ctx, notify.Setting{
		Event: notify.EventOrderCompleted, Enabled: true,
		Channels: []string{notify.ChannelLog},
	}); err != nil {
		t.Fatalf("menyimpan pengaturan: %v", err)
	}
	if _, err := svc.SetSetting(ctx, notify.Setting{
		Event: notify.EventOrderCompleted, Enabled: false,
		Channels: []string{notify.ChannelLog},
	}); err != nil {
		t.Fatalf("mematikan event: %v", err)
	}

	var detail string
	err := pool.QueryRow(ctx, `
		SELECT detail FROM audit_trail
		WHERE  entity = 'notification_settings' AND detail LIKE '%dimatikan%'
		ORDER  BY occurred_at DESC LIMIT 1`).Scan(&detail)
	if err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if !strings.Contains(detail, "dimatikan") {
		t.Fatalf("jejak audit tidak menyebut event dimatikan: %q", detail)
	}
}

// TestPengaturan_KanalGandaDibersihkan menjaga agar kanal yang disebut dua
// kali tidak menghasilkan dua notifikasi untuk satu kejadian.
func TestPengaturan_KanalGandaDibersihkan(t *testing.T) {
	pool := newPool(t)
	svc := notify.NewService(notify.Deps{Pool: pool})

	out, err := svc.SetSetting(context.Background(), notify.Setting{
		Event: notify.EventOrderPaid, Enabled: true,
		Channels: []string{"log", "LOG", " log ", notify.ChannelPush},
	})
	if err != nil {
		t.Fatalf("menyimpan pengaturan: %v", err)
	}
	if len(out.Channels) != 2 {
		t.Fatalf("kanal tersimpan %v, seharusnya dua tanpa duplikat", out.Channels)
	}
}

// TestSusunPesan_SeluruhEventPunyaKalimat menjaga agar tidak ada event yang
// terkirim dengan pesan bawaan yang tidak menjelaskan apa pun.
func TestSusunPesan_SeluruhEventPunyaKalimat(t *testing.T) {
	for _, e := range notify.KnownEvents() {
		judul, isi := notify.Compose(e, "ICE-261005-00001", "")
		if judul == "" || isi == "" {
			t.Fatalf("event %s tanpa judul atau isi", e)
		}
		if judul == "Pemberitahuan" {
			t.Fatalf("event %s memakai judul bawaan, seharusnya punya kalimatnya sendiri", e)
		}
	}

	// Alasan dari pemanggil ditambahkan di belakang.
	_, isi := notify.Compose(notify.EventOrderCancelled, "ICE-1", "Alasan: stok habis.")
	if !strings.Contains(isi, "stok habis") {
		t.Fatalf("alasan tidak diteruskan: %q", isi)
	}
}

func TestKirim_NotifikasiTidakAda(t *testing.T) {
	pool := newPool(t)
	svc := notify.NewService(notify.Deps{Pool: pool})
	if err := svc.Send(context.Background(), uuid.New()); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("galat %v, seharusnya ErrNotFound", err)
	}
}
