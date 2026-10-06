-- +goose Up

-- Penandaan selisih rekonsiliasi (SRS-PAY-005).
--
-- Selisih tidak selalu punya baris settlement, dan tidak selalu punya baris
-- pembayaran: pembayaran yang belum muncul pada settlement adalah selisih
-- tanpa settlement, dan settlement yang tidak cocok dengan pembayaran mana pun
-- adalah selisih tanpa pembayaran. Karena itu kedua rujukannya boleh kosong,
-- namun tidak boleh kosong keduanya.
CREATE TABLE reconciliation_flags (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    payment_id    uuid REFERENCES payments(id)    ON DELETE CASCADE,
    settlement_id uuid REFERENCES settlements(id) ON DELETE CASCADE,

    -- Jenis selisih, dipetakan dari hasil pembandingan. Disimpan sebagai teks
    -- agar jenis baru dapat ditambahkan tanpa migrasi; daftarnya dijaga kode.
    kind text NOT NULL,
    note text NOT NULL DEFAULT '',

    actor_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    resolved_at timestamptz,
    resolved_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT reconciliation_flags_ada_rujukan
        CHECK (payment_id IS NOT NULL OR settlement_id IS NOT NULL),
    CONSTRAINT reconciliation_flags_kind_wajib CHECK (kind <> ''),
    -- Penyelesaian wajib menyebut pelakunya. Selisih yang ditutup tanpa ada
    -- yang bertanggung jawab tidak dapat ditanyakan kembali.
    CONSTRAINT reconciliation_flags_selesai_berpelaku
        CHECK (resolved_at IS NULL OR resolved_by IS NOT NULL)
);

-- Satu selisih terbuka per pembayaran per jenis. Menandai dua kali hal yang
-- sama hanya menambah pekerjaan tinjauan tanpa menambah informasi.
CREATE UNIQUE INDEX reconciliation_flags_terbuka_payment
    ON reconciliation_flags (payment_id, kind)
    WHERE resolved_at IS NULL AND payment_id IS NOT NULL;

CREATE UNIQUE INDEX reconciliation_flags_terbuka_settlement
    ON reconciliation_flags (settlement_id, kind)
    WHERE resolved_at IS NULL AND settlement_id IS NOT NULL;

CREATE INDEX reconciliation_flags_terbuka_idx
    ON reconciliation_flags (created_at DESC) WHERE resolved_at IS NULL;

-- Berkas settlement dimasukkan beberapa kali selama satu periode, dan baris
-- yang sama dapat ikut terbawa. Indeks keunikan pada (provider, provider_ref)
-- sudah ada sejak migrasi pembayaran, jadi pemasukan ulang cukup memakai
-- ON CONFLICT tanpa perubahan skema.
--
-- Yang ditambahkan di sini hanya pencarian menurut referensi pembayaran, yang
-- dipakai saat menautkan baris settlement ke pembayarannya.
CREATE INDEX settlements_ref_lookup_idx ON settlements (provider_ref);

-- +goose Down
DROP INDEX settlements_ref_lookup_idx;
DROP TABLE reconciliation_flags;
