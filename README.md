# Iceman Apps

Backend sistem pemesanan dan distribusi es kristal: pencatatan pesanan lintas
kanal, pengelolaan kapasitas pengiriman per depo, pembayaran QRIS, dan modul
driver yang tetap bekerja saat jaringan buruk.

Ditulis dengan Go 1.26, PostgreSQL 18, dan Echo v4.

## Isi repositori ini

Repositori ini **hanya memuat kode**. Dokumen proyek, diagram perancangan, dan
aset merek klien sengaja tidak dipublikasikan karena memuat data operasional
dan komersial milik klien.

```
backend/      aplikasi Go: API, migrasi, dan uji
deploy/       docker compose untuk pengembangan lokal
docs_kit/     perkakas pembangun dokumen (pustaka umum, tanpa isi dokumen)
```

## Menjalankan

```bash
cp .env.example .env        # lalu isi POSTGRES_PASSWORD
cd backend
make up                     # PostgreSQL pada port 5433
make test                   # seluruh uji, termasuk uji konkurensi
make run                    # server pada :8088
```

Butuh Go 1.26 dan Docker. Tidak ada yang perlu dipasang selain itu.

Membuat pengguna untuk pengembangan:

```bash
make seed ROLE=ADMIN_OPS EMAIL=ops@contoh.test PASSWORD=rahasia-yang-panjang
```

## Keputusan teknis

| Pilihan | Alasan |
|---|---|
| Go + Echo | Model konkurensinya cocok dengan webhook pembayaran yang datang bersamaan dan sinkronisasi driver serentak. Satu binary, tanpa runtime. |
| SQL ditulis eksplisit, bukan ORM | Penguncian baris dan rekonsiliasi butuh SQL yang ditulis sadar. ORM menyembunyikan query yang justru paling perlu diawasi. |
| Antrean di PostgreSQL | Job dapat dimasukkan dalam transaksi yang sama dengan perubahan pembayaran, sehingga pembayaran tidak mungkin tercatat tanpa notifikasinya. Satu komponen infrastruktur berkurang. |
| UUID v7 | Berurutan waktu sehingga indeksnya rapat, namun tidak dapat ditebak dari luar. Sudah bawaan PostgreSQL 18. |
| Uang sebagai bilangan bulat | Rupiah penuh pada kolom bigint, agar rekonsiliasi tidak terganggu galat pembulatan. |

## Bagian yang paling diperhatikan

Dua tempat yang kalau salah, cacatnya baru ketahuan setelah merugikan orang.

**Kuota slot pengiriman.** Dua pelanggan dapat memesan slot terakhir pada detik
yang sama. Pemeriksaan ketersediaan tanpa kunci baris akan kebobolan, berapapun
cepatnya bahasa yang dipakai, karena keduanya membaca nilai yang sama sebelum
salah satunya menulis.

Uji `TestPembanding_TanpaKunciKebobolan` menunjukkan hal itu terjadi sungguhan:
40 percobaan bersamaan pada slot berkapasitas 5 menghasilkan 40 pemesanan yang
dianggap berhasil. Uji `TestPembanding_DenganKunciTidakKebobolan` menjalankan
beban yang sama melalui `SELECT ... FOR UPDATE` dan hasilnya tepat 5.

Keduanya sengaja dipertahankan. Bila suatu saat ada yang mengusulkan menghapus
penguncian demi kecepatan, jalankan dulu keduanya.

**Pemakaian ulang token penyegar.** Token penyegar hanya boleh dipakai sekali.
Pemakaian ulang dianggap tanda token tercuri, sehingga seluruh sesi pengguna itu
dibatalkan sekaligus, bukan hanya sesi yang bersangkutan.

## Uji

```
make test     seluruh uji
make race     dengan deteksi kondisi balapan
make fresh    basis data uji dihapus lalu diuji ulang dari nol
```

Uji memakai PostgreSQL sungguhan, bukan tiruan, karena yang diuji justru
perilaku transaksi dan penguncian baris.

## Keamanan

- Kata sandi memakai argon2id dengan garam acak dan perbandingan waktu tetap.
- Lima percobaan gagal mengunci akun selama lima belas menit.
- Peran berisiko tinggi ditolak masuk sampai verifikasi faktor kedua tersedia,
  bukan diloloskan sementara.
- Token penyegar disimpan sebagai hash, bukan nilai aslinya.
- Pemeriksaan izin berada di middleware, bukan tersebar di tiap handler.
- Tidak ada kredensial di dalam kode. Seluruhnya dibaca dari lingkungan, dan
  kunci penandatangan token dibangkitkan acak bila tidak diisi.

## Status

Tahap awal. Yang sudah berdiri: fondasi basis data, pengelolaan kuota slot,
autentikasi, sesi, dan hak akses berbasis peran.

Belum ada: verifikasi faktor kedua, katalog produk, pemesanan, pembayaran,
modul driver, dan pelacakan posisi.
