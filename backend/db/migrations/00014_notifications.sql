-- +goose Up

-- Pengaturan notifikasi per jenis event (SRS-NOT-001).
--
-- Event yang aktif dan kanalnya dapat diubah admin tanpa rilis ulang. Kanal
-- disimpan sebagai senarai teks, bukan enum, karena kanal mana yang wajib
-- belum diputuskan (OQ-012) dan dua dari empat pilihannya memakai WhatsApp
-- yang sudah dikeluarkan dari lingkup. Daftar kanal yang dikenali dijaga kode.
CREATE TABLE notification_settings (
    event      text        PRIMARY KEY,
    enabled    boolean     NOT NULL DEFAULT true,
    channels   text[]      NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notification_settings_event_wajib CHECK (event <> '')
);

CREATE TRIGGER notification_settings_set_updated_at BEFORE UPDATE ON notification_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Percobaan pengiriman notifikasi, dipartisi menurut bulan.
--
-- ERD menyebut tabel ini salah satu yang tumbuh paling cepat, dengan masa
-- simpan enam bulan. Partisi dipakai agar pembersihannya dilakukan dengan
-- melepas partisi, bukan menghapus baris satu per satu.
--
-- Setiap percobaan tercatat beserta kanal, status, dan alasan kegagalannya
-- (SRS-NOT-001), karena kegagalan notifikasi tidak boleh menggagalkan
-- transaksi inti dan karena itu satu satunya cara mengetahuinya adalah dari
-- catatan ini.
CREATE TABLE notifications (
    id      uuid NOT NULL DEFAULT uuidv7(),
    event   text NOT NULL,
    channel text NOT NULL,

    -- Penerima disimpan apa adanya, misalnya nomor telepon atau token
    -- perangkat, karena bentuknya berbeda tiap kanal.
    recipient text NOT NULL DEFAULT '',

    -- Rujukan ke hal yang diberitahukan. Tidak memakai foreign key karena
    -- tabel ini dipartisi dan dibersihkan berkala, sedangkan pesanan yang
    -- dirujuknya bertahan jauh lebih lama; kunci asing akan menghalangi
    -- pelepasan partisi.
    order_id    uuid,
    customer_id uuid,

    title text NOT NULL DEFAULT '',
    body  text NOT NULL DEFAULT '',

    status     text    NOT NULL DEFAULT 'PENDING',
    attempt    integer NOT NULL DEFAULT 0,
    last_error text    NOT NULL DEFAULT '',

    sent_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (created_at, id),

    CONSTRAINT notifications_status_valid
        CHECK (status IN ('PENDING', 'SENT', 'FAILED', 'SKIPPED')),
    CONSTRAINT notifications_attempt_nonneg CHECK (attempt >= 0),
    CONSTRAINT notifications_event_wajib    CHECK (event <> ''),
    CONSTRAINT notifications_channel_wajib  CHECK (channel <> ''),
    -- Notifikasi berstatus terkirim wajib punya waktu kirim. Tanpa itu,
    -- pemantauan tidak dapat membedakan yang baru terkirim dari yang lama.
    CONSTRAINT notifications_terkirim_berwaktu
        CHECK (status <> 'SENT' OR sent_at IS NOT NULL)
) PARTITION BY RANGE (created_at);

-- Indeks pemantauan kegagalan pengiriman, sesuai ERD Bab 7.2.
CREATE INDEX notifications_status_idx ON notifications (status, created_at);
CREATE INDEX notifications_order_idx  ON notifications (order_id, created_at DESC);

-- ensure_monthly_partition membuat partisi satu bulan untuk tabel mana pun
-- yang dipartisi menurut rentang waktu.
--
-- Menggantikan ensure_driver_location_partition, yang mengerjakan hal yang
-- sama hanya untuk satu tabel. Dengan dua tabel terpartisi, dua fungsi yang
-- hampir sama berarti dua tempat yang harus diperbaiki bila cara penamaannya
-- berubah.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ensure_monthly_partition(induk text, bulan date)
RETURNS text AS $$
DECLARE
    awal  date := date_trunc('month', bulan)::date;
    akhir date := (date_trunc('month', bulan) + interval '1 month')::date;
    nama  text := induk || '_' || to_char(awal, 'YYYY_MM');
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = nama) THEN
        RETURN nama;
    END IF;
    EXECUTE format(
        'CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
        nama, induk, awal, akhir);
    RETURN nama;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Partisi bulan ini dan tiga bulan ke depan disiapkan sejak awal, supaya
-- pencatatan notifikasi tidak gagal seandainya pekerja latar belum berjalan.
SELECT ensure_monthly_partition('notifications', current_date);
SELECT ensure_monthly_partition('notifications', (current_date + interval '1 month')::date);
SELECT ensure_monthly_partition('notifications', (current_date + interval '2 months')::date);
SELECT ensure_monthly_partition('notifications', (current_date + interval '3 months')::date);

DROP FUNCTION IF EXISTS ensure_driver_location_partition(date);

-- Event bawaan beserta kanalnya.
--
-- Kanal diisi LOG karena kanal sungguhan belum diputuskan (OQ-012). Kanal LOG
-- mencatat notifikasi tanpa mengirimnya ke mana pun, sehingga seluruh jalur
-- notifikasi dapat diuji dan dipantau sekarang, dan menyambungkan kanal
-- sungguhan nanti berarti menambah satu implementasi antarmuka.
--
-- Daftar eventnya diambil dari tabel transisi SRS Bab 5.1 dan 5.3, yaitu
-- perpindahan yang menyebut "Antre notifikasi" sebagai efek samping wajib,
-- ditambah pengingat pembelian berulang yang disebut SRS-NOT-001.
INSERT INTO notification_settings (event, enabled, channels) VALUES
    ('ORDER_PAID',             true,  '{LOG}'),
    ('ORDER_OUT_FOR_DELIVERY', true,  '{LOG}'),
    ('ORDER_COMPLETED',        true,  '{LOG}'),
    ('ORDER_CANCELLED',        true,  '{LOG}'),
    ('DELIVERY_FAILED',        true,  '{LOG}'),
    ('REORDER_REMINDER',       false, '{LOG}')
ON CONFLICT (event) DO NOTHING;

-- +goose Down
DROP TABLE notifications;
DROP TABLE notification_settings;
DROP FUNCTION ensure_monthly_partition(text, date);
