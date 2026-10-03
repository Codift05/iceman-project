# Iceman Apps - Backend

Backend Go untuk Iceman Apps. Keputusan teknologinya tertulis pada
`03_System_Architecture.pdf` Bab 2, dan setiap paket di sini merujuk nomor
kebutuhan pada `02_SRS_Iceman_Apps.pdf`.

## Menjalankan

```bash
make up      # basis data pada port 5433, sekaligus menyiapkan basis data uji
make run     # server pada :8088, migrasi berjalan otomatis lebih dahulu
make worker  # pekerja latar, pembuat slot harian
make test    # seluruh uji termasuk uji konkurensi
make down    # hentikan basis data
```

Butuh Go 1.26 dan Docker. Tidak ada yang perlu dipasang selain itu.

## Struktur

```
cmd/api             titik masuk server HTTP
cmd/worker          titik masuk pekerja latar
cmd/seed            pembuat pengguna pengembangan
internal/scheduling depo, wilayah layanan, slot pengiriman, dan kuota kapasitas
internal/identity   kata sandi, token, faktor kedua, dan hak akses
internal/worker     job latar di atas antrean River
internal/audit      jejak audit yang ikut transaksi pemanggil
internal/httpx      bentuk galat seragam dan pengenal permintaan
internal/store      koneksi basis data dan penerapan migrasi
db/migrations       migrasi goose, ditanam ke dalam binary
```

## Yang sudah ada

- Migrasi depo, wilayah layanan, dan slot pengiriman beserta constraint penjaga
  kebenaran (DB-01, DB-09, DB-11).
- `scheduling.Reserve` mengambil kuota slot dengan `SELECT ... FOR UPDATE`,
  lengkap dengan pemeriksaan hari libur, batas cut-off, dan kapasitas.
- `scheduling.Release` dan `scheduling.Move` untuk pembatalan dan penjadwalan ulang.
- Autentikasi pengguna internal: argon2id, penguncian akun, token akses dan
  token penyegar yang diputar setiap dipakai.
- Faktor kedua berbasis waktu (TOTP), dengan satu kode hanya berlaku sekali.
- Hak akses berbasis peran, diperiksa di middleware.
- Jejak audit yang ditulis dalam transaksi yang sama dengan perubahannya, dan
  tidak dapat diubah maupun dihapus lewat aplikasi.
- Pengelolaan depo beserta penentuan depo terdekat dari koordinat pelanggan.
- Pengelolaan wilayah layanan dan slot pengiriman, termasuk kuota, hari libur,
  dan batas pemesanan.
- Ketersediaan slot untuk pelanggan, lengkap dengan alasan bila tidak dapat
  dipilih, dan tawaran slot terdekat yang masih terbuka.
- Pekerja latar yang membuat slot tiga puluh hari ke depan setiap hari.
- Bentuk galat seragam dan pengenal permintaan.
- Server HTTP dengan endpoint kesehatan dan mematikan diri dengan rapi.

## Uji konkurensi

Uji pada `internal/scheduling` adalah inti pembuktian rancangan, bukan pelengkap.

