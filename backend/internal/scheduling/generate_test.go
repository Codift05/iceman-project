package scheduling_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/scheduling"
)

func TestGenerator_MembuatSlotSejumlahHariKaliJendela(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	hasil, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
		From: time.Now(), Days: 7, AreaID: &areaID,
	})
	if err != nil {
		t.Fatalf("membuat slot: %v", err)
	}
	mau := 7 * len(scheduling.DefaultTemplates)
	if hasil.Areas != 1 {
		t.Fatalf("area diproses %d, seharusnya 1", hasil.Areas)
	}
	if hasil.Created != mau {
		t.Fatalf("slot dibuat %d, seharusnya %d", hasil.Created, mau)
	}
}

// TestGenerator_Idempoten adalah alasan EnsureSlots memakai ON CONFLICT alih
// alih memeriksa lebih dahulu lalu menyisipkan. Job pembuat slot berjalan
// setiap hari dan dapat dicoba ulang setelah gagal di tengah, jadi menjalankan
// ulang tidak boleh menggandakan slot.
func TestGenerator_Idempoten(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	in := scheduling.GenerateInput{From: time.Now(), Days: 5, AreaID: &areaID}
	if _, err := g.EnsureSlots(ctx, in); err != nil {
		t.Fatalf("pembuatan pertama: %v", err)
	}
	kedua, err := g.EnsureSlots(ctx, in)
	if err != nil {
		t.Fatalf("pembuatan kedua: %v", err)
	}
	mau := 5 * len(scheduling.DefaultTemplates)
	if kedua.Created != 0 {
		t.Fatalf("pembuatan kedua membuat %d slot, seharusnya 0", kedua.Created)
	}
	if kedua.Skipped != mau {
		t.Fatalf("slot dilewati %d, seharusnya %d", kedua.Skipped, mau)
	}

	var jumlah int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM delivery_slots WHERE service_area_id = $1`, areaID).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung slot: %v", err)
	}
	if jumlah != mau {
		t.Fatalf("slot tersimpan %d, seharusnya %d", jumlah, mau)
	}
}

// TestGenerator_HanyaAreaYangDiminta menjaga agar penyaringan area bekerja,
// supaya admin dapat membuka satu area baru tanpa menyentuh area lain.
func TestGenerator_HanyaAreaYangDiminta(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()

	areaA := areaBaru(t, pool)
	areaB := areaBaru(t, pool)

	if _, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
		From: time.Now(), Days: 3, AreaID: &areaA,
	}); err != nil {
		t.Fatalf("membuat slot: %v", err)
	}

	var jumlahB int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM delivery_slots WHERE service_area_id = $1`, areaB).Scan(&jumlahB); err != nil {
		t.Fatalf("menghitung slot area lain: %v", err)
	}
	if jumlahB != 0 {
		t.Fatalf("area yang tidak diminta ikut terbuat %d slot", jumlahB)
	}
}

