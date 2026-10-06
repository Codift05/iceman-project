package payment_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/payment"
)

func hariIni() string { return time.Now().Format("2006-01-02") }

func periode() payment.ReconcileFilter {
	return payment.ReconcileFilter{
		From:  time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		Until: time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
	}
}

// cariBaris mencari baris laporan menurut referensi penyedianya.
func cariBaris(r *payment.ReconcileResult, ref string) *payment.ReconcileRow {
	for i := range r.Rows {
		if r.Rows[i].ProviderRef == ref {
			return &r.Rows[i]
		}
	}
	return nil
}

// TestRekonsiliasi_PembayaranTanpaSettlementTampilSebagaiSelisih menjaga janji
// SRS-PAY-005 yang paling penting: pembayaran yang belum muncul pada settlement
// tetap tampil sebagai selisih, bukan disembunyikan. Itu justru kasus yang
// paling perlu dilihat, yaitu uang yang sudah ditagihkan namun belum diterima.
func TestRekonsiliasi_PembayaranTanpaSettlementTampilSebagaiSelisih(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}

	baris := cariBaris(hasil, p.ProviderRef)
	if baris == nil {
		t.Fatal("pembayaran tanpa settlement hilang dari laporan")
	}
	// Selisihnya sebesar seluruh nominalnya, bukan nol, karena seluruhnya
	// memang belum diterima.
	if baris.DiffCents != p.AmountCents {
		t.Fatalf("selisih %d, seharusnya %d", baris.DiffCents, p.AmountCents)
	}
	if baris.Flag != payment.FlagNoSettlement {
		t.Fatalf("penanda %q, seharusnya NO_SETTLEMENT", baris.Flag)
	}
	if baris.GrossCents != nil {
		t.Fatal("baris tanpa settlement seharusnya tanpa nominal settlement")
	}
}

// TestRekonsiliasi_SettlementCocokTidakBerselisih menjaga sisi sebaliknya.
func TestRekonsiliasi_SettlementCocokTidakBerselisih(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	const biaya = 70000
	impor, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{{
		ProviderRef: p.ProviderRef, GrossCents: p.AmountCents,
		FeeCents: biaya, SettledDate: time.Now(),
	}})
	if err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}
	if impor.Inserted != 1 || impor.Unmatched != 0 {
		t.Fatalf("hasil pemasukan salah: %+v", impor)
	}

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}
	baris := cariBaris(hasil, p.ProviderRef)
	if baris == nil {
		t.Fatal("pembayaran hilang dari laporan")
	}
	if baris.DiffCents != 0 {
		t.Fatalf("selisih %d, seharusnya 0", baris.DiffCents)
	}
	if baris.Flag != "" {
		t.Fatalf("penanda %q, seharusnya kosong", baris.Flag)
	}
	if baris.FeeCents == nil || *baris.FeeCents != biaya {
		t.Fatalf("biaya penyedia pada laporan salah: %+v", baris.FeeCents)
	}
	if baris.NetCents == nil || *baris.NetCents != p.AmountCents-biaya {
		t.Fatalf("nominal diterima salah: %+v", baris.NetCents)
	}

	// Biaya penyedia disalin ke baris pembayaran.
	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.ProviderFeeCents != biaya {
		t.Fatalf("biaya pada pembayaran %d, seharusnya %d", lagi.ProviderFeeCents, biaya)
	}
}

// TestRekonsiliasi_NominalBerbedaDitandai menjaga agar yang diterima tidak
// sama dengan yang ditagihkan selalu terlihat.
func TestRekonsiliasi_NominalBerbedaDitandai(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	kurang := p.AmountCents - 50000
	if _, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{{
		ProviderRef: p.ProviderRef, GrossCents: kurang,
		FeeCents: 1000, SettledDate: time.Now(),
	}}); err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}
	baris := cariBaris(hasil, p.ProviderRef)
	if baris == nil {
		t.Fatal("pembayaran hilang dari laporan")
	}
	if baris.DiffCents != 50000 {
		t.Fatalf("selisih %d, seharusnya 50000", baris.DiffCents)
	}
	if baris.Flag != payment.FlagAmountMismatch {
		t.Fatalf("penanda %q, seharusnya AMOUNT_MISMATCH", baris.Flag)
	}
	// Dan sudah ditandai otomatis saat berkasnya dimasukkan, sehingga muncul
	// pada daftar tindak lanjut tanpa perlu ditandai manual.
	if !baris.FlagOpen {
		t.Fatal("selisih nominal seharusnya sudah ditandai saat pemasukan")
	}
}

