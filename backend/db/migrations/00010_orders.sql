-- +goose Up

-- Status pesanan. Transisi yang diizinkan diatur di kode (SRS Bab 5.1) karena
-- penjaganya butuh pemeriksaan peran dan efek samping, bukan hanya nama status.
CREATE TYPE order_status AS ENUM (
    'WAITING_PAYMENT',
    'PAID',
    'PROCESSING',
    'SCHEDULED',
    'OUT_FOR_DELIVERY',
    'COMPLETED',
    'CANCELLED'
);

-- Kanal pesanan. Pesanan manual dari WhatsApp tetap melewati seluruh validasi
-- yang sama, hanya kanalnya ditandai (SRS-ORD-006).
CREATE TYPE order_channel AS ENUM ('APP', 'WHATSAPP');

-- Penomoran pesanan. Satu barisan angka dipakai seluruh kanal agar nomornya
-- tidak pernah bertabrakan antara pesanan aplikasi dan pesanan manual
-- (DB-04). Barisan tidak diatur ulang setiap hari, karena pengaturan ulang
-- memerlukan penguncian harian yang menjadi titik bentrok saat ramai.
CREATE SEQUENCE order_no_seq;

CREATE TABLE orders (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    order_no        text          NOT NULL,
    invoice_no      text,
    customer_id     uuid          NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,

    -- Depo dan wilayah disalin agar laporan per depo tetap benar walau peta
    -- layanan berubah sesudahnya.
    depot_id        uuid          NOT NULL REFERENCES depots(id) ON DELETE RESTRICT,
    service_area_id uuid          NOT NULL REFERENCES service_areas(id) ON DELETE RESTRICT,

    -- Salinan alamat pengiriman (SRS-ORD-002). Pesanan tidak boleh berubah
    -- karena pelanggan menyunting alamatnya sesudah pesanan dibuat, dan driver
    -- harus mengantar ke alamat yang disetujui saat itu.
    address_id      uuid          REFERENCES customer_addresses(id) ON DELETE SET NULL,
    recipient_name  text          NOT NULL,
    recipient_phone text          NOT NULL,
    address_line    text          NOT NULL,
    address_notes   text          NOT NULL DEFAULT '',
    latitude        numeric(9,6)  NOT NULL,
    longitude       numeric(9,6)  NOT NULL,

    slot_id         uuid          NOT NULL REFERENCES delivery_slots(id) ON DELETE RESTRICT,
    scheduled_date  date          NOT NULL,

    status          order_status  NOT NULL,
    channel         order_channel NOT NULL DEFAULT 'APP',

    subtotal_cents     bigint NOT NULL,
    delivery_fee_cents bigint NOT NULL,
    discount_cents     bigint NOT NULL DEFAULT 0,
    total_cents        bigint NOT NULL,

    -- Termin yang berlaku saat pesanan dibuat, disalin agar jatuh tempo tetap
    -- dapat dihitung walau termin pelanggan berubah sesudahnya. Kosong berarti
    -- pesanan ini dibayar di muka.
    payment_term_days integer,

    -- Kunci idempotensi dari klien. Permintaan berulang dengan kunci yang sama
    -- menghasilkan satu pesanan (SRS-ORD-002), yang penting pada jaringan
    -- seluler yang sering memutus sambungan sesudah permintaan terkirim.
    idempotency_key text,

    -- Pembuat pesanan manual. Kosong untuk pesanan dari aplikasi pelanggan.
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,

    customer_notes   text NOT NULL DEFAULT '',
    cancelled_reason text NOT NULL DEFAULT '',

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,

    CONSTRAINT orders_no_key      UNIQUE (order_no),
    CONSTRAINT orders_invoice_key UNIQUE (invoice_no),

    -- DB-08: total wajib sama dengan rincian penyusunnya. Tanpa ini, galat
    -- pembulatan atau jalur kode yang lupa menghitung ulang menghasilkan
    -- tagihan yang tidak dapat dijelaskan kepada pelanggan.
    CONSTRAINT orders_total_konsisten
        CHECK (total_cents = subtotal_cents + delivery_fee_cents - discount_cents),
    CONSTRAINT orders_subtotal_nonneg CHECK (subtotal_cents >= 0),
    CONSTRAINT orders_fee_nonneg      CHECK (delivery_fee_cents >= 0),
    CONSTRAINT orders_discount_nonneg CHECK (discount_cents >= 0),
    CONSTRAINT orders_total_nonneg    CHECK (total_cents >= 0),
    CONSTRAINT orders_lat_range       CHECK (latitude  BETWEEN -90  AND 90),
    CONSTRAINT orders_lng_range       CHECK (longitude BETWEEN -180 AND 180),

    -- Alasan pembatalan wajib diisi (SRS-ORD-004). Dijaga basis data agar
    -- tidak ada jalur kode yang dapat membatalkan tanpa menyebut alasannya.
    CONSTRAINT orders_batal_beralasan
        CHECK (status <> 'CANCELLED' OR cancelled_reason <> '')
);

