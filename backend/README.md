# Iceman Apps - Backend

Backend Go untuk Iceman Apps. Keputusan teknologinya tertulis pada
`03_System_Architecture.pdf` Bab 2, dan setiap paket di sini merujuk nomor
kebutuhan pada `02_SRS_Iceman_Apps.pdf`.

## Menjalankan

```bash
make up      # basis data pada port 5433, sekaligus menyiapkan basis data uji
make run     # server pada :8088, migrasi berjalan otomatis lebih dahulu
make test    # seluruh uji termasuk uji konkurensi
make down    # hentikan basis data
```

Butuh Go 1.26 dan Docker. Tidak ada yang perlu dipasang selain itu.

## Struktur

```
cmd/api            titik masuk server HTTP
internal/scheduling slot pengiriman dan kuota kapasitas
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

Dua uji pembanding terakhir sengaja dipertahankan. Bila suatu saat ada yang
mengusulkan menghapus `FOR UPDATE` demi kecepatan, jalankan keduanya lebih
dahulu.

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

## Catatan

Modul bernama `github.com/iceman/backend`. Ganti ke alamat repositori yang
sebenarnya sebelum rilis pertama.
