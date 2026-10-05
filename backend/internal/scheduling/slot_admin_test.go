package scheduling_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iceman/backend/internal/scheduling"
)

// areaBaru membuat satu depo dan satu area kosong tanpa slot.
func areaBaru(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var depotID, areaID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO depots (code, name, latitude, longitude, service_radius_km)
		VALUES ($1, 'Depo Slot', 1.4748, 124.8421, 8)
		RETURNING id`, "UJI-"+suffix).Scan(&depotID)
	if err != nil {
		t.Fatalf("menyiapkan depo: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO service_areas (depot_id, name, delivery_fee_cents)
		VALUES ($1, $2, 10000)
		RETURNING id`, depotID, "Area "+suffix).Scan(&areaID)
	if err != nil {
		t.Fatalf("menyiapkan area: %v", err)
	}
	return areaID
}

func besok() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
}

func TestSlot_BuatDanTolakGanda(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	in := scheduling.SlotInput{
		ServiceAreaID: areaID, Date: besok(),
		WindowStart: "08:00", WindowEnd: "11:00",
		Capacity: 20, CutoffAt: future(),
	}
	slot, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("membuat slot: %v", err)
	}
	if slot.Capacity != 20 || slot.Used != 0 {
		t.Fatalf("slot baru salah: %+v", slot)
	}

	// Jendela yang sama pada tanggal yang sama harus ditolak (DB-09). Tanpa
	// kekangan ini, dua slot kembar membuat kuota harian berlipat dua.
	if _, err := s.Create(ctx, in); err == nil {
		t.Fatal("slot ganda pada jendela yang sama seharusnya ditolak")
	}
}

func TestSlot_TolakKapasitasNegatif(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	areaID := areaBaru(t, pool)

	_, err := s.Create(context.Background(), scheduling.SlotInput{
		ServiceAreaID: areaID, Date: besok(),
		WindowStart: "08:00", WindowEnd: "11:00",
		Capacity: -1, CutoffAt: future(),
	})
	if err == nil {
		t.Fatal("kapasitas negatif seharusnya ditolak")
	}
}

func TestSlot_DaftarUntukArea(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	for _, w := range [][2]string{{"08:00", "11:00"}, {"11:00", "14:00"}, {"14:00", "17:00"}} {
		if _, err := s.Create(ctx, scheduling.SlotInput{
			ServiceAreaID: areaID, Date: besok(),
			WindowStart: w[0], WindowEnd: w[1],
			Capacity: 10, CutoffAt: future(),
		}); err != nil {
			t.Fatalf("membuat slot %s: %v", w[0], err)
		}
	}

	daftar, err := s.ListForArea(ctx, areaID, besok(), 2)
	if err != nil {
		t.Fatalf("membaca daftar slot: %v", err)
	}
	if len(daftar) != 3 {
		t.Fatalf("slot terbaca %d, seharusnya 3", len(daftar))
	}
	if daftar[0].WindowStart != "08:00" {
		t.Fatalf("urutan salah, slot pertama %s", daftar[0].WindowStart)
	}
}

// TestSlot_AlasanTidakBisaDipilih menjaga janji UI/UX Bab 10.1: slot yang tidak
// dapat dipilih tetap ditampilkan beserta alasannya, bukan disembunyikan.
// Menyembunyikannya membuat pelanggan mengira layanan tidak tersedia hari itu.
func TestSlot_AlasanTidakBisaDipilih(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()

	kasus := []struct {
		nama      string
		kapasitas int32
		terpakai  int32
		cutoff    time.Time
		libur     bool
		mau       string
		bisa      bool
	}{
		{"kuota tersisa", 10, 3, future(), false, "", true},
		{"kuota penuh", 5, 5, future(), false, scheduling.ReasonFull, false},
		{"batas pesan terlewat", 10, 0, past(), false, scheduling.ReasonCutoffPassed, false},
		{"hari libur", 10, 0, future(), true, scheduling.ReasonHoliday, false},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			slotID := seedSlot(t, pool, k.kapasitas, k.terpakai, k.cutoff, k.libur)

			var areaID uuid.UUID
			if err := pool.QueryRow(ctx,
				`SELECT service_area_id FROM delivery_slots WHERE id = $1`, slotID).Scan(&areaID); err != nil {
				t.Fatalf("membaca area slot: %v", err)
			}

			daftar, err := s.Availability(ctx, areaID, besok(), 2, time.Now())
			if err != nil {
				t.Fatalf("membaca ketersediaan: %v", err)
			}
			if len(daftar) != 1 {
				t.Fatalf("ketersediaan terbaca %d, seharusnya 1", len(daftar))
			}
			got := daftar[0]
			if got.Selectable != k.bisa {
				t.Fatalf("dapat dipilih %v, seharusnya %v", got.Selectable, k.bisa)
			}
			if got.Reason != k.mau {
				t.Fatalf("alasan %q, seharusnya %q", got.Reason, k.mau)
			}
			if k.bisa {
				if got.Remaining == nil || *got.Remaining != k.kapasitas-k.terpakai {
					t.Fatalf("sisa kuota salah: %+v", got.Remaining)
				}
			} else if got.Remaining != nil {
				t.Fatalf("slot tidak terpilih seharusnya tanpa sisa kuota, dapat %d", *got.Remaining)
			}
		})
	}
}