-- Permintaan berulang dengan kunci idempotensi yang sama menghasilkan satu
-- pesanan. Kunci berlaku per pelanggan, bukan menyeluruh, agar dua pelanggan
-- yang kebetulan memakai kunci yang sama tidak saling menghalangi.
CREATE UNIQUE INDEX orders_idempotency_key
    ON orders (customer_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE INDEX orders_dashboard_idx ON orders (status, scheduled_date, depot_id);
CREATE INDEX orders_riwayat_idx   ON orders (customer_id, created_at DESC);
CREATE INDEX orders_laporan_idx   ON orders (created_at);
CREATE INDEX orders_depo_idx      ON orders (depot_id, scheduled_date);
CREATE INDEX orders_slot_idx      ON orders (slot_id);

CREATE TRIGGER orders_set_updated_at BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Isi pesanan. Nama, kemasan, dan harga disalin dari produk saat pesanan
-- dibuat (SRS-ORD-002, BR-007). Menonaktifkan atau mengubah produk sesudahnya
-- tidak boleh mengubah pesanan historis, dan tanpa salinan ini pesanan lama
-- akan tampil dengan harga yang bukan harga yang dibayar pelanggan.
CREATE TABLE order_items (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id   uuid    NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id uuid    NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    sku        text    NOT NULL,
    name       text    NOT NULL,
    packaging  text    NOT NULL DEFAULT '',
    qty        integer NOT NULL,
    unit_price_cents bigint NOT NULL,
    line_total_cents bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- DB-07: jumlah wajib positif dan total baris wajib konsisten dengan
    -- harga satuan dikali jumlah.
    CONSTRAINT order_items_konsisten
        CHECK (qty > 0 AND line_total_cents = qty * unit_price_cents),
    CONSTRAINT order_items_harga_nonneg CHECK (unit_price_cents >= 0),
    CONSTRAINT order_items_product_key  UNIQUE (order_id, product_id)
);

CREATE INDEX order_items_order_idx ON order_items (order_id);

-- Riwayat perubahan status pesanan (SRS-ORD-003). Setiap transisi mencatat
-- status asal, status tujuan, pelaku, dan waktu.
--
-- Terpisah dari audit_trail karena dipakai pelanggan juga, bukan hanya admin:
-- riwayat status adalah bagian dari informasi pesanan yang dilihat pelanggan,
-- sedangkan audit_trail memuat perubahan data yang tidak perlu dilihatnya.
CREATE TABLE order_status_history (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid         NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    from_status order_status,
    to_status   order_status NOT NULL,
    actor_id    uuid         REFERENCES users(id) ON DELETE SET NULL,
    reason      text         NOT NULL DEFAULT '',
    occurred_at timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX order_status_history_order_idx
    ON order_status_history (order_id, occurred_at);

-- Riwayat status bersifat tetap, seperti jejak audit. Status pesanan
-- menentukan uang dan kewajiban mengantar, jadi riwayatnya tidak boleh
-- disunting setelah tercatat.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION order_status_history_immutable()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'order_status_history bersifat tetap: operasi % tidak diizinkan', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER order_status_history_no_update
    BEFORE UPDATE ON order_status_history
    FOR EACH ROW EXECUTE FUNCTION order_status_history_immutable();

CREATE TRIGGER order_status_history_no_delete
    BEFORE DELETE ON order_status_history
    FOR EACH ROW EXECUTE FUNCTION order_status_history_immutable();

-- +goose Down
DROP TABLE order_status_history;
DROP FUNCTION order_status_history_immutable();
DROP TABLE order_items;
DROP TABLE orders;
DROP SEQUENCE order_no_seq;
DROP TYPE order_channel;
DROP TYPE order_status;
