package identity_test

import (
	"context"
	"testing"
)

// Matriks ini menyalin PRD Bab 16. Bila seed peran diubah tanpa memperbarui
// PRD, atau sebaliknya, uji ini yang menangkapnya.
func TestIzin_SesuaiMatriksPRD(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()

	kasus := []struct {
		peran string
		izin  string
		boleh bool
	}{
		// Super Admin memegang seluruh izin.
		{"SUPER_ADMIN", "user.manage", true},
		{"SUPER_ADMIN", "role.manage", true},
		{"SUPER_ADMIN", "audit.view", true},
		{"SUPER_ADMIN", "payment.manage", true},
		{"SUPER_ADMIN", "settings.manage", true},

		// Admin Operasional menjalankan operasional harian.
		{"ADMIN_OPS", "order.manage", true},
		{"ADMIN_OPS", "order.create_manual", true},
		{"ADMIN_OPS", "area_slot.manage", true},
		{"ADMIN_OPS", "dispatch.manage", true},
		{"ADMIN_OPS", "tracking.view", true},
		{"ADMIN_OPS", "promo.manage", true},
		{"ADMIN_OPS", "refund.request", true},
		// namun tidak boleh memproses refund maupun mengelola pengguna.
		{"ADMIN_OPS", "refund.process", false},
		{"ADMIN_OPS", "payment.manage", false},
		{"ADMIN_OPS", "user.manage", false},
		{"ADMIN_OPS", "role.manage", false},
		{"ADMIN_OPS", "settings.manage", false},
		{"ADMIN_OPS", "customer.manage_terms", false},

		// Keuangan memegang pembayaran dan refund.
		{"FINANCE", "payment.manage", true},
		{"FINANCE", "refund.process", true},
		{"FINANCE", "customer.view_finance", true},
		{"FINANCE", "report.view_financial", true},
		// namun tidak menyentuh master data operasional.
		{"FINANCE", "product.manage", false},
		{"FINANCE", "area_slot.manage", false},
		{"FINANCE", "order.manage", false},
		{"FINANCE", "dispatch.manage", false},
		{"FINANCE", "user.manage", false},

		// Manajemen hanya membaca.
		{"MANAGEMENT", "report.view_summary", true},
		{"MANAGEMENT", "order.view", true},
		{"MANAGEMENT", "audit.view", true},
		{"MANAGEMENT", "order.manage", false},
		{"MANAGEMENT", "product.manage", false},
		{"MANAGEMENT", "payment.manage", false},
		{"MANAGEMENT", "report.export", false},

		// Driver hanya tugasnya sendiri.
		{"DRIVER", "delivery.view_own", true},
		{"DRIVER", "delivery.update_own", true},
		{"DRIVER", "order.view", false},
		{"DRIVER", "customer.view", false},
		{"DRIVER", "tracking.view", false},
		{"DRIVER", "report.view_operational", false},
	}

	for _, k := range kasus {
		rid := roleID(t, pool, k.peran)
		got, err := svc.Can(ctx, rid, k.izin)
		if err != nil {
			t.Fatalf("memeriksa izin: %v", err)
		}
		if got != k.boleh {
			t.Errorf("%s terhadap %s = %v, seharusnya %v", k.peran, k.izin, got, k.boleh)
		}
	}
}

// Peran yang hanya membaca tidak boleh memiliki satupun izin yang mengubah data.
func TestIzin_ManajemenTidakPunyaIzinMengubah(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()

	codes, err := svc.Permissions(ctx, roleID(t, pool, "MANAGEMENT"))
	if err != nil {
		t.Fatal(err)
	}
	terlarang := []string{"manage", "create", "update", "delete", "process", "approve", "export"}
	for _, c := range codes {
		for _, kata := range terlarang {
			if len(c) >= len(kata) && c[len(c)-len(kata):] == kata {
				t.Errorf("peran Manajemen memegang izin mengubah: %s", c)
			}
		}
	}
	t.Logf("Manajemen memegang %d izin, seluruhnya hanya membaca", len(codes))
}

// Driver adalah peran paling sempit. Jumlah izinnya dikunci agar penambahan
// tidak terjadi tanpa disadari.
func TestIzin_DriverPalingSempit(t *testing.T) {
	svc, pool := newService(t)
	codes, err := svc.Permissions(context.Background(), roleID(t, pool, "DRIVER"))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 {
		t.Fatalf("Driver memegang %d izin (%v), seharusnya tepat 2", len(codes), codes)
	}
}
