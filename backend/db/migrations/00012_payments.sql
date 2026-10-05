-- +goose Up

-- Status pembayaran internal (SRS-PAY-003, SRS Bab 5.2).
--
-- Status penyedia dipetakan ke daftar ini, bukan disimpan apa adanya, agar
-- berpindah penyedia tidak mengubah arti data lama.
CREATE TYPE payment_status AS ENUM (
    'PENDING',
    'SUCCESS',
    'EXPIRED',
    'FAILED',
    'REFUNDED',
    'CANCELLED'
);

-- Pembayaran. Satu pembayaran terhubung ke tepat satu pesanan (SRS-PAY-001),
-- namun satu pesanan dapat memiliki beberapa pembayaran berurutan: QRIS yang
-- kedaluwarsa diganti yang baru, dan pelanggan yang gagal dapat mencoba lagi.
--
-- Karena itu keunikannya bukan pada order_id, melainkan pada satu pembayaran
-- aktif per pesanan, yang dijaga indeks parsial di bawah.
CREATE TABLE payments (
    id       uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id uuid           NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    status   payment_status NOT NULL DEFAULT 'PENDING',

    -- Nominal disalin dari total pesanan saat pembayaran dibuat. Tagihan yang
    -- sudah terkirim ke pelanggan tidak boleh berubah karena pesanannya
    -- disunting, dan nominal inilah yang dibandingkan dengan nominal pada
    -- webhook (SRS-PAY-002).
    amount_cents bigint NOT NULL,

    -- Metode dan referensi penyedia. Referensi bersifat unik agar satu
    -- transaksi penyedia tidak dapat dipakai dua pesanan (SRS-PAY-001).
    method          text NOT NULL DEFAULT 'QRIS',
    provider        text NOT NULL DEFAULT '',
    provider_ref    text,
    -- Muatan QRIS yang ditampilkan pelanggan. Disimpan agar pelanggan dapat
    -- membuka kembali kodenya tanpa membuat tagihan baru.
    qr_payload text NOT NULL DEFAULT '',

    expires_at timestamptz,
    paid_at    timestamptz,

    -- Biaya penyedia, terisi dari settlement. Dipisahkan dari nominal agar
    -- rekonsiliasi dapat membandingkan yang diterima dengan yang ditagihkan.
    provider_fee_cents bigint NOT NULL DEFAULT 0,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT payments_provider_ref_key UNIQUE (provider_ref),
    CONSTRAINT payments_amount_pos       CHECK (amount_cents > 0),
    CONSTRAINT payments_fee_nonneg       CHECK (provider_fee_cents >= 0),
    -- Pembayaran berhasil wajib punya waktu pembayaran. Tanpa itu, laporan
    -- penerimaan per periode tidak dapat dihitung.
    CONSTRAINT payments_sukses_berwaktu
        CHECK (status NOT IN ('SUCCESS', 'REFUNDED') OR paid_at IS NOT NULL)
);

-- Satu pesanan hanya boleh punya satu pembayaran yang masih menunggu.
-- Dua QRIS aktif untuk satu pesanan membuat pelanggan dapat membayar dua kali.
CREATE UNIQUE INDEX payments_satu_pending
    ON payments (order_id) WHERE status = 'PENDING';

-- Satu pesanan juga hanya boleh punya satu pembayaran yang berhasil.
CREATE UNIQUE INDEX payments_satu_sukses
    ON payments (order_id) WHERE status IN ('SUCCESS', 'REFUNDED');

CREATE INDEX payments_order_idx  ON payments (order_id, created_at DESC);
CREATE INDEX payments_status_idx ON payments (status, created_at);

CREATE TRIGGER payments_set_updated_at BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Event webhook dari penyedia pembayaran (DB-02).
--
-- Event disimpan sebelum diproses, dengan kunci unik dari penyedia. Penyedia
-- mengirim ulang event yang belum dijawab berhasil, dan tanpa kunci ini satu
-- pembayaran dapat tercatat berkali kali (SRS-PAY-002).
--
-- Muatan mentah disimpan apa adanya untuk penelusuran. Ketika nanti ada
-- selisih dengan penyedia, yang menyelesaikannya adalah muatan asli, bukan
-- tafsiran kita atasnya.
CREATE TABLE payment_events (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    event_id   text        NOT NULL,
    payment_id uuid        REFERENCES payments(id) ON DELETE SET NULL,
    provider   text        NOT NULL DEFAULT '',
    -- Status mentah dari penyedia, disimpan sebelum dipetakan. Status yang
    -- belum dikenal perlu dapat ditinjau manusia, bukan hilang (SRS-PAY-003).
    provider_status text  NOT NULL DEFAULT '',
    mapped_status   payment_status,
    amount_cents    bigint,
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Penanda event yang perlu ditinjau: status penyedia tidak dikenal,
    -- nominal tidak cocok, atau pembayarannya tidak ditemukan.
    needs_review boolean NOT NULL DEFAULT false,
    review_note  text    NOT NULL DEFAULT '',

    processed_at timestamptz,
    received_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT payment_events_event_key UNIQUE (event_id)
);

