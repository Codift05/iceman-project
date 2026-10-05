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
internal/catalog    produk, harga dasar, dan harga khusus pelanggan
internal/customer   pelanggan, alamat pengiriman, dan termin kontrak
internal/cart       keranjang di sisi server
internal/order      checkout, transisi status, dan pesan ulang
internal/delivery   penugasan driver, modul driver, dan pelacakan posisi
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
- Katalog produk beserta harga khusus per pelanggan kontrak.
- Pelanggan, alamat pengiriman, dan termin pembayaran kontrak.
- Keranjang di sisi server yang menghitung ulang harga setiap kali dibaca.
- Pembuatan pesanan lengkap dengan penguncian kuota slot, idempotensi, dan
  pengantrean pembuatan tagihan dalam satu transaksi.
- Transisi status pesanan mengikuti matriks yang mengikat, termasuk pembatalan
  yang mengembalikan kuota dan penjadwalan ulang yang memindahkannya.
- Pesan ulang dari pesanan sebelumnya memakai harga terkini.
- Penugasan driver per depo, lengkap dengan penugasan ulang dan riwayatnya.
- Modul driver: daftar tugas, pembaruan status, bukti serah terima, laporan
  kendala, dan sinkronisasi perintah yang dibuat tanpa jaringan.
- Pelacakan posisi driver dengan persetujuan, partisi bulanan, dan pembersihan
  masa simpan lewat pelepasan partisi.
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
| `TestCheckout_DuaBersamaanKuotaTerakhirSatuBerhasil` | SRS-ORD-002, dua checkout bersamaan menghasilkan tepat satu pesanan |
| `TestCheckout_DuaPuluhBersamaanKuotaLimaLimaBerhasil` | kegagalan berupa SLOT_FULL, bukan pelanggaran kekangan |
| `TestCheckout_KuotaDanPesananSelaluSamaBanyak` | tidak ada kuota tanpa pesanan, tidak ada pesanan tanpa kuota |
| `TestCheckout_KunciIdempotensiBersamaanSatuPesanan` | permintaan kembar serentak tetap satu pesanan |
| `TestCheckout_PenolakanTidakMenyisakanKuotaDanKeranjang` | checkout gagal tidak menyisakan kuota maupun keranjang kosong |
| `TestMatriks_SesuaiTabelSRS` | matriks transisi cocok dengan tabel SRS Bab 5.1 |
| `TestAlamat_PelangganLainTidakDapatMembaca` | alamat pelanggan lain tidak terbaca sama sekali |
| `TestTransisiKirim_HanyaDriverYangDitugaskan` | izin saja tidak cukup, tugas siapa ini juga diperiksa |
| `TestPenugasanUlang_DriverLamaKehilanganAkses` | penugasan ulang mencabut akses driver lama |
| `TestMatriksKirim_SesuaiTabelSRS` | matriks pengiriman cocok dengan tabel SRS Bab 5.3 |
| `TestSync_PerintahSamaTigaKaliSatuPerubahan` | DB-03, perintah kembar hanya diproses sekali |
| `TestSync_DiprosesMenurutWaktuPerangkat` | antrean offline yang tiba tidak berurutan tetap benar |
| `TestPosisi_KirimUlangTidakMenggandakan` | kunci alami posisi membuat kiriman ulang idempoten |
| `TestPosisi_TanpaPersetujuanDitolak` | SEC-012, perekaman butuh persetujuan driver |
| `TestMasaSimpanPosisi_MenyiapkanDanMelepasPartisi` | SRS-TRK-004, pembersihan lewat pelepasan partisi |

Dua uji pembanding terakhir sengaja dipertahankan. Bila suatu saat ada yang
mengusulkan menghapus `FOR UPDATE` demi kecepatan, jalankan keduanya lebih
dahulu.

Tiga uji sudah diperiksa dengan cara merusak kode yang diujinya lebih dahulu,
supaya lulusnya memang berarti sesuatu:

`TestEnqueueTx_IkutBatalSaatTransaksiBatal`, bila `InsertTx` diganti `Insert`
biasa, gagal karena job bertahan walau transaksinya dibatalkan.

`TestAlamat_PelangganLainTidakDapatMembaca`, bila saringan `customer_id`
dihapus, gagal karena pelanggan lain berhasil membaca alamat itu.

`TestCheckout_DuaPuluhBersamaanKuotaLimaLimaBerhasil`, bila `FOR UPDATE`
dilepas, gagal karena kegagalannya berubah menjadi pelanggaran kekangan alih
alih `SLOT_FULL`. Uji ini mulanya hanya memeriksa jumlah akhir dan tidak
menggigit, karena kekangan DB-01 tetap menjaga jumlahnya benar. Yang
membedakan ada tidaknya penguncian adalah bentuk galatnya, bukan jumlahnya.

`TestTransisiKirim_HanyaDriverYangDitugaskan`, bila pemeriksaan driver yang
ditugaskan dihapus, gagal bersama satu uji lain karena driver lain dan driver
lama sama sama berhasil mengubah tugas orang.