// TestRekonsiliasi_SettlementTanpaPembayaranTampil menjaga agar uang yang
// masuk tanpa diketahui asalnya ikut tertelusuri.
func TestRekonsiliasi_SettlementTanpaPembayaranTampil(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()

	ref := "ref-asing-" + uuid.NewString()[:8]
	impor, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{{
		ProviderRef: ref, GrossCents: 500000, FeeCents: 5000, SettledDate: time.Now(),
	}})
	if err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}
	if impor.Unmatched != 1 {
		t.Fatalf("baris tanpa pembayaran %d, seharusnya 1", impor.Unmatched)
	}
	// Barisnya tetap disimpan, bukan dibuang.
	if impor.Inserted != 1 {
		t.Fatalf("baris tersimpan %d, seharusnya 1", impor.Inserted)
	}

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}
	baris := cariBaris(hasil, ref)
	if baris == nil {
		t.Fatal("settlement tanpa pembayaran hilang dari laporan")
	}
	if baris.PaymentID != nil {
		t.Fatal("baris ini seharusnya tanpa pembayaran")
	}
	// Selisihnya negatif: uang masuk yang belum ada tagihannya.
	if baris.DiffCents != -500000 {
		t.Fatalf("selisih %d, seharusnya -500000", baris.DiffCents)
	}
	if baris.Flag != payment.FlagOrphanSettlement {
		t.Fatalf("penanda %q, seharusnya ORPHAN_SETTLEMENT", baris.Flag)
	}
	// Ringkasan menghitung seluruh periode pada basis data yang dipakai
	// bersama, sehingga yang diperiksa adalah barisnya ikut terhitung, bukan
	// jumlah mutlaknya. Barisnya sendiri sudah diperiksa di atas.
	if hasil.Summary.OrphanSettlements < 1 {
		t.Fatalf("ringkasan menyebut %d settlement tanpa pembayaran, seharusnya setidaknya 1",
			hasil.Summary.OrphanSettlements)
	}
}

