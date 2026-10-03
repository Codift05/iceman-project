# Iceman Docs Kit

Perkakas pembangun dokumen proyek Iceman. Semua dokumen keluar dengan tampilan
identik: A4, Arial, teks justify, monokrom, cover bergaya Iceman, daftar isi dan
daftar gambar bernomor halaman, footer hanya nomor halaman.

## Struktur

```
docs_kit/
  icemankit.py            pustaka utama (kelas Doc)
  cover.py                generator cover dari artwork Iceman
  assets/
    template.docx         induk style, sectPr, dan footer
    cover_base.png        artwork cover asli
    logo.png
    theme.iuml            tema PlantUML (garis siku-siku)
    plantuml.jar
  documents/
    doc00_decision_log.py satu berkas per dokumen
    diagrams/*.puml       sumber diagram dokumen tersebut
  build/                  hasil antara (cover, png, pdf sementara)
```

## Membuat dokumen baru

```python
from icemankit import Doc

d = Doc(code="SRS",
        title="Software Requirements Specification",
        version="1.0",
        status="Draft untuk Review")

d.h1("1. Pendahuluan")
d.p("Paragraf.")
d.bullets(["Butir satu", "Butir dua"])
d.h2("1.1 Sub Bagian")
d.table([["Kolom A", "Kolom B"], ["isi", "isi"]], [4000, 5746])
d.req("FR-001: Judul Requirement", [
    ["Sumber", "[REQ]"],
    ["Prioritas", "Must Have"],
    ["Acceptance Criteria", ["1. Kriteria pertama.", "2. Kriteria kedua."]],
])
d.diagram("diagrams/alur.puml", "Alur proses pemesanan.")
d.build("02_SRS_Iceman_Apps")     # menghasilkan .docx dan .pdf di folder proyek
```

## Catatan penting

- `d.figure()` dan `d.diagram()` menomori gambar otomatis sebagai `Gambar N.`
  dan mendaftarkannya ke Daftar Gambar. Jangan menulis nomor sendiri.
- Lebar tabel selalu dipaskan ke blok teks (9746 twips). Kolom yang diberikan
  akan diskala agar jumlahnya persis, jadi angka lebar bersifat proporsional.
- `build()` menjalankan dua kali render PDF: pertama untuk mengukur halaman,
  kedua setelah nomor halaman daftar isi diisi. Hasilnya diverifikasi otomatis.
- Hindari tanda em dash dan en dash pada teks. Gunakan titik dua, koma, atau
  tanda kurung.
- Diagram memakai tema siku-siku. Jangan mengubah `linetype` pada berkas puml.

## Prasyarat

Java (PlantUML), LibreOffice (`soffice`), `pdftotext`, Python `python-docx` dan
`Pillow`. Semuanya sudah tersedia di mesin ini.