// TestSlot_HariLiburMembatalkanPilihan menguji urutan pemeriksaan: hari libur
// menang atas kuota yang masih tersisa.
func TestSlot_HariLiburMembatalkanPilihan(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()

	slotID := seedSlot(t, pool, 10, 0, future(), false)
	var areaID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT service_area_id FROM delivery_slots WHERE id = $1`, slotID).Scan(&areaID); err != nil {
		t.Fatalf("membaca area slot: %v", err)
	}

	if err := s.SetHoliday(ctx, slotID, true); err != nil {
		t.Fatalf("menandai hari libur: %v", err)
	}
	daftar, err := s.Availability(ctx, areaID, besok(), 2, time.Now())
	if err != nil {
		t.Fatalf("membaca ketersediaan: %v", err)
	}
	if daftar[0].Selectable || daftar[0].Reason != scheduling.ReasonHoliday {
		t.Fatalf("slot libur seharusnya tertutup, dapat %+v", daftar[0])
	}

	// Pembatalan hari libur harus memulihkan slot, bukan menyisakannya tertutup.
	if err := s.SetHoliday(ctx, slotID, false); err != nil {
		t.Fatalf("membatalkan hari libur: %v", err)
	}
	daftar, err = s.Availability(ctx, areaID, besok(), 2, time.Now())
	if err != nil {
		t.Fatalf("membaca ketersediaan: %v", err)
	}
	if !daftar[0].Selectable {
		t.Fatalf("slot seharusnya kembali terbuka, dapat %+v", daftar[0])
	}
}

// TestSlot_BerikutnyaYangTersedia menguji jalan keluar yang ditawarkan ketika
// pilihan pelanggan ditolak: slot penuh dan slot libur dilewati.
func TestSlot_BerikutnyaYangTersedia(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	buat := func(jamMulai, jamAkhir string, kapasitas, terpakai int32, libur bool) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO delivery_slots
			       (service_area_id, slot_date, window_start, window_end,
			        capacity, used, cutoff_at, is_holiday)
			VALUES ($1, current_date + 1, $2, $3, $4, $5, $6, $7)`,
			areaID, jamMulai, jamAkhir, kapasitas, terpakai, future(), libur)
		if err != nil {
			t.Fatalf("menyiapkan slot %s: %v", jamMulai, err)
		}
	}
	buat("08:00", "11:00", 5, 5, false) // penuh
	buat("11:00", "14:00", 5, 0, true)  // libur
	buat("14:00", "17:00", 5, 2, false) // tersedia

	got, err := s.NextAvailable(ctx, areaID, time.Now())
	if err != nil {
		t.Fatalf("mencari slot berikutnya: %v", err)
	}
	if got == nil {
		t.Fatal("seharusnya ada slot tersedia")
	}
	if got.WindowStart != "14:00" {
		t.Fatalf("slot berikutnya %s, seharusnya 14:00", got.WindowStart)
	}
}

func TestSlot_BerikutnyaKosongSaatSemuaTertutup(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO delivery_slots
		       (service_area_id, slot_date, window_start, window_end, capacity, used, cutoff_at)
		VALUES ($1, current_date + 1, '08:00', '11:00', 5, 5, $2)`, areaID, future())
	if err != nil {
		t.Fatalf("menyiapkan slot: %v", err)
	}

	got, err := s.NextAvailable(ctx, areaID, time.Now())
	if err != nil {
		t.Fatalf("mencari slot berikutnya: %v", err)
	}
	if got != nil {
		t.Fatalf("tidak seharusnya ada slot tersedia, dapat %+v", got)
	}
}

// TestSlotDate_TerbitSebagaiTanggalSaja menjaga agar tanggal slot tidak terbit
// sebagai cap waktu UTC. Klien yang menerima "2026-10-04T00:00:00Z" akan
// menggesernya ke zona waktu setempat dan menampilkannya pada hari yang salah.
func TestSlotDate_TerbitSebagaiTanggalSaja(t *testing.T) {
	pool := newPool(t)
	s := scheduling.NewSlots(pool)
	ctx := context.Background()
	areaID := areaBaru(t, pool)

	slot, err := s.Create(ctx, scheduling.SlotInput{
		ServiceAreaID: areaID, Date: besok(),
		WindowStart: "08:00", WindowEnd: "11:00",
		Capacity: 10, CutoffAt: future(),
	})
	if err != nil {
		t.Fatalf("membuat slot: %v", err)
	}

	raw, err := json.Marshal(slot)
	if err != nil {
		t.Fatalf("mengubah ke JSON: %v", err)
	}
	mau := `"date":"` + besok().Format("2006-01-02") + `"`
	if !strings.Contains(string(raw), mau) {
		t.Fatalf("JSON slot tidak memuat %s:\n%s", mau, raw)
	}
}