| Uji | Membuktikan |
|---|---|
| `TestReserve_DuaBersamaanSisaSatu` | TC-CON-01, tepat satu berhasil |
| `TestReserve_DuaPuluhBersamaanSisaLima` | TC-CON-02, tepat lima berhasil |
| `TestReserve_BanyakSlotTidakAdaKuotaHilang` | TC-CON-06, 600 percobaan pada 10 slot menghasilkan tepat 200 pemesanan |
| `TestReserve_RollbackTidakMenyisakanKuota` | transaksi gagal tidak menyisakan kuota terpakai |
| `TestConstraint_KuotaTidakBolehTerlampaui` | DB-01 menolak walau jalur kode lupa mengunci |
| `TestPembanding_TanpaKunciKebobolan` | cara tanpa kunci baris benar benar kebobolan |
| `TestPembanding_DenganKunciTidakKebobolan` | cara yang dipakai tidak kebobolan |
| `TestRefresh_PemakaianUlangMembatalkanSeluruhSesi` | token penyegar dipakai ulang membatalkan semua sesi |
| `TestRefresh_DuaPermintaanBersamaan` | delapan penyegaran bersamaan, tepat satu berhasil |
| `TestMFA_DuaVerifikasiBersamaanKodeSama` | delapan verifikasi bersamaan kode sama, tepat satu lolos |
| `TestMFA_TokenTantanganTerkunciTujuannya` | token tantangan tidak dapat menyamar jadi token akses |
| `TestIzin_SesuaiMatriksPRD` | 38 kombinasi peran dan izin dikunci |
| `TestSetCapacity_JejakIkutBatalSaatTransaksiGagal` | jejak audit batal bersama transaksinya |
| `TestAudit_TidakDapatDiubahMaupunDihapus` | basis data menolak UPDATE dan DELETE pada jejak |
| `TestEnqueueTx_IkutBatalSaatTransaksiBatal` | job antrean ikut batal bila transaksinya batal (AD-03) |
| `TestGenerator_Idempoten` | pembuatan slot dijalankan ulang tidak menggandakan slot |

Dua uji pembanding terakhir sengaja dipertahankan. Bila suatu saat ada yang
mengusulkan menghapus `FOR UPDATE` demi kecepatan, jalankan keduanya lebih
dahulu.

Uji `TestEnqueueTx_IkutBatalSaatTransaksiBatal` sudah diperiksa dengan cara
merusaknya: bila `InsertTx` diganti `Insert` biasa, uji itu gagal. Dengan
begitu lulusnya memang berarti sesuatu.

## Port yang dipakai

Mesin pengembangan ini menjalankan beberapa proyek lain. Iceman Apps sengaja
memakai port yang tidak dipakai siapa pun.

| Port | Pemakai | Milik |
|---|---|---|
| 5433 | PostgreSQL Iceman | proyek ini |
| 8088 | API Iceman | proyek ini |
| 80 | biotrace-caddy | proyek lain |
| 3000 | biotrace-gotenberg | proyek lain |
| 3001 | nala-dev-backend | proyek lain |
| 8080 | biotrace-go-backend | proyek lain |
| 8091 | prima-hsse-frontend | proyek lain |
| 9090 | biotrace-satusehat-mock | proyek lain |

Jangan pakai port di bagian bawah tabel. Bila butuh port baru, periksa lebih
dahulu dengan `ss -lntp` dan `docker ps`, lalu tambahkan ke tabel ini.

## Jejak audit

Perubahan data kritis dicatat beserta nilai lama, nilai baru, pelaku, waktu,
dan pengenal permintaan. Pencatatan memakai transaksi milik pemanggil, bukan
transaksi sendiri, sehingga membatalkan perubahan juga membatalkan catatannya.
Tanpa sifat ini, jejak dapat memuat perubahan yang sebenarnya tidak terjadi.

Catatan bersifat tetap: pemicu pada basis data menolak UPDATE dan DELETE untuk
siapa pun. Pembersihan karena masa simpan nanti dilakukan dengan melepas
partisi, bukan menghapus baris, sehingga tidak membuka celah penghapusan satuan.

Penolakan akses juga tercatat, lengkap dengan siapa yang mencoba, endpoint apa,
dan izin apa yang kurang.

```
GET /v1/admin/audit-trail?entity=delivery_slots&outcome=DENIED
```

Pelaku dan pengenal permintaan dibawa lewat konteks, bukan lewat parameter
setiap fungsi. Tanpa itu, seluruh fungsi di jalur perubahan data harus menambah
dua parameter yang hanya diteruskan tanpa dipakai.

## Alur masuk dengan faktor kedua

Peran Super Admin dan Keuangan wajib memakai faktor kedua. Peran lain boleh
mengaktifkannya sendiri, dan setelah aktif menjadi wajib.

```
POST /v1/auth/login          kata sandi benar
                             -> { mfa_required: true, challenge_token, next }

next = "enroll"              belum terdaftar
  POST /v1/auth/mfa/enroll   -> { secret, provisioning_uri }
  POST /v1/auth/mfa/confirm  kode pertama -> token akses dan penyegar

next = "verify"              sudah terdaftar
  POST /v1/auth/mfa/verify   kode -> token akses dan penyegar
```