// TestRekonsiliasi_PemasukanUlangTidakMenggandakan menjaga agar berkas
// settlement yang dikirim ulang dengan tambahan baris baru tidak menggandakan
// baris yang sudah ada.
func TestRekonsiliasi_PemasukanUlangTidakMenggandakan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	baris := payment.SettlementRow{
		ProviderRef: p.ProviderRef, GrossCents: p.AmountCents,
		FeeCents: 70000, SettledDate: time.Now(),
	}

	pertama, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{baris})
	if err != nil {
		t.Fatalf("pemasukan pertama: %v", err)
	}
	if pertama.Inserted != 1 {
		t.Fatalf("tersimpan %d, seharusnya 1", pertama.Inserted)
	}

	// Berkas dikirim ulang, ditambah satu baris baru.
	refBaru := "ref-baru-" + uuid.NewString()[:8]
	kedua, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{
		baris,
		{ProviderRef: refBaru, GrossCents: 100000, FeeCents: 1000, SettledDate: time.Now()},
	})
	if err != nil {
		t.Fatalf("pemasukan kedua: %v", err)
	}
	if kedua.Duplicate != 1 {
		t.Fatalf("duplikat %d, seharusnya 1", kedua.Duplicate)
	}
	if kedua.Inserted != 1 {
		t.Fatalf("tersimpan %d, seharusnya 1 yang baru", kedua.Inserted)
	}

	var jumlah int
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM settlements WHERE provider_ref = $1`,
		p.ProviderRef).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung settlement: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("baris settlement %d, seharusnya 1", jumlah)
	}
}

// TestRekonsiliasi_BarisTidakSahDilewatiBukanMenggagalkanBerkas menjaga agar
// satu baris rusak tidak membuang seluruh berkas settlement.
func TestRekonsiliasi_BarisTidakSahDilewatiBukanMenggagalkanBerkas(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	hasil, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{
		{ProviderRef: "", GrossCents: 1000, FeeCents: 0, SettledDate: time.Now()},
		{ProviderRef: "ref-x", GrossCents: -5, FeeCents: 0, SettledDate: time.Now()},
		{ProviderRef: "ref-y", GrossCents: 1000, FeeCents: 2000, SettledDate: time.Now()},
		{ProviderRef: "ref-z", GrossCents: 1000, FeeCents: 0},
		{ProviderRef: p.ProviderRef, GrossCents: p.AmountCents, FeeCents: 1000,
			SettledDate: time.Now()},
	})
	if err != nil {
		t.Fatalf("satu baris rusak tidak boleh menggagalkan berkas: %v", err)
	}
	if hasil.Inserted != 1 {
		t.Fatalf("tersimpan %d, seharusnya 1", hasil.Inserted)
	}
	if len(hasil.Rejected) != 4 {
		t.Fatalf("ditolak %d, seharusnya 4 beserta alasannya: %v",
			len(hasil.Rejected), hasil.Rejected)
	}
}

// TestRekonsiliasi_RingkasanMenjumlahkanDenganBenar menjaga angka yang dibaca
// keuangan.
func TestRekonsiliasi_RingkasanMenjumlahkanDenganBenar(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()

	// Dua pembayaran: satu sudah disettlement, satu belum.
	sudah := l.lunas(t)
	belum := l.lunas(t)

	const biaya = 60000
	if _, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{{
		ProviderRef: sudah.ProviderRef, GrossCents: sudah.AmountCents,
		FeeCents: biaya, SettledDate: time.Now(),
	}}); err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}

	// Ringkasan dapat memuat pembayaran dari uji lain pada basis data yang
	// sama, jadi yang diperiksa hubungan antar angkanya, bukan nilai mutlak.
	s := hasil.Summary
	if s.BilledCents < sudah.AmountCents+belum.AmountCents {
		t.Fatalf("ditagihkan %d, seharusnya mencakup kedua pembayaran uji", s.BilledCents)
	}
	if s.SettledCents < sudah.AmountCents {
		t.Fatalf("disettlement %d, seharusnya mencakup pembayaran yang sudah settle",
			s.SettledCents)
	}
	if s.FeeCents < biaya {
		t.Fatalf("biaya %d, seharusnya mencakup %d", s.FeeCents, biaya)
	}
	if s.NetCents != s.SettledCents-s.FeeCents {
		t.Fatalf("diterima %d, seharusnya disettlement %d dikurangi biaya %d",
			s.NetCents, s.SettledCents, s.FeeCents)
	}
	if s.Unsettled < 1 {
		t.Fatalf("belum settle %d, seharusnya setidaknya 1", s.Unsettled)
	}
	if s.From != periode().From || s.To != periode().Until {
		t.Fatalf("periode pada ringkasan salah: %s sampai %s", s.From, s.To)
	}
}

// TestRekonsiliasi_HanyaBerselisih menjaga saringan tindak lanjut.
func TestRekonsiliasi_HanyaBerselisih(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	cocok := l.lunas(t)
	timpang := l.lunas(t)

	if _, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{
		{ProviderRef: cocok.ProviderRef, GrossCents: cocok.AmountCents,
			FeeCents: 1000, SettledDate: time.Now()},
		{ProviderRef: timpang.ProviderRef, GrossCents: timpang.AmountCents - 1,
			FeeCents: 1000, SettledDate: time.Now()},
	}); err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}

	f := periode()
	f.OnlyDiff = true
	hasil, err := l.bayar.Reconcile(ctx, f)
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}

	if cariBaris(hasil, cocok.ProviderRef) != nil {
		t.Fatal("baris yang cocok seharusnya tidak muncul pada saringan selisih")
	}
	if cariBaris(hasil, timpang.ProviderRef) == nil {
		t.Fatal("baris yang berselisih seharusnya muncul")
	}
	// Ringkasan tetap menghitung seluruh periode, bukan hanya yang disaring,
	// supaya angka totalnya tidak menyesatkan.
	if hasil.Summary.SettledCents < cocok.AmountCents {
		t.Fatalf("ringkasan seharusnya tetap menghitung baris yang cocok: %+v",
			hasil.Summary)
	}
}

func TestRekonsiliasi_PeriodeTidakSahDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()

	kasus := []payment.ReconcileFilter{
		{},
		{From: hariIni()},
		{Until: hariIni()},
		{From: "bukan-tanggal", Until: hariIni()},
		{From: hariIni(), Until: "bukan-tanggal"},
		{From: "2026-10-10", Until: "2026-10-01"},
	}
	for i, f := range kasus {
		if _, err := l.bayar.Reconcile(ctx, f); !errors.Is(err, payment.ErrPeriodInvalid) {
			t.Fatalf("kasus ke-%d seharusnya ditolak, dapat %v", i, err)
		}
	}
}

// TestRekonsiliasi_PembacaanTercatatPadaAudit menjaga agar siapa yang membuka
// laporan penerimaan satu periode dapat ditelusuri.
func TestRekonsiliasi_PembacaanTercatatPadaAudit(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	fin := l.buatAdmin(t)

	if _, err := l.bayar.Reconcile(sebagai(fin), periode()); err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}

	var jumlah int
	if err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_trail
		WHERE  entity = 'settlements' AND action = 'ACCESS' AND actor_id = $1`,
		fin).Scan(&jumlah); err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if jumlah < 1 {
		t.Fatal("pembacaan laporan seharusnya tercatat pada jejak audit")
	}
}

