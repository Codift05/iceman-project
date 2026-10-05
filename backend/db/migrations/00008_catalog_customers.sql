-- +goose Up

-- Produk. Harga disimpan dalam sen agar tidak ada pembulatan pecahan
-- (Database Bab 3). Kolom sku bersifat unik dan menjadi identitas produk pada
-- seluruh kanal (DB: UNIQUE (sku)).
CREATE TABLE products (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    sku               text        NOT NULL,
    name              text        NOT NULL,
    category          text        NOT NULL DEFAULT '',
    -- Kemasan disimpan sebagai teks bebas, misalnya "Balok 25 kg" atau
    -- "Kristal 5 kg", karena Iceman masih menambah varian kemasan dan
    -- membakukannya sekarang berarti migrasi tiap kali ada varian baru.
    packaging         text        NOT NULL DEFAULT '',
    base_price_cents  bigint      NOT NULL,
    -- Minimum order per produk, bukan satu nilai global, karena balok dan
    -- kristal punya minimum yang berbeda (SRS-CAT-002).
    min_order_qty     integer     NOT NULL DEFAULT 1,
    -- Ketersediaan dinyatakan sebagai penanda, bukan jumlah stok. Iceman
    -- memproduksi sesuai pesanan dan belum menghitung stok satuan (OQ-004).
    -- Produk habis tetap muncul di katalog namun ditandai (SRS-CAT-001).
    is_available      boolean     NOT NULL DEFAULT true,
    is_active         boolean     NOT NULL DEFAULT true,
    photo_url         text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT products_sku_key        UNIQUE (sku),
    CONSTRAINT products_price_nonneg   CHECK (base_price_cents >= 0),
    CONSTRAINT products_min_order_pos  CHECK (min_order_qty >= 1)
);

CREATE INDEX products_category_idx ON products (category) WHERE is_active;

CREATE TRIGGER products_set_updated_at BEFORE UPDATE ON products
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Pelanggan. Nomor telepon menjadi identitas masuk (DB: UNIQUE (phone)).
--
-- Pelanggan dibedakan menjadi ritel dan kontrak. Pelanggan kontrak dengan
-- termin yang sah tidak membayar di muka, pesanannya langsung PROCESSING
-- (BR-002, SRS-ORD-002).
CREATE TYPE customer_type AS ENUM ('RETAIL', 'CONTRACT');

CREATE TABLE customers (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    phone         text          NOT NULL,
    name          text          NOT NULL,
    email         text,
    type          customer_type NOT NULL DEFAULT 'RETAIL',
    is_active     boolean       NOT NULL DEFAULT true,
    created_at    timestamptz   NOT NULL DEFAULT now(),
    updated_at    timestamptz   NOT NULL DEFAULT now(),

    CONSTRAINT customers_phone_key UNIQUE (phone)
);

CREATE TRIGGER customers_set_updated_at BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Alamat pengiriman pelanggan. Terikat pada satu wilayah layanan, karena
-- ongkos kirim dan depo pelayannya mengikuti wilayah itu (SRS-DEP-003).
--
-- Koordinat disimpan agar pengiriman dapat dilacak dan depo terdekat dapat
-- ditentukan ulang bila peta layanan berubah.
CREATE TABLE customer_addresses (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id     uuid         NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    service_area_id uuid         NOT NULL REFERENCES service_areas(id) ON DELETE RESTRICT,
    label           text         NOT NULL DEFAULT '',
    recipient_name  text         NOT NULL,
    phone           text         NOT NULL,
    address_line    text         NOT NULL,
    notes           text         NOT NULL DEFAULT '',
    latitude        numeric(9,6) NOT NULL,
    longitude       numeric(9,6) NOT NULL,
    is_primary      boolean      NOT NULL DEFAULT false,
    is_active       boolean      NOT NULL DEFAULT true,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT customer_addresses_lat_range CHECK (latitude  BETWEEN -90  AND 90),
    CONSTRAINT customer_addresses_lng_range CHECK (longitude BETWEEN -180 AND 180)
);

CREATE INDEX customer_addresses_customer_idx
    ON customer_addresses (customer_id) WHERE is_active;

-- Satu pelanggan hanya boleh punya satu alamat utama. Dijaga indeks parsial
-- agar basis data menolaknya, bukan hanya kode aplikasi.
CREATE UNIQUE INDEX customer_addresses_one_primary
    ON customer_addresses (customer_id) WHERE is_primary AND is_active;

CREATE TRIGGER customer_addresses_set_updated_at BEFORE UPDATE ON customer_addresses
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Termin pembayaran pelanggan kontrak. Hanya termin aktif yang membuat pesanan
-- langsung PROCESSING tanpa pembayaran di muka (BR-002).
--
-- Batas piutang disimpan agar pesanan dapat ditolak bila pelanggan melampaui
-- plafonnya. Nilai nol berarti tanpa batas.
CREATE TABLE contract_terms (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id        uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    payment_term_days  integer     NOT NULL,
    credit_limit_cents bigint      NOT NULL DEFAULT 0,
    valid_from         date        NOT NULL,
    valid_until        date,
    is_active          boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT contract_terms_days_pos     CHECK (payment_term_days > 0),
    CONSTRAINT contract_terms_limit_nonneg CHECK (credit_limit_cents >= 0),
    CONSTRAINT contract_terms_period       CHECK (valid_until IS NULL OR valid_until >= valid_from)
);

-- Satu pelanggan hanya boleh punya satu termin aktif pada satu waktu.
CREATE UNIQUE INDEX contract_terms_one_active
    ON contract_terms (customer_id) WHERE is_active;

CREATE TRIGGER contract_terms_set_updated_at BEFORE UPDATE ON contract_terms
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Harga khusus per pelanggan. Bila ada dan masih berlaku, harga ini dipakai
-- menggantikan harga dasar produk (SRS-CAT-001).
CREATE TABLE contract_prices (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    product_id  uuid        NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    price_cents bigint      NOT NULL,
    valid_from  date        NOT NULL,
    valid_until date,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT contract_prices_nonneg CHECK (price_cents >= 0),
    CONSTRAINT contract_prices_period CHECK (valid_until IS NULL OR valid_until >= valid_from)
);

-- Satu pelanggan hanya boleh punya satu harga khusus per produk per periode
-- yang dimulai pada tanggal yang sama. Mencegah dua baris harga bersaing untuk
-- satu tanggal yang sama.
CREATE UNIQUE INDEX contract_prices_unique
    ON contract_prices (customer_id, product_id, valid_from);

CREATE TRIGGER contract_prices_set_updated_at BEFORE UPDATE ON contract_prices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE contract_prices;
DROP TABLE contract_terms;
DROP TABLE customer_addresses;
DROP TABLE customers;
DROP TYPE customer_type;
DROP TABLE products;