Token tantangan berumur lima menit, membawa penanda jenis dan tujuan, serta
tidak dapat dipakai sebagai token akses. Satu kode TOTP hanya berlaku sekali:
langkah waktu yang sudah terpakai dicatat, sehingga kode yang sempat terlihat
orang lain tidak dapat dipakai ulang pada jendela tiga puluh detik yang sama.

## Catatan

Modul bernama `github.com/iceman/backend`. Ganti ke alamat repositori yang
sebenarnya sebelum rilis pertama.

## Pekerja latar

Antrean job memakai PostgreSQL yang sama dengan data aplikasi, bukan Redis atau
layanan antrean terpisah. Itu pilihan sadar (Architecture AD-03): job dapat
dimasukkan dalam transaksi yang sama dengan perubahan datanya, sehingga tidak
mungkin ada job yang mengacu pada perubahan yang ternyata batal, atau perubahan
tersimpan tanpa job susulannya.

```bash
make worker   # jalankan pekerja; migrasi antrean berjalan otomatis
```

Satu job sudah berjalan: pembuatan slot pengiriman tiga puluh hari ke depan
untuk setiap wilayah layanan aktif, sekali sehari. Pembuatannya idempoten,
bersandar pada unique index `(service_area_id, slot_date, window_start)`
(DB-09), sehingga job dapat dicoba ulang kapan saja termasuk setelah gagal di
tengah jalan. Penjadwalan hariannya dijalankan River hanya dari satu proses
yang terpilih sebagai pemimpin, jadi menambah pekerja tidak membuat slot dibuat
berkali kali.

Pola jam operasional masih ditetapkan di kode (`scheduling.DefaultTemplates`:
08.00, 11.00, dan 14.00) sampai Iceman memutuskan jam operasional dan cara
menghitung kapasitas (OQ-007). Memindahkannya ke tabel konfigurasi adalah
pekerjaan kecil yang menunggu keputusan itu.

## Endpoint penjadwalan

Endpoint terbuka dipakai aplikasi pelanggan sebelum masuk, saat memeriksa
apakah alamatnya terjangkau dan jadwal apa yang tersedia. Isinya hanya data
yang memang perlu diketahui calon pelanggan.

```
GET    /v1/public/depots/nearest?lat=&lng=
GET    /v1/public/areas/:id/availability?from=&days=
GET    /v1/public/areas/:id/slots/next
```

Sisanya butuh token dan izin.

| Endpoint | Izin |
|---|---|
| `GET /v1/depots` | `depot.view` |
| `GET /v1/depots/:id` | `depot.view` |
| `POST /v1/depots` | `depot.manage` |
| `PATCH /v1/depots/:id` | `depot.manage` |
| `GET /v1/areas` | `area_slot.view` |
| `POST /v1/areas` | `area_slot.manage` |
| `PATCH /v1/areas/:id` | `area_slot.manage` |
| `GET /v1/areas/:id/slots` | `area_slot.view` |
| `POST /v1/slots` | `area_slot.manage` |
| `PATCH /v1/slots/:id/capacity` | `area_slot.manage` |
| `PATCH /v1/slots/:id/holiday` | `area_slot.manage` |

Membuat depo hanya boleh dilakukan Super Admin. Admin Operasional memegang
`depot.view` namun tidak `depot.manage`, karena menambah depo mengubah peta
layanan dan bukan tindakan harian.

Slot yang tidak dapat dipilih tetap dikirim ke pelanggan beserta alasannya
(`FULL`, `CUTOFF_PASSED`, atau `HOLIDAY`), bukan disembunyikan. Menyembunyikan
slot penuh membuat pelanggan mengira layanan tidak tersedia pada hari itu
(UI/UX Bab 10.1).

Tanggal slot terbit sebagai `"2026-10-04"`, bukan cap waktu. Cap waktu tengah
malam mengundang klien menggesernya ke zona waktu lain dan menampilkan slot
pada hari yang salah.
