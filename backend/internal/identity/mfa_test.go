package identity_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/iceman/backend/internal/identity"
)

// kode membangkitkan kode TOTP yang berlaku saat ini dari sebuah rahasia,
// meniru apa yang ditampilkan aplikasi autentikator di ponsel.
// langkahBerikutnya mengembalikan awal langkah TOTP sesudah langkah yang
// sedang berjalan.
//
// Dihitung dari batas langkah, bukan dengan menambahkan tiga puluh satu detik
// pada waktu sekarang. Penambahan sederhana itu melompat dua langkah bila
// waktu sekarang sedang berada di detik terakhir sebuah langkah, dan kode dua
// langkah ke depan berada di luar toleransi satu langkah yang diterima server.
// Akibatnya uji gagal kira kira satu dari tiga puluh kali dijalankan, yang
// jauh lebih merugikan daripada bug yang dicarinya.
func langkahBerikutnya(t time.Time) time.Time {
	const langkah = 30
	return time.Unix((t.Unix()/langkah+1)*langkah, 0)
}

func kode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{Period: 30, Digits: 6})
	if err != nil {
		t.Fatalf("membangkitkan kode: %v", err)
	}
	return c
}

// Alur lengkap: peran berisiko tinggi mendaftarkan faktor kedua, lalu masuk
// dengan kode. Sebelum ini, Super Admin dan Keuangan tidak dapat masuk sama
// sekali karena jalurnya sengaja ditutup.
func TestMFA_AlurLengkapSuperAdmin(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, email := seedUser(t, pool, "SUPER_ADMIN", "rahasia-yang-panjang")

	// Tahap satu: kata sandi benar, namun diminta mendaftar lebih dahulu.
	_, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang")
	if !errors.Is(err, identity.ErrMFAEnrollRequired) {
		t.Fatalf("galat = %v, seharusnya ErrMFAEnrollRequired", err)
	}

	// Tahap dua: mendaftar dan membuktikan satu kode.
	enr, err := svc.BeginMFAEnrollment(ctx, userID)
	if err != nil {
		t.Fatalf("memulai pendaftaran: %v", err)
	}
	if enr.Secret == "" || enr.URI == "" {
		t.Fatal("rahasia dan URI pendaftaran tidak boleh kosong")
	}
	if err := svc.ConfirmMFAEnrollment(ctx, userID, kode(t, enr.Secret, time.Now())); err != nil {
		t.Fatalf("mengaktifkan faktor kedua: %v", err)
	}

	// Tahap tiga: masuk berikutnya meminta kode, bukan pendaftaran.
	_, err = svc.Authenticate(ctx, email, "rahasia-yang-panjang")
	if !errors.Is(err, identity.ErrMFARequired) {
		t.Fatalf("galat = %v, seharusnya ErrMFARequired", err)
	}

	// Tahap empat: kode dari langkah berikutnya membuka jalan. Kode yang tadi
	// dipakai mendaftar sudah hangus, itu memang perilaku yang diinginkan.
	if err := svc.VerifyMFA(ctx, userID, kode(t, enr.Secret, langkahBerikutnya(time.Now()))); err != nil {
		t.Fatalf("verifikasi kode: %v", err)
	}
}