`TestSync_DiprosesMenurutWaktuPerangkat`, bila pengurutan menurut waktu
perangkat dilepas, gagal dengan pesan "ASSIGNED ke ARRIVED tidak diizinkan"
karena perintah diproses mengikuti urutan kedatangan.

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

## Alur pesanan

Pembuatan pesanan adalah bagian dengan aturan terbanyak, dan urutannya
mengikuti dua diagram alur checkout pada SRS Bab 4.3.

Seluruh pemeriksaan yang tidak memerlukan kunci dikerjakan lebih dahulu, di
luar transaksi: kunci idempotensi, isi keranjang, kepemilikan alamat, keaktifan
wilayah, kecocokan slot dengan wilayah, dan termin pelanggan. Baru setelah
semuanya lolos, transaksi dimulai dan baris slot dikunci. Mengunci lebih awal
berarti permintaan yang jelas salah ikut menahan pelanggan lain yang mengincar
slot yang sama.

Di dalam transaksi, lima hal terjadi bersama: kuota slot diambil, pesanan dan
isinya disimpan, keranjang dikosongkan, riwayat status dicatat, dan job
pembuatan tagihan diantre.

Keranjang sengaja tidak menyimpan harga. Harga dihitung ulang setiap keranjang
dibaca, sehingga perubahan harga oleh admin langsung tercermin. Harga baru
disalin pada saat pesanan dibuat, karena sejak itu nilainya harus tetap
(BR-007).

Pesanan menyimpan salinan nama produk, kemasan, harga satuan, ongkos kirim, dan
alamat. Tanpa salinan itu, menonaktifkan produk atau menyunting alamat akan
mengubah pesanan yang sudah disetujui.

| Endpoint | Izin |
|---|---|
| `GET /v1/public/products` | terbuka |
| `GET /v1/products` | `product.view` |
| `POST /v1/products` | `product.manage` |
| `PUT /v1/products/:id/contract-price` | `product.manage` |
| `GET /v1/customers` | `customer.view` |
| `POST /v1/customers` | `customer.manage` |
| `POST /v1/customers/:id/addresses` | `customer.manage` |
| `PUT /v1/customers/:id/contract-term` | `customer.manage_terms` |
| `GET /v1/customers/:id/cart` | `order.create_manual` |
| `POST /v1/customers/:id/cart/items` | `order.create_manual` |
| `POST /v1/customers/:id/orders/:orderID/reorder` | `order.create_manual` |
| `GET /v1/orders` | `order.view` |
| `POST /v1/orders` | `order.create_manual` |
| `POST /v1/orders/:id/status` | `order.manage` |
| `POST /v1/orders/:id/cancel` | `order.manage` |
| `POST /v1/orders/:id/reschedule` | `order.manage` |

Penolakan `SLOT_FULL` disertai tawaran jadwal terdekat yang masih terbuka.
Tanpa itu, satu satunya jalan bagi pelanggan adalah mencoba slot satu per satu.

## Yang menunggu keputusan klien

Rute pelanggan untuk keranjang, checkout, dan riwayat belum dipasang karena
OQ-002 belum diputuskan: cara pelanggan masuk dan kanal pengiriman kode OTP.
Dua dari empat pilihannya mengirim OTP lewat WhatsApp, yang sudah dikeluarkan
dari lingkup, sehingga pilihannya perlu ditinjau ulang.

Seluruh jalur keranjang dan checkout sudah berjalan dan teruji lewat rute
pesanan manual (SRS-ORD-006), dengan aturan yang sama persis. Yang belum ada
hanya lapisan autentikasi pelanggannya.

Promo belum diterapkan. SRS-CAT-003 berstatus Should Have dan diskon tetap nol
sampai aturannya diputuskan, namun nilainya sudah dihitung di jalur checkout
sehingga DB-08 tetap memeriksa konsistensi total.

Penyedia pembayaran belum disambungkan. Job pembuatan tagihan sudah diantre
dalam transaksi yang sama dengan pesanan, tinggal mengisi pemanggilan
penyedianya.

Pengelolaan pengguna internal belum punya endpoint. Membuat driver beserta
deponya untuk sekarang lewat `make seed ROLE=DRIVER DEPOT=MDO-01`. Driver wajib
terikat satu depo karena DB-10 mewajibkan driver dan pesanan berasal dari depo
yang sama, dan perintah seed menolak peran DRIVER tanpa depo agar akunnya tidak
jadi lalu penugasannya gagal tanpa sebab yang jelas.

Perkiraan waktu tiba belum dihitung. SRS-TRK-003 berstatus Should Have dan
memerlukan mesin routing yang berjalan sendiri. Kolomnya sudah ada pada baris
pengiriman dan ikut dikirim ke peta, bernilai kosong sampai mesinnya
disambungkan.

Penyiaran posisi lewat kanal realtime belum ada. Peta sekarang dibaca dengan
permintaan biasa ke `/v1/tracking/live`. Pencatatan posisinya sudah berjalan
penuh, dan SRS-TRK-002 memang mewajibkan kegagalan kanal realtime tidak
menghentikan pencatatan, jadi menambahkannya nanti tidak mengubah yang sudah
ada.

