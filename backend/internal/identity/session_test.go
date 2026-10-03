package identity_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iceman/backend/internal/identity"
)

func login(t *testing.T, svc *identity.Service, email, pass string) *identity.Tokens {
	t.Helper()
	ctx := context.Background()
	u, err := svc.Authenticate(ctx, email, pass)
	if err != nil {
		t.Fatalf("masuk: %v", err)
	}
	tok, err := svc.IssueTokens(ctx, u, "uji")
	if err != nil {
		t.Fatalf("menerbitkan token: %v", err)
	}
	return tok
}

func TestRefresh_MemutarToken(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	first := login(t, svc, email, "rahasia-yang-panjang")
	second, err := svc.Refresh(ctx, first.Refresh, "uji")
	if err != nil {
		t.Fatalf("penyegaran gagal: %v", err)
	}
	if second.Refresh == first.Refresh {
		t.Fatal("token penyegar seharusnya berganti setiap dipakai")
	}
	if second.Access == "" {
		t.Fatal("token akses baru kosong")
	}

	// Token baru masih dapat dipakai.
	if _, err := svc.Refresh(ctx, second.Refresh, "uji"); err != nil {
		t.Fatalf("token baru seharusnya sah: %v", err)
	}
}

// TC-AUT-06: memakai ulang token penyegar lama membatalkan seluruh sesi
// pengguna, bukan hanya sesi yang bersangkutan (SRS-AUT-003).
func TestRefresh_PemakaianUlangMembatalkanSeluruhSesi(t *testing.T) {
	svc, pool := newService(t)
	userID, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	// Pengguna masuk dari dua perangkat.
	perangkatA := login(t, svc, email, "rahasia-yang-panjang")
	perangkatB := login(t, svc, email, "rahasia-yang-panjang")

	aktif, err := svc.ActiveSessions(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if aktif != 2 {
		t.Fatalf("sesi aktif = %d, seharusnya 2", aktif)
	}

	// Perangkat A menyegarkan tokennya secara wajar.
	rotasiA, err := svc.Refresh(ctx, perangkatA.Refresh, "uji")
	if err != nil {
		t.Fatalf("penyegaran wajar gagal: %v", err)
	}

	// Lalu token lama perangkat A dipakai lagi. Ini tanda token tercuri.
	_, err = svc.Refresh(ctx, perangkatA.Refresh, "penyusup")
	if !errors.Is(err, identity.ErrRefreshReused) {
		t.Fatalf("galat = %v, seharusnya ErrRefreshReused", err)
	}

	// Seluruh sesi pengguna dibatalkan, termasuk perangkat B yang tidak bersalah
	// dan token hasil rotasi yang baru saja terbit.
	if aktif, err := svc.ActiveSessions(ctx, userID); err != nil || aktif != 0 {
		t.Fatalf("sesi aktif = %d err=%v, seharusnya 0", aktif, err)
	}
	for nama, tok := range map[string]string{
		"hasil rotasi A": rotasiA.Refresh,
		"perangkat B":    perangkatB.Refresh,
	} {
		if _, err := svc.Refresh(ctx, tok, "uji"); err == nil {
			t.Fatalf("%s seharusnya sudah tidak berlaku", nama)
		}
	}
}

// Dua permintaan penyegaran bersamaan dengan token yang sama hanya boleh
// menghasilkan satu pasangan token baru. Tanpa kunci baris, keduanya dapat
// dianggap sah dan menerbitkan dua sesi.
func TestRefresh_DuaPermintaanBersamaan(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	tok := login(t, svc, email, "rahasia-yang-panjang")

	const penyerbu = 8
	var berhasil, dipakaiUlang int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch _, err := svc.Refresh(ctx, tok.Refresh, "uji"); {
			case err == nil:
				atomic.AddInt32(&berhasil, 1)
			case errors.Is(err, identity.ErrRefreshReused):
				atomic.AddInt32(&dipakaiUlang, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if berhasil != 1 {
		t.Fatalf("berhasil = %d, seharusnya tepat 1", berhasil)
	}
	t.Logf("%d permintaan bersamaan: %d berhasil, %d terdeteksi pemakaian ulang",
		penyerbu, berhasil, dipakaiUlang)
}

func TestRefresh_TokenTidakDikenal(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.Refresh(context.Background(), "token-karangan", "uji")
	if !errors.Is(err, identity.ErrRefreshInvalid) {
		t.Fatalf("galat = %v, seharusnya ErrRefreshInvalid", err)
	}
}

func TestLogout_MembatalkanSesi(t *testing.T) {
	svc, pool := newService(t)
	_, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")
	ctx := context.Background()

	tok := login(t, svc, email, "rahasia-yang-panjang")
	if err := svc.Logout(ctx, tok.Refresh); err != nil {
		t.Fatalf("keluar gagal: %v", err)
	}
	if _, err := svc.Refresh(ctx, tok.Refresh, "uji"); err == nil {
		t.Fatal("token setelah keluar seharusnya tidak berlaku")
	}
}

func TestToken_AksesDapatDiperiksa(t *testing.T) {
	svc, pool := newService(t)
	userID, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")

	tok := login(t, svc, email, "rahasia-yang-panjang")
	signer := identity.NewSigner([]byte("kunci-uji-jangan-dipakai-produksi"))

	claims, err := signer.ParseAccess(tok.Access)
	if err != nil {
		t.Fatalf("memeriksa token: %v", err)
	}
	if claims.UserID != userID {
		t.Fatalf("UserID = %s, seharusnya %s", claims.UserID, userID)
	}
	if claims.Role != "ADMIN_OPS" {
		t.Fatalf("Role = %s, seharusnya ADMIN_OPS", claims.Role)
	}

	// Token yang ditandatangani kunci lain harus ditolak.
	lain := identity.NewSigner([]byte("kunci-yang-berbeda-sama-sekali"))
	if _, err := lain.ParseAccess(tok.Access); err == nil {
		t.Fatal("token dengan kunci berbeda seharusnya ditolak")
	}
	_ = svc
}
