-- +goose Up

-- Jenis pelanggan bisnis (SRS-ADM-002).
--
-- SRS menyebut tiga jenis: ritel, bisnis, dan kontrak. Migrasi awal hanya
-- membuat dua, dan kekurangannya baru terlihat saat kebutuhan dashboard admin
-- dibaca ulang.
--
-- Bisnis berbeda dari kontrak: ia pelanggan usaha yang membeli dalam jumlah
-- besar namun tetap membayar di muka, sedangkan kontrak membayar belakangan
-- dengan termin. Membedakannya penting bagi laporan dan bagi penentuan harga
-- khusus, yang dapat diberikan kepada bisnis tanpa memberinya termin.
--
-- Nilai enum ditambahkan, bukan tipenya dibuat ulang, supaya baris yang sudah
-- ada tidak perlu dipindahkan.
ALTER TYPE customer_type ADD VALUE IF NOT EXISTS 'BUSINESS';

-- +goose Down
-- PostgreSQL tidak mendukung penghapusan nilai enum. Membatalkannya berarti
-- membuat ulang tipenya beserta seluruh kolom yang memakainya, dan itu lebih
-- berisiko daripada menyisakan satu nilai yang tidak terpakai.