## Pengiriman dan modul driver

Satu pesanan satu baris pengiriman (DB-05). Penugasan ulang mengubah baris itu,
bukan menambah baris baru, sehingga tidak mungkin ada dua driver yang sama sama
merasa bertugas. Riwayat penugasannya disimpan tersendiri agar tetap dapat
ditelusuri.

Driver dan pesanan wajib berasal dari depo yang sama (DB-10). Karena menyangkut
tiga tabel, ini dijaga pemicu basis data, dan diperiksa juga di kode supaya
galatnya dapat dibaca admin.

Seluruh driver memegang izin `delivery.update_own`, jadi izin saja tidak
membedakan tugas siapa sebuah pengiriman. Matriks transisi menandai perpindahan
mana yang hanya boleh dilakukan driver yang ditugaskan, dan pemeriksaannya
memakai pengenal dari token, bukan dari parameter.

Modul driver berjalan di perangkat yang sering kehilangan sinyal, sehingga dua
hal menjadi bawaan: setiap perintah membawa pengenal buatan perangkat agar
pengiriman ulang tidak diproses dua kali (DB-03), dan setiap perubahan
menyimpan waktu perangkat di samping waktu server. Waktu server yang dipakai
untuk urutan resmi, karena jam perangkat dapat meleset atau diubah.

Perintah diproses mengikuti urutan waktu perangkat, bukan urutan kedatangan.
Antrean lokal dapat terkirim tidak berurutan, dan memproses "tiba" sebelum
"berangkat" akan ditolak matriks padahal driver mengerjakannya dengan benar.

```
POST /v1/driver/sync
  { "commands": [ { "client_event_id": "...", "delivery_id": "...",
                    "status": "ARRIVED", "device_time": "..." } ] }
  -> { "results": [ { "outcome": "CONFLICT", "server_status": "DELIVERED",
                      "reason": "..." } ] }
```

Konflik dilaporkan di dalam badan jawaban, bukan sebagai status HTTP gagal,
karena satu kumpulan dapat memuat perintah yang berhasil dan yang berkonflik
sekaligus. Satu status HTTP tidak dapat mewakili keduanya.

| Endpoint | Izin |
|---|---|
| `GET /v1/deliveries` | `dispatch.manage` |
| `POST /v1/deliveries` | `dispatch.manage` |
| `PUT /v1/deliveries/:id/sequence` | `dispatch.manage` |
| `POST /v1/deliveries/:id/status` | `dispatch.manage` |
| `GET /v1/deliveries/:id` | `order.view` |
| `GET /v1/tracking/live` | `tracking.view` |
| `GET /v1/deliveries/:id/trail` | `tracking.view` |
| `GET /v1/driver/tasks` | `delivery.view_own` |
| `POST /v1/driver/tasks/:id/status` | `delivery.update_own` |
| `POST /v1/driver/tasks/:id/proof` | `delivery.update_own` |
| `POST /v1/driver/tasks/:id/positions` | `delivery.update_own` |
| `PUT /v1/driver/tracking-consent` | `delivery.update_own` |
| `POST /v1/driver/sync` | `delivery.update_own` |

Izin peta posisi dipisahkan dari pengelolaan dispatch, karena posisi driver
adalah data pribadi dan yang boleh melihat peta belum tentu boleh mengatur
penugasan.

## Pelacakan posisi dan data pribadi

Posisi driver adalah data pribadi, dan pelacakannya menyentuh urusan pemantauan
karyawan (SEC-012). Tiga hal mengikuti dari itu.

Perekaman tidak berjalan sebelum driver menyetujuinya, dan pencabutan
persetujuan menghentikannya seketika karena diperiksa setiap kali posisi
dikirim. Rekaman yang sudah ada tidak dihapus: itu bukti pengiriman yang sudah
berlangsung.

Perekaman hanya aktif saat driver sedang menuju lokasi atau sudah tiba. Di luar
itu, pelacakan melampaui keperluan operasional.

Pembacaan jejak posisi tercatat pada jejak audit, sehingga siapa yang
membukanya dapat ditelusuri.

Tabel posisi dipartisi menurut bulan. Masa simpan tiga puluh hari dibersihkan
dengan melepas partisi, bukan menghapus baris: melepas partisi hampir seketika
dan tidak mengunci tabel, sedangkan DELETE pada puluhan juta baris mengunci dan
membengkakkan tabel sampai autovacuum menyusulnya. Posisi terakhir dan
perkiraan tiba disalin ke baris pengiriman agar tetap tersedia setelah rekaman
mentah dihapus.

Driver yang posisinya tidak diperbarui lebih dari lima belas menit ditandai
tidak terpantau, bukan ditampilkan pada posisi usang. Menampilkan posisi lama
seolah terkini membuat admin menelepon driver yang disangka berhenti, padahal
yang hilang hanya sinyalnya.
