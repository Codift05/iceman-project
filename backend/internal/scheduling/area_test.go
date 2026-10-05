package scheduling_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iceman/backend/internal/scheduling"
)

func buatArea(t *testing.T, a *scheduling.Areas, depotID uuid.UUID, nama string, ongkos int64) *scheduling.ServiceArea {
	t.Helper()
	area, err := a.Create(context.Background(), scheduling.AreaInput{
		DepotID: depotID, Name: nama, DeliveryFeeCents: ongkos,
	})
	if err != nil {
		t.Fatalf("membuat area %s: %v", nama, err)
	}
	return area
}

func TestArea_BuatDanDaftar(t *testing.T) {
	pool := newPool(t)
	d := scheduling.NewDepots(pool)
	a := scheduling.NewAreas(pool)
	ctx := context.Background()

	dep := buatDepot(t, d, "Depo Area", manadoPusatLat, manadoPusatLng, 8)
	buatArea(t, a, dep.ID, "Malalayang "+uuid.NewString()[:6], 15000)
	buatArea(t, a, dep.ID, "Winangun "+uuid.NewString()[:6], 20000)

	daftar, err := a.List(ctx, &dep.ID, true)
	if err != nil {
		t.Fatalf("membaca daftar area: %v", err)
	}
	if len(daftar) != 2 {
		t.Fatalf("area pada depo ini seharusnya 2, dapat %d", len(daftar))
	}
}

// TestArea_NamaGandaDitolakPerDepo menjaga agar kekangan keunikan berlaku per
// depo, bukan menyeluruh. Dua depo boleh punya area bernama sama, misalnya
// "Pusat Kota", karena keduanya melayani wilayah yang berbeda.
func TestArea_NamaGandaDitolakPerDepo(t *testing.T) {
	pool := newPool(t)
	d := scheduling.NewDepots(pool)
	a := scheduling.NewAreas(pool)
	ctx := context.Background()

	nama := "Pusat Kota " + uuid.NewString()[:6]
	dep1 := buatDepot(t, d, "Depo Satu", manadoPusatLat, manadoPusatLng, 8)
	dep2 := buatDepot(t, d, "Depo Dua", manadoUtaraLat, manadoUtaraLng, 8)

	buatArea(t, a, dep1.ID, nama, 15000)

	_, err := a.Create(ctx, scheduling.AreaInput{DepotID: dep1.ID, Name: nama, DeliveryFeeCents: 15000})
	if !errors.Is(err, scheduling.ErrAreaNameExists) {
		t.Fatalf("nama ganda pada depo yang sama seharusnya ditolak, dapat %v", err)
	}

	if _, err := a.Create(ctx, scheduling.AreaInput{DepotID: dep2.ID, Name: nama, DeliveryFeeCents: 15000}); err != nil {
		t.Fatalf("nama sama pada depo lain seharusnya diterima, dapat %v", err)
	}
}

// TestArea_TolakDepoTidakAktif menjaga janji di area.go: area tidak boleh
// bergantung pada depo yang sudah berhenti melayani, karena alamat pelanggan di
// area itu akan kehilangan depo pelayannya.
func TestArea_TolakDepoTidakAktif(t *testing.T) {
	pool := newPool(t)
	d := scheduling.NewDepots(pool)
	a := scheduling.NewAreas(pool)
	ctx := context.Background()

	dep := buatDepot(t, d, "Depo Tutup", manadoPusatLat, manadoPusatLng, 8)
	mati := false
	_, err := d.Update(ctx, dep.ID, scheduling.DepotInput{
		Code: dep.Code, Name: dep.Name, Latitude: dep.Latitude, Longitude: dep.Longitude,
		ServiceRadiusKm: dep.ServiceRadiusKm, IsActive: &mati,
	})
	if err != nil {
		t.Fatalf("menonaktifkan depo: %v", err)
	}

	_, err = a.Create(ctx, scheduling.AreaInput{
		DepotID: dep.ID, Name: "Area Yatim " + uuid.NewString()[:6], DeliveryFeeCents: 10000,
	})
	if !errors.Is(err, scheduling.ErrAreaDepotOff) {
		t.Fatalf("area pada depo tidak aktif seharusnya ditolak, dapat %v", err)
	}
}

func TestArea_TolakOngkosNegatif(t *testing.T) {
	pool := newPool(t)
	d := scheduling.NewDepots(pool)
	a := scheduling.NewAreas(pool)
	ctx := context.Background()

	dep := buatDepot(t, d, "Depo Ongkos", manadoPusatLat, manadoPusatLng, 8)
	_, err := a.Create(ctx, scheduling.AreaInput{
		DepotID: dep.ID, Name: "Area " + uuid.NewString()[:6], DeliveryFeeCents: -1,
	})
	if !errors.Is(err, scheduling.ErrFeeNegative) {
		t.Fatalf("ongkos negatif seharusnya ditolak, dapat %v", err)
	}
}

func TestArea_UbahOngkosTercatat(t *testing.T) {
	pool := newPool(t)
	d := scheduling.NewDepots(pool)
	a := scheduling.NewAreas(pool)
	ctx := context.Background()

	dep := buatDepot(t, d, "Depo Ubah", manadoPusatLat, manadoPusatLng, 8)
	area := buatArea(t, a, dep.ID, "Area "+uuid.NewString()[:6], 15000)

	after, err := a.Update(ctx, area.ID, scheduling.AreaInput{DeliveryFeeCents: 18000})
	if err != nil {
		t.Fatalf("mengubah area: %v", err)
	}
	if after.DeliveryFeeCents != 18000 {
		t.Fatalf("ongkos sesudah ubah %d, seharusnya 18000", after.DeliveryFeeCents)
	}
	// Nama tidak dikirim, jadi nilai lamanya harus dipertahankan.
	if after.Name != area.Name {
		t.Fatalf("nama berubah menjadi %q padahal tidak dikirim", after.Name)
	}

	var jumlah int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_trail
		WHERE  entity = 'service_areas' AND entity_id = $1 AND action = 'UPDATE'`,
		area.ID).Scan(&jumlah)
	if err != nil {
		t.Fatalf("membaca jejak audit: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("jejak audit perubahan ongkos %d, seharusnya 1", jumlah)
	}
}

func TestArea_UbahAreaTidakAda(t *testing.T) {
	pool := newPool(t)
	a := scheduling.NewAreas(pool)
	_, err := a.Update(context.Background(), uuid.New(), scheduling.AreaInput{DeliveryFeeCents: 1000})
	if !errors.Is(err, scheduling.ErrAreaNotFound) {
		t.Fatalf("area tidak ada seharusnya ErrAreaNotFound, dapat %v", err)
	}
}