// TestPenandaan_DitandaiDanDiselesaikan menjaga SRS-PAY-005: selisih dapat
// ditandai untuk tindak lanjut beserta catatan.
func TestPenandaan_DitandaiDanDiselesaikan(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)
	fin := l.buatAdmin(t)

	f, err := l.bayar.FlagDiscrepancy(sebagai(fin), payment.FlagInput{
		PaymentID: &p.ID, Kind: payment.FlagNoSettlement,
		Note: "belum muncul pada settlement dua hari",
	})
	if err != nil {
		t.Fatalf("menandai selisih: %v", err)
	}
	if f.ActorID == nil || *f.ActorID != fin {
		t.Fatalf("pelaku penandaan tidak tercatat: %+v", f.ActorID)
	}

	// Menandai hal yang sama dua kali ditolak: hanya menambah pekerjaan
	// tinjauan tanpa menambah informasi.
	if _, err := l.bayar.FlagDiscrepancy(sebagai(fin), payment.FlagInput{
		PaymentID: &p.ID, Kind: payment.FlagNoSettlement, Note: "lagi",
	}); !errors.Is(err, payment.ErrAlreadyFlagged) {
		t.Fatalf("penandaan kedua seharusnya ditolak, dapat %v", err)
	}

	terbuka, err := l.bayar.OpenFlags(ctx, 0)
	if err != nil {
		t.Fatalf("membaca selisih terbuka: %v", err)
	}
	var ketemu bool
	for _, x := range terbuka {
		if x.ID == f.ID {
			ketemu = true
		}
	}
	if !ketemu {
		t.Fatal("penandaan seharusnya muncul pada daftar selisih terbuka")
	}

	if err := l.bayar.ResolveFlag(sebagai(fin), f.ID, "settlement menyusul sehari kemudian"); err != nil {
		t.Fatalf("menyelesaikan selisih: %v", err)
	}

	// Catatan awal tidak hilang: ia menjelaskan apa selisihnya, catatan
	// penutup menjelaskan bagaimana diselesaikan.
	var catatan string
	var pelakuSelesai *uuid.UUID
	if err := l.pool.QueryRow(ctx,
		`SELECT note, resolved_by FROM reconciliation_flags WHERE id = $1`,
		f.ID).Scan(&catatan, &pelakuSelesai); err != nil {
		t.Fatalf("membaca penandaan: %v", err)
	}
	if !strings.Contains(catatan, "belum muncul pada settlement") {
		t.Fatalf("catatan awal hilang: %q", catatan)
	}
	if !strings.Contains(catatan, "menyusul sehari kemudian") {
		t.Fatalf("catatan penutup tidak tersimpan: %q", catatan)
	}
	if pelakuSelesai == nil || *pelakuSelesai != fin {
		t.Fatal("pelaku penyelesaian tidak tercatat")
	}

	// Sesudah diselesaikan, hal yang sama dapat ditandai lagi bila muncul
	// kembali.
	if _, err := l.bayar.FlagDiscrepancy(sebagai(fin), payment.FlagInput{
		PaymentID: &p.ID, Kind: payment.FlagNoSettlement, Note: "muncul lagi",
	}); err != nil {
		t.Fatalf("penandaan sesudah selesai seharusnya diterima: %v", err)
	}
}

// TestPenandaan_PenyelesaianTanpaPelakuDitolak menjaga agar selisih tidak
// dapat ditutup tanpa ada yang bertanggung jawab.
func TestPenandaan_PenyelesaianTanpaPelakuDitolak(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)
	fin := l.buatAdmin(t)

	f, err := l.bayar.FlagDiscrepancy(sebagai(fin), payment.FlagInput{
		PaymentID: &p.ID, Kind: payment.FlagNoSettlement, Note: "selisih",
	})
	if err != nil {
		t.Fatalf("menandai selisih: %v", err)
	}
	if err := l.bayar.ResolveFlag(ctx, f.ID, "coba tanpa pelaku"); err == nil {
		t.Fatal("penyelesaian tanpa pengguna yang masuk seharusnya ditolak")
	}
}