func TestMFA_KodeSalahDitolak(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, _ := seedUser(t, pool, "FINANCE", "rahasia-yang-panjang")

	enr, err := svc.BeginMFAEnrollment(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmMFAEnrollment(ctx, userID, kode(t, enr.Secret, time.Now())); err != nil {
		t.Fatal(err)
	}

	if err := svc.VerifyMFA(ctx, userID, "000000"); !errors.Is(err, identity.ErrMFACodeInvalid) {
		t.Fatalf("galat = %v, seharusnya ErrMFACodeInvalid", err)
	}
}

// Satu kode hanya berlaku sekali. Tanpa penjagaan ini, kode yang sempat
// terlihat orang lain masih dapat dipakai selama tiga puluh detik yang sama.
func TestMFA_KodeTidakDapatDipakaiDuaKali(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, _ := seedUser(t, pool, "FINANCE", "rahasia-yang-panjang")

	enr, _ := svc.BeginMFAEnrollment(ctx, userID)
	c := kode(t, enr.Secret, time.Now())
	if err := svc.ConfirmMFAEnrollment(ctx, userID, c); err != nil {
		t.Fatal(err)
	}

	// Kode yang sama dipakai lagi untuk masuk.
	if err := svc.VerifyMFA(ctx, userID, c); !errors.Is(err, identity.ErrMFACodeReplayed) {
		t.Fatalf("galat = %v, seharusnya ErrMFACodeReplayed", err)
	}
}

// Dua permintaan bersamaan dengan kode yang sama hanya boleh lolos satu.
func TestMFA_DuaVerifikasiBersamaanKodeSama(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, _ := seedUser(t, pool, "FINANCE", "rahasia-yang-panjang")

	enr, _ := svc.BeginMFAEnrollment(ctx, userID)
	// Aktifkan memakai kode langkah sebelumnya agar kode sekarang masih segar.
	if err := svc.ConfirmMFAEnrollment(ctx, userID,
		kode(t, enr.Secret, time.Now().Add(-30*time.Second))); err != nil {
		t.Fatal(err)
	}

	c := kode(t, enr.Secret, time.Now())
	const penyerbu = 8
	var lolos, ulang int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < penyerbu; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := svc.VerifyMFA(ctx, userID, c); {
			case err == nil:
				atomic.AddInt32(&lolos, 1)
			case errors.Is(err, identity.ErrMFACodeReplayed):
				atomic.AddInt32(&ulang, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if lolos != 1 {
		t.Fatalf("lolos = %d, seharusnya tepat 1", lolos)
	}
	t.Logf("%d verifikasi bersamaan dengan kode sama: %d lolos, %d ditolak sebagai pemakaian ulang",
		penyerbu, lolos, ulang)
}

func TestMFA_BelumDidaftarkan(t *testing.T) {
	svc, pool := newService(t)
	userID, _ := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")

	err := svc.VerifyMFA(context.Background(), userID, "123456")
	if !errors.Is(err, identity.ErrMFANotEnrolled) {
		t.Fatalf("galat = %v, seharusnya ErrMFANotEnrolled", err)
	}
}

func TestMFA_TidakDapatDidaftarkanDuaKali(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, _ := seedUser(t, pool, "FINANCE", "rahasia-yang-panjang")

	enr, _ := svc.BeginMFAEnrollment(ctx, userID)
	if err := svc.ConfirmMFAEnrollment(ctx, userID, kode(t, enr.Secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BeginMFAEnrollment(ctx, userID); !errors.Is(err, identity.ErrMFAAlreadyOn) {
		t.Fatalf("galat = %v, seharusnya ErrMFAAlreadyOn", err)
	}
}

// Peran biasa tetap dapat mengaktifkan faktor kedua secara sukarela, dan
// setelah itu wajib memakainya.
func TestMFA_PeranBiasaDapatMengaktifkanSendiri(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID, email := seedUser(t, pool, "ADMIN_OPS", "rahasia-yang-panjang")

	if _, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang"); err != nil {
		t.Fatalf("sebelum mengaktifkan seharusnya langsung masuk: %v", err)
	}

	enr, _ := svc.BeginMFAEnrollment(ctx, userID)
	if err := svc.ConfirmMFAEnrollment(ctx, userID, kode(t, enr.Secret, time.Now())); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Authenticate(ctx, email, "rahasia-yang-panjang")
	if !errors.Is(err, identity.ErrMFARequired) {
		t.Fatalf("setelah diaktifkan = %v, seharusnya ErrMFARequired", err)
	}
}

// Token tantangan tidak boleh dapat dipakai sebagai token akses, dan tujuannya
// tidak boleh dapat ditukar.
func TestMFA_TokenTantanganTerkunciTujuannya(t *testing.T) {
	svc, pool := newService(t)
	userID, _ := seedUser(t, pool, "FINANCE", "rahasia-yang-panjang")

	ch, err := svc.Challenge(userID, identity.PurposeMFAVerify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ParseChallenge(ch, identity.PurposeMFAEnroll); err == nil {
		t.Fatal("token tantangan verifikasi seharusnya ditolak untuk tujuan pendaftaran")
	}
	if _, err := svc.ParseChallenge(ch, identity.PurposeMFAVerify); err != nil {
		t.Fatalf("tujuan yang benar seharusnya diterima: %v", err)
	}

	signer := identity.NewSigner([]byte("kunci-uji-jangan-dipakai-produksi"))
	if _, err := signer.ParseAccess(ch); err == nil {
		t.Fatal("token tantangan seharusnya tidak lolos sebagai token akses")
	}
}

// TestLangkahBerikutnya_SelaluTepatSatuLangkah membuktikan perbaikan flake
// secara menyeluruh, bukan dengan menjalankan uji berkali kali dan berharap.
//
// Seluruh tiga puluh posisi detik di dalam satu langkah diperiksa. Cara lama,
// menambahkan tiga puluh satu detik, melompat dua langkah pada posisi detik
// terakhir, dan kode dua langkah ke depan ditolak server karena di luar
// toleransi satu langkah.
func TestLangkahBerikutnya_SelaluTepatSatuLangkah(t *testing.T) {
	const langkah = 30
	// Awal sebuah langkah, dipilih agar perhitungannya mudah ditelusuri.
	awal := time.Unix(1764547200, 0)

	var caraLamaMelompat int
	for offset := 0; offset < langkah; offset++ {
		sekarang := awal.Add(time.Duration(offset) * time.Second)
		langkahSekarang := sekarang.Unix() / langkah

		if got := langkahBerikutnya(sekarang).Unix() / langkah; got != langkahSekarang+1 {
			t.Fatalf("offset %ds: langkah %d, seharusnya %d",
				offset, got, langkahSekarang+1)
		}
		if caraLama := sekarang.Add(31*time.Second).Unix() / langkah; caraLama != langkahSekarang+1 {
			caraLamaMelompat++
		}
	}

	// Menjaga agar penjelasan di atas tetap benar. Bila suatu saat tidak ada
	// posisi yang membuat cara lama melompat, alasan perbaikan ini hilang dan
	// komentarnya menyesatkan.
	if caraLamaMelompat == 0 {
		t.Fatal("cara lama ternyata tidak pernah melompat, penjelasan perbaikan ini perlu ditinjau")
	}
	t.Logf("cara lama melompat dua langkah pada %d dari %d posisi detik",
		caraLamaMelompat, langkah)
}