// TestGenerator_LewatiAreaDanDepoTidakAktif menjaga agar slot tidak dibuat
// untuk wilayah yang sudah berhenti dilayani. Slot semacam itu akan tampil
// sebagai pilihan bagi pelanggan padahal tidak ada yang mengantar.
func TestGenerator_LewatiAreaDanDepoTidakAktif(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()

	areaMati := areaBaru(t, pool)
	if _, err := pool.Exec(ctx,
		`UPDATE service_areas SET is_active = false WHERE id = $1`, areaMati); err != nil {
		t.Fatalf("menonaktifkan area: %v", err)
	}

	areaDepoMati := areaBaru(t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE depots SET is_active = false
		WHERE  id = (SELECT depot_id FROM service_areas WHERE id = $1)`, areaDepoMati); err != nil {
		t.Fatalf("menonaktifkan depo: %v", err)
	}

	for nama, id := range map[string]uuid.UUID{"area tidak aktif": areaMati, "depo tidak aktif": areaDepoMati} {
		hasil, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
			From: time.Now(), Days: 3, AreaID: &id,
		})
		if err != nil {
			t.Fatalf("%s: membuat slot: %v", nama, err)
		}
		if hasil.Areas != 0 || hasil.Created != 0 {
			t.Fatalf("%s: seharusnya dilewati, dapat %+v", nama, hasil)
		}
	}
}

// TestGenerator_BatasPesanDihitungMundurDariJendela menjaga aturan BR-004:
// batas pemesanan berjarak tetap sebelum jendela pengiriman dimulai, bukan
// dihitung dari saat slot dibuat.
func TestGenerator_BatasPesanDihitungMundurDariJendela(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	tpl := scheduling.Template{
		WindowStart: "13:00", WindowEnd: "16:00",
		Capacity: 10, CutoffBefore: 3 * time.Hour,
	}
	if _, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
		From: time.Now(), Days: 1, AreaID: &areaID,
		Templates: []scheduling.Template{tpl},
	}); err != nil {
		t.Fatalf("membuat slot: %v", err)
	}

	var tanggal, cutoff time.Time
	err := pool.QueryRow(ctx, `
		SELECT slot_date, cutoff_at FROM delivery_slots
		WHERE  service_area_id = $1`, areaID).Scan(&tanggal, &cutoff)
	if err != nil {
		t.Fatalf("membaca slot: %v", err)
	}

	mau := time.Date(tanggal.Year(), tanggal.Month(), tanggal.Day(), 10, 0, 0, 0, time.Local)
	if !cutoff.Equal(mau) {
		t.Fatalf("batas pesan %s, seharusnya %s", cutoff.Local(), mau)
	}
}

// TestGenerator_TanggalPertamaAdalahHariIniSetempat menjaga perhitungan awal
// hari di zona waktu setempat. Memotong waktu relatif UTC membuat tanggal slot
// bergeser sehari di Indonesia bagian tengah dan timur.
func TestGenerator_TanggalPertamaAdalahHariIniSetempat(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	// Sore hari, saat pemotongan relatif UTC paling mudah salah.
	n := time.Now()
	sore := time.Date(n.Year(), n.Month(), n.Day(), 21, 30, 0, 0, time.Local)

	if _, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
		From: sore, Days: 1, AreaID: &areaID,
		Templates: []scheduling.Template{{
			WindowStart: "08:00", WindowEnd: "11:00", Capacity: 5, CutoffBefore: time.Hour,
		}},
	}); err != nil {
		t.Fatalf("membuat slot: %v", err)
	}

	var tanggal time.Time
	if err := pool.QueryRow(ctx,
		`SELECT slot_date FROM delivery_slots WHERE service_area_id = $1`, areaID).Scan(&tanggal); err != nil {
		t.Fatalf("membaca slot: %v", err)
	}
	if tanggal.Format("2006-01-02") != sore.Format("2006-01-02") {
		t.Fatalf("tanggal slot %s, seharusnya %s",
			tanggal.Format("2006-01-02"), sore.Format("2006-01-02"))
	}
}

// TestGenerator_HariDiLuarBatasDikembalikanKeBawaan menjaga agar permintaan
// yang keliru, misalnya nol atau seribu hari, tidak membuat pekerja menyisipkan
// slot bertahun tahun ke depan.
func TestGenerator_HariDiLuarBatasDikembalikanKeBawaan(t *testing.T) {
	pool := newPool(t)
	g := scheduling.NewGenerator(pool)
	ctx := context.Background()

	for _, hari := range []int{0, -5, 1000} {
		areaID := areaBaru(t, pool)
		hasil, err := g.EnsureSlots(ctx, scheduling.GenerateInput{
			From: time.Now(), Days: hari, AreaID: &areaID,
			Templates: []scheduling.Template{{
				WindowStart: "08:00", WindowEnd: "11:00", Capacity: 5, CutoffBefore: time.Hour,
			}},
		})
		if err != nil {
			t.Fatalf("hari %d: membuat slot: %v", hari, err)
		}
		if hasil.Created != 30 {
			t.Fatalf("hari %d: slot dibuat %d, seharusnya kembali ke bawaan 30", hari, hasil.Created)
		}
	}
}
