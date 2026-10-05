-- +goose Up

-- Keranjang disimpan di sisi server dan terikat pada akun pelanggan
-- (SRS-ORD-001). Menyimpannya di perangkat membuat keranjang hilang saat
-- pelanggan berpindah perangkat, dan membuat harga dapat dimanipulasi klien.
CREATE TABLE carts (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Satu pelanggan satu keranjang. Keranjang ganda membuat pelanggan menambah
-- barang ke satu keranjang lalu melakukan checkout atas keranjang lainnya.
CREATE UNIQUE INDEX carts_customer_key ON carts (customer_id);

CREATE TRIGGER carts_set_updated_at BEFORE UPDATE ON carts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Isi keranjang. Perhatikan tidak ada kolom harga di sini.
--
-- Harga sengaja tidak disimpan. SRS-ORD-001 mewajibkan harga dan ketersediaan
-- divalidasi ulang setiap keranjang dibuka, dan perubahan harga oleh admin
-- tercermin saat itu. Menyimpan harga di keranjang berarti harga lama ikut
-- terbawa ke checkout, dan menyegarkannya menjadi pekerjaan tambahan yang
-- dapat terlupakan. Harga hanya disalin pada saat pesanan dibuat, karena sejak
-- itu nilainya harus tetap (BR-007).
CREATE TABLE cart_items (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    cart_id    uuid        NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    product_id uuid        NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    qty        integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT cart_items_qty_pos CHECK (qty > 0)
);

-- Satu produk satu baris per keranjang. Menambah produk yang sudah ada
-- menambah jumlahnya, bukan membuat baris baru (SRS-ORD-001).
CREATE UNIQUE INDEX cart_items_product_key ON cart_items (cart_id, product_id);

CREATE TRIGGER cart_items_set_updated_at BEFORE UPDATE ON cart_items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE cart_items;
DROP TABLE carts;