func TestPenandaan_TanpaSubjekDitolak(t *testing.T) {
	l := siapkan(t)
	if _, err := l.bayar.FlagDiscrepancy(context.Background(), payment.FlagInput{
		Kind: payment.FlagNoSettlement,
	}); !errors.Is(err, payment.ErrFlagNoSubject) {
		t.Fatalf("galat %v, seharusnya ErrFlagNoSubject", err)
	}
}

func TestPenandaan_TidakAdaDitolak(t *testing.T) {
	l := siapkan(t)
	fin := l.buatAdmin(t)
	if err := l.bayar.ResolveFlag(sebagai(fin), uuid.New(), "coba"); !errors.Is(err, payment.ErrFlagNotFound) {
		t.Fatalf("galat %v, seharusnya ErrFlagNotFound", err)
	}
}

// TestKoreksiBiaya_TercatatNilaiLamaDanBaru menjaga SRS-PAY-005: koreksi
// manual meninggalkan jejak nilai lama dan nilai baru.
func TestKoreksiBiaya_TercatatNilaiLamaDanBaru(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)
	fin := l.buatAdmin(t)

	if _, err := l.bayar.ImportSettlements(ctx, []payment.SettlementRow{{
		ProviderRef: p.ProviderRef, GrossCents: p.AmountCents,
		FeeCents: 70000, SettledDate: time.Now(),
	}}); err != nil {
		t.Fatalf("memasukkan settlement: %v", err)
	}

	if err := l.bayar.CorrectFee(sebagai(fin), p.ID, 65000,
		"biaya pada berkas penyedia salah, dikoreksi sesuai invoice"); err != nil {
		t.Fatalf("mengoreksi biaya: %v", err)
	}

	lagi, err := l.bayar.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("membaca pembayaran: %v", err)
	}
	if lagi.ProviderFeeCents != 65000 {
		t.Fatalf("biaya sesudah koreksi %d, seharusnya 65000", lagi.ProviderFeeCents)
	}

	var detail string
	if err := l.pool.QueryRow(ctx, `
		SELECT detail FROM audit_trail
		WHERE  entity = 'payments' AND entity_id = $1 AND actor_id = $2
		  AND  detail LIKE 'koreksi manual%'`, p.ID, fin).Scan(&detail); err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if !strings.Contains(detail, "70000") || !strings.Contains(detail, "65000") {
		t.Fatalf("jejak audit tidak menyebut nilai lama dan baru: %q", detail)
	}
}

// TestKoreksiBiaya_ValidasiMasukan menjaga agar salah satuan tertangkap.
// Rupiah yang dimasukkan sebagai sen akan membuat biayanya melebihi nominal
// pembayaran seratus kali.
func TestKoreksiBiaya_ValidasiMasukan(t *testing.T) {
	l := siapkan(t)
	p := l.lunas(t)
	fin := l.buatAdmin(t)

	// Keduanya memakai galat tersendiri, bukan galat umum, supaya petugas
	// keuangan diberi tahu apa yang salah alih alih "terjadi gangguan".
	if err := l.bayar.CorrectFee(sebagai(fin), p.ID, -1, "coba negatif"); !errors.Is(err, payment.ErrFeeInvalid) {
		t.Fatalf("biaya negatif seharusnya ErrFeeInvalid, dapat %v", err)
	}
	err := l.bayar.CorrectFee(sebagai(fin), p.ID, p.AmountCents+1, "coba melebihi")
	if !errors.Is(err, payment.ErrFeeInvalid) {
		t.Fatalf("biaya melebihi nominal seharusnya ErrFeeInvalid, dapat %v", err)
	}
	// Pesannya menyebut kemungkinan salah satuan, karena itu kesalahan yang
	// paling sering terjadi di sini.
	if !strings.Contains(err.Error(), "satuan") {
		t.Fatalf("pesan galat seharusnya menyebut satuannya: %q", err.Error())
	}
	if err := l.bayar.CorrectFee(sebagai(fin), p.ID, 1000, "  "); !errors.Is(err, payment.ErrReasonRequired) {
		t.Fatal("koreksi tanpa alasan seharusnya ditolak")
	}
	if err := l.bayar.CorrectFee(sebagai(fin), uuid.New(), 1000, "coba"); !errors.Is(err, payment.ErrNotFound) {
		t.Fatal("pembayaran tidak ada seharusnya ErrNotFound")
	}
}