CREATE INDEX payment_events_payment_idx ON payment_events (payment_id, received_at);
CREATE INDEX payment_events_review_idx  ON payment_events (received_at DESC) WHERE needs_review;

-- Percobaan webhook yang tanda tangannya salah.
--
-- Dicatat terpisah dari payment_events karena muatannya tidak dipercaya:
-- menyimpannya di tabel yang sama berarti data yang belum terverifikasi
-- bercampur dengan data yang sudah. Pencatatannya tetap perlu, sebab lonjakan
-- percobaan dengan tanda tangan salah adalah tanda seseorang sedang mencoba
-- memalsukan pembayaran (SRS-PAY-002).
CREATE TABLE payment_webhook_rejects (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    provider    text        NOT NULL DEFAULT '',
    reason      text        NOT NULL,
    remote_addr text        NOT NULL DEFAULT '',
    body_sha256 text        NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payment_webhook_rejects_waktu_idx
    ON payment_webhook_rejects (received_at DESC);

-- Pengembalian dana (SRS-PAY-004).
--
-- Disimpan sebagai baris tersendiri, bukan sebagai kolom pada pembayaran,
-- karena refund dapat terjadi beberapa kali sebagian sebagian, dan masing
-- masing punya referensi, pelaku, dan alasannya sendiri.
CREATE TABLE refunds (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    payment_id   uuid        NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
    amount_cents bigint      NOT NULL,
    reason       text        NOT NULL,
    -- Referensi penyedia untuk refund otomatis. Kosong berarti refund dicatat
    -- manual, yaitu dana dikembalikan di luar sistem lalu dicatat di sini.
    provider_ref text,
    is_manual    boolean     NOT NULL DEFAULT true,
    actor_id     uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT refunds_amount_pos      CHECK (amount_cents > 0),
    CONSTRAINT refunds_reason_wajib    CHECK (reason <> ''),
    CONSTRAINT refunds_provider_ref_key UNIQUE (provider_ref)
);

CREATE INDEX refunds_payment_idx ON refunds (payment_id, created_at);

-- Baris settlement dari penyedia, dasar rekonsiliasi (SRS-PAY-005).
--
-- Dimasukkan dari berkas settlement penyedia. Pembayaran yang belum muncul di
-- sini tetap tampil pada rekonsiliasi sebagai selisih, bukan disembunyikan.
CREATE TABLE settlements (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    provider      text        NOT NULL DEFAULT '',
    provider_ref  text        NOT NULL,
    payment_id    uuid        REFERENCES payments(id) ON DELETE SET NULL,
    gross_cents   bigint      NOT NULL,
    fee_cents     bigint      NOT NULL DEFAULT 0,
    net_cents     bigint      NOT NULL,
    settled_date  date        NOT NULL,
    -- Catatan tindak lanjut untuk selisih yang ditandai.
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT settlements_ref_key    UNIQUE (provider, provider_ref),
    CONSTRAINT settlements_fee_nonneg CHECK (fee_cents >= 0),
    -- Net wajib sama dengan gross dikurangi biaya, supaya berkas settlement
    -- yang dibaca salah tertangkap saat dimasukkan, bukan saat laporan dibaca.
    CONSTRAINT settlements_net_konsisten CHECK (net_cents = gross_cents - fee_cents)
);

CREATE INDEX settlements_tanggal_idx ON settlements (settled_date);
CREATE INDEX settlements_payment_idx ON settlements (payment_id);

-- +goose Down
DROP TABLE settlements;
DROP TABLE refunds;
DROP TABLE payment_webhook_rejects;
DROP TABLE payment_events;
DROP TABLE payments;
DROP TYPE payment_status;
