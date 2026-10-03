package scheduling_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/scheduling"
)

func newDepots(t *testing.T) (*scheduling.Depots, *pgxpool.Pool) {
	t.Helper()
	pool := newPool(t)
	return scheduling.NewDepots(pool), pool
}

// isolasiDepot menonaktifkan seluruh depo yang sudah ada sebelumnya.
//
// Penentuan depo terdekat memilih di antara semua depo aktif, sehingga depo
// sisa dari berkas uji lain ikut bersaing dan membuat hasilnya tidak pasti.
// Menonaktifkan lebih dahulu membuat setiap uji berangkat dari keadaan yang
// sama tanpa perlu menghapus baris yang masih dirujuk tabel lain.
func isolasiDepot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE depots SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("mengisolasi depo: %v", err)
	}
}

func buatDepot(t *testing.T, d *scheduling.Depots, nama string, lat, lng, radius float64) *scheduling.Depot {
	t.Helper()
	dep, err := d.Create(context.Background(), scheduling.DepotInput{
		Code:            "UJI-" + uuid.NewString()[:8],
		Name:            nama,
		Latitude:        lat,
		Longitude:       lng,
		ServiceRadiusKm: radius,
	})
	if err != nil {
		t.Fatalf("membuat depo %s: %v", nama, err)
	}
	return dep
}

// Koordinat acuan di Manado.
const (
	manadoPusatLat = 1.4748
	manadoPusatLng = 124.8421
	manadoUtaraLat = 1.5210
	manadoUtaraLng = 124.8605
)

func TestDepot_BuatDanBaca(t *testing.T) {
	d, _ := newDepots(t)
	ctx := context.Background()

	dep := buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 8)
	got, err := d.Get(ctx, dep.ID)
	if err != nil {
		t.Fatalf("membaca depo: %v", err)
	}
	if got.Name != "Depo Pusat" || !got.IsActive {
		t.Fatalf("depo terbaca salah: %+v", got)
	}
}

func TestDepot_KodeTidakBolehGanda(t *testing.T) {
	d, _ := newDepots(t)
	ctx := context.Background()
	kode := "SAMA-" + uuid.NewString()[:8]

	in := scheduling.DepotInput{Code: kode, Name: "A",
		Latitude: manadoPusatLat, Longitude: manadoPusatLng, ServiceRadiusKm: 5}
	if _, err := d.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Name = "B"
	if _, err := d.Create(ctx, in); !errors.Is(err, scheduling.ErrDepotCodeExists) {
		t.Fatalf("galat = %v, seharusnya ErrDepotCodeExists", err)
	}
}

func TestDepot_KoordinatDiLuarRentangDitolak(t *testing.T) {
	d, _ := newDepots(t)
	ctx := context.Background()

	for _, k := range []struct {
		nama     string
		lat, lng float64
	}{
		{"lintang terlalu besar", 91, 124},
		{"lintang terlalu kecil", -91, 124},
		{"bujur terlalu besar", 1, 181},
		{"bujur terlalu kecil", 1, -181},
	} {
		_, err := d.Create(ctx, scheduling.DepotInput{
			Code: "X-" + uuid.NewString()[:8], Name: k.nama,
			Latitude: k.lat, Longitude: k.lng, ServiceRadiusKm: 5})
		if !errors.Is(err, scheduling.ErrCoordinateRange) {
			t.Errorf("%s: galat = %v, seharusnya ErrCoordinateRange", k.nama, err)
		}
	}
}

// Alamat di dalam radius dilayani depo terdekat.
func TestResolve_MemilihDepoTerdekat(t *testing.T) {
	d, pool := newDepots(t)
	ctx := context.Background()
	isolasiDepot(t, pool)

	pusat := buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 20)
	utara := buatDepot(t, d, "Depo Utara", manadoUtaraLat, manadoUtaraLng, 20)

	// Titik tepat di koordinat depo pusat.
	r, err := d.Resolve(ctx, manadoPusatLat, manadoPusatLng)
	if err != nil {
		t.Fatalf("menentukan depo: %v", err)
	}
	if r.Depot.ID != pusat.ID {
		t.Fatalf("depo = %s, seharusnya Depo Pusat", r.Depot.Name)
	}
	if r.DistanceKm > 0.01 {
		t.Fatalf("jarak = %.4f km, seharusnya mendekati nol", r.DistanceKm)
	}

	// Titik tepat di koordinat depo utara.
	r, err = d.Resolve(ctx, manadoUtaraLat, manadoUtaraLng)
	if err != nil {
		t.Fatal(err)
	}
	if r.Depot.ID != utara.ID {
		t.Fatalf("depo = %s, seharusnya Depo Utara", r.Depot.Name)
	}
}

// Jarak hitung haversine diperiksa terhadap jarak sebenarnya antara dua
// koordinat Manado, sekitar 5,3 km.
func TestResolve_JarakMasukAkal(t *testing.T) {
	d, pool := newDepots(t)
	ctx := context.Background()
	isolasiDepot(t, pool)
	buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 50)

	r, err := d.Resolve(ctx, manadoUtaraLat, manadoUtaraLng)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.DistanceKm-5.3) > 0.5 {
		t.Fatalf("jarak = %.2f km, seharusnya sekitar 5,3 km", r.DistanceKm)
	}
	t.Logf("jarak Manado Pusat ke Manado Utara terhitung %.2f km", r.DistanceKm)
}

// Alamat di luar jangkauan seluruh depo ditolak, bukan diarahkan paksa ke
// depo terjauh.
func TestResolve_DiLuarRadiusDitolak(t *testing.T) {
	d, pool := newDepots(t)
	ctx := context.Background()
	isolasiDepot(t, pool)
	buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 3)

	// Titik sekitar 5,3 km, di luar radius 3 km.
	_, err := d.Resolve(ctx, manadoUtaraLat, manadoUtaraLng)
	if !errors.Is(err, scheduling.ErrNoDepotServes) {
		t.Fatalf("galat = %v, seharusnya ErrNoDepotServes", err)
	}
}

func TestResolve_DepoNonaktifDiabaikan(t *testing.T) {
	d, pool := newDepots(t)
	ctx := context.Background()
	isolasiDepot(t, pool)
	dep := buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 20)

	nonaktif := false
	if _, err := d.Update(ctx, dep.ID, scheduling.DepotInput{
		Name: dep.Name, Latitude: dep.Latitude, Longitude: dep.Longitude,
		ServiceRadiusKm: dep.ServiceRadiusKm, IsActive: &nonaktif}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Resolve(ctx, manadoPusatLat, manadoPusatLng); !errors.Is(err, scheduling.ErrNoDepotServes) {
		t.Fatalf("galat = %v, depo nonaktif seharusnya diabaikan", err)
	}
}

// Perubahan radius tidak boleh membuat alamat berpindah depo diam diam pada
// pesanan yang sudah ada. Yang diuji di sini: perubahannya tercatat.
func TestDepot_PerubahanTercatatPadaJejakAudit(t *testing.T) {
	d, pool := newDepots(t)
	ctx := context.Background()
	dep := buatDepot(t, d, "Depo Pusat", manadoPusatLat, manadoPusatLng, 8)

	if _, err := d.Update(ctx, dep.ID, scheduling.DepotInput{
		Name: "Depo Pusat Baru", Latitude: dep.Latitude, Longitude: dep.Longitude,
		ServiceRadiusKm: 12}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_trail WHERE entity = 'depots' AND entity_id = $1`,
		dep.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 { // satu saat dibuat, satu saat diubah
		t.Fatalf("jejak = %d baris, seharusnya 2", n)
	}
}