// TestBacaCSV_JudulKolomDilewati menjaga agar berkas settlement dengan maupun
// tanpa judul kolom keduanya terbaca. Menolak salah satunya berarti petugas
// keuangan harus menyuntingnya lebih dahulu.
func TestBacaCSV_JudulKolomDilewati(t *testing.T) {
	denganJudul := `referensi,gross,biaya,tanggal
ref-1,25000.00,700.00,2026-10-05
ref-2,50000,1400,2026-10-05
`
	rows, ditolak, err := payment.ParseSettlementCSV(strings.NewReader(denganJudul))
	if err != nil {
		t.Fatalf("membaca CSV berjudul: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("baris terbaca %d, seharusnya 2", len(rows))
	}
	if len(ditolak) != 0 {
		t.Fatalf("baris ditolak %v, judul kolom seharusnya dilewati diam diam", ditolak)
	}
	// Nominal dibaca sebagai rupiah lalu dikonversi ke sen.
	if rows[0].GrossCents != 2500000 || rows[0].FeeCents != 70000 {
		t.Fatalf("konversi rupiah ke sen salah: %+v", rows[0])
	}
	if rows[1].GrossCents != 5000000 {
		t.Fatalf("konversi rupiah bulat salah: %+v", rows[1])
	}

	tanpaJudul := "ref-3,10000,200,2026-10-05\n"
	rows2, _, err := payment.ParseSettlementCSV(strings.NewReader(tanpaJudul))
	if err != nil {
		t.Fatalf("membaca CSV tanpa judul: %v", err)
	}
	if len(rows2) != 1 {
		t.Fatalf("baris terbaca %d, seharusnya 1", len(rows2))
	}
}

// TestBacaCSV_BarisRusakDilaporkan menjaga agar petugas keuangan tahu baris
// mana yang tidak terbaca, bukan hanya bahwa ada yang gagal.
func TestBacaCSV_BarisRusakDilaporkan(t *testing.T) {
	isi := `ref-1,25000,700,2026-10-05
ref-2,bukan-angka,700,2026-10-05
kurang,kolom
ref-4,10000,200,bukan-tanggal
`
	rows, ditolak, err := payment.ParseSettlementCSV(strings.NewReader(isi))
	if err != nil {
		t.Fatalf("membaca CSV: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("baris terbaca %d, seharusnya 1", len(rows))
	}
	if len(ditolak) != 3 {
		t.Fatalf("baris ditolak %d, seharusnya 3: %v", len(ditolak), ditolak)
	}
	for _, d := range ditolak {
		if !strings.Contains(d, "baris ke-") {
			t.Fatalf("laporan penolakan seharusnya menyebut nomor barisnya: %q", d)
		}
	}
}

// TestEkspor_BerbentukCSVDenganRupiah menjaga SRS-PAY-005: hasil rekonsiliasi
// dapat diekspor. Nominalnya rupiah, bukan sen, karena yang membacanya petugas
// keuangan dengan lembar kerja.
func TestEkspor_BerbentukCSVDenganRupiah(t *testing.T) {
	l := siapkan(t)
	ctx := context.Background()
	p := l.lunas(t)

	hasil, err := l.bayar.Reconcile(ctx, periode())
	if err != nil {
		t.Fatalf("menyusun laporan: %v", err)
	}

	var buf strings.Builder
	if err := hasil.ExportCSV(&buf); err != nil {
		t.Fatalf("mengekspor: %v", err)
	}
	isi := buf.String()

	if !strings.HasPrefix(isi, "nomor_pesanan,referensi_penyedia,") {
		t.Fatalf("judul kolom tidak sesuai: %q", strings.SplitN(isi, "\n", 2)[0])
	}
	if !strings.Contains(isi, p.ProviderRef) {
		t.Fatal("ekspor seharusnya memuat pembayaran uji")
	}
	// Nominal ditulis dalam rupiah berdesimal dua angka.
	rupiah := "25150.00"
	if p.AmountCents == 2515000 && !strings.Contains(isi, rupiah) {
		t.Fatalf("nominal seharusnya ditulis sebagai rupiah %s", rupiah)
	}
}
