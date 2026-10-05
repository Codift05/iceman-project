-- +goose Up

-- Status pengiriman (SRS Bab 5.3).
CREATE TYPE delivery_status AS ENUM (
    'ASSIGNED',
    'ACCEPTED',
    'ON_THE_WAY',
    'ARRIVED',
    'DELIVERED',
    'FAILED_DELIVERY',
    'RESCHEDULED'
);

-- Profil driver, melengkapi baris users yang berperan DRIVER.
--
-- Dipisahkan dari users karena hanya driver yang memiliki atribut ini, dan
-- menaruhnya di users berarti setiap pengguna kantor ikut membawa kolom
-- kendaraan yang selalu kosong.
--
-- Persetujuan pelacakan disimpan di sini. Pelacakan posisi menyentuh urusan
-- pemantauan karyawan, sehingga perekaman tidak boleh berjalan sebelum driver
-- menyetujuinya, dan pencabutannya harus menghentikan perekaman seketika
-- (SRS-TRK-001, SEC-012).
CREATE TABLE drivers (
    user_id            uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    vehicle_plate      text        NOT NULL DEFAULT '',
    vehicle_note       text        NOT NULL DEFAULT '',
    tracking_consent_at timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER drivers_set_updated_at BEFORE UPDATE ON drivers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Penugasan pengiriman. Satu pesanan satu baris (DB-05).
--
-- Penugasan ulang mengubah baris ini, bukan menambah baris baru, sehingga
-- tidak mungkin ada dua driver yang sama sama merasa bertugas atas satu
-- pesanan. Riwayat penugasannya disimpan pada tabel tersendiri
-- (SRS-DLV-001).
--
-- Posisi terakhir dan perkiraan tiba disalin ke sini agar tetap tersedia
-- setelah rekaman posisi mentah dihapus karena masa simpan (SRS-TRK-004).
CREATE TABLE deliveries (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id   uuid            NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    driver_id  uuid            NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    depot_id   uuid            NOT NULL REFERENCES depots(id) ON DELETE RESTRICT,
    status     delivery_status NOT NULL DEFAULT 'ASSIGNED',

    -- Urutan pengiriman dalam satu slot, dapat diubah admin (SRS-DLV-001).
    sequence_no integer NOT NULL DEFAULT 0,

    -- Waktu perangkat dan waktu server keduanya disimpan. Jam perangkat driver
    -- dapat meleset atau diubah, jadi waktu server yang dipakai untuk urutan
    -- resmi, sementara waktu perangkat disimpan untuk penelusuran
    -- (SRS-DLV-003).
    last_device_time timestamptz,

    last_latitude  numeric(9,6),
    last_longitude numeric(9,6),
    last_position_at timestamptz,
    eta_at           timestamptz,

    accepted_at  timestamptz,
    departed_at  timestamptz,
    arrived_at   timestamptz,
    completed_at timestamptz,

    failure_reason text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT deliveries_order_key UNIQUE (order_id),
    CONSTRAINT deliveries_sequence_nonneg CHECK (sequence_no >= 0),
    CONSTRAINT deliveries_lat_range CHECK (last_latitude  IS NULL OR last_latitude  BETWEEN -90  AND 90),
    CONSTRAINT deliveries_lng_range CHECK (last_longitude IS NULL OR last_longitude BETWEEN -180 AND 180),

    -- Kegagalan kirim wajib beralasan (SRS-DLV-003, SRS-DLV-005). Dijaga basis
    -- data agar tidak ada jalur kode yang dapat menandai gagal tanpa
    -- menyebutkan sebabnya kepada admin.
    CONSTRAINT deliveries_gagal_beralasan
        CHECK (status <> 'FAILED_DELIVERY' OR failure_reason <> '')
);

CREATE INDEX deliveries_driver_idx ON deliveries (driver_id, status);
CREATE INDEX deliveries_depot_idx  ON deliveries (depot_id, status);

CREATE TRIGGER deliveries_set_updated_at BEFORE UPDATE ON deliveries
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- DB-10: driver dan pesanan wajib berasal dari depo yang sama.
--
-- Tidak dapat dinyatakan sebagai CHECK biasa karena menyangkut tiga tabel,
-- jadi dijaga pemicu. Tanpa ini, driver depo A dapat menerima tugas milik
-- depo B, dan ia akan berangkat dari gudang yang tidak menyimpan pesanan itu.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION deliveries_depot_match()
RETURNS trigger AS $$
DECLARE
    depo_pesanan uuid;
    depo_driver  uuid;
BEGIN
    SELECT depot_id INTO depo_pesanan FROM orders WHERE id = NEW.order_id;
    SELECT depot_id INTO depo_driver  FROM users  WHERE id = NEW.driver_id;

    IF depo_pesanan IS DISTINCT FROM NEW.depot_id THEN
        RAISE EXCEPTION 'depo pengiriman % tidak sama dengan depo pesanan %',
            NEW.depot_id, depo_pesanan;
    END IF;
    IF depo_driver IS NULL OR depo_driver IS DISTINCT FROM NEW.depot_id THEN
        RAISE EXCEPTION 'driver % bukan milik depo %', NEW.driver_id, NEW.depot_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER deliveries_depot_match_check
    BEFORE INSERT OR UPDATE OF order_id, driver_id, depot_id ON deliveries
    FOR EACH ROW EXECUTE FUNCTION deliveries_depot_match();

-- Riwayat penugasan. Penugasan ulang mencatat pelaku dan alasan
-- (SRS-DLV-001), dan riwayatnya tetap tersimpan setelah penugasan berubah.
CREATE TABLE delivery_assignments (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    delivery_id uuid        NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    from_driver uuid        REFERENCES users(id) ON DELETE SET NULL,
    to_driver   uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    actor_id    uuid        REFERENCES users(id) ON DELETE SET NULL,
    reason      text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX delivery_assignments_delivery_idx
    ON delivery_assignments (delivery_id, occurred_at);

-- Riwayat status pengiriman. Mencatat waktu perangkat dan waktu server
-- keduanya (SRS-DLV-003).
CREATE TABLE delivery_status_history (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    delivery_id uuid            NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    from_status delivery_status,
    to_status   delivery_status NOT NULL,
    actor_id    uuid            REFERENCES users(id) ON DELETE SET NULL,
    device_time timestamptz,
    reason      text            NOT NULL DEFAULT '',
    occurred_at timestamptz     NOT NULL DEFAULT now()
);

CREATE INDEX delivery_status_history_delivery_idx
    ON delivery_status_history (delivery_id, occurred_at);

-- Bukti serah terima. Satu pengiriman satu bukti (DB-06).
--
-- Berkasnya tidak disimpan di basis data, hanya kuncinya pada object storage.
-- Berkas diunggah langsung oleh perangkat driver memakai URL berbatas waktu,
-- sehingga foto beresolusi penuh tidak melewati server aplikasi
-- (SRS-DLV-004).
CREATE TABLE proof_of_delivery (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    delivery_id   uuid        NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    photo_key     text        NOT NULL,
    receiver_name text        NOT NULL DEFAULT '',
    notes         text        NOT NULL DEFAULT '',
    device_time   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT proof_of_delivery_delivery_key UNIQUE (delivery_id),
    CONSTRAINT proof_of_delivery_photo_wajib  CHECK (photo_key <> '')
);

-- Kendala pengiriman yang dilaporkan driver (SRS-DLV-005).
CREATE TABLE delivery_issues (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    delivery_id uuid        NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    category    text        NOT NULL,
    note        text        NOT NULL DEFAULT '',
    photo_key   text        NOT NULL DEFAULT '',
    reported_by uuid        REFERENCES users(id) ON DELETE SET NULL,
    device_time timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT delivery_issues_category_wajib CHECK (category <> '')
);

CREATE INDEX delivery_issues_delivery_idx ON delivery_issues (delivery_id, created_at);

-- Perintah dari perangkat driver yang sudah diproses (DB-03).
--
-- Pengenal dibuat di perangkat, sehingga perintah yang dikirim ulang karena
-- jaringan terputus tidak diproses dua kali (SRS-DLV-006). Hasil pemrosesan
-- disimpan agar pengiriman ulang dapat dijawab dengan jawaban yang sama, bukan
-- dengan galat.
CREATE TABLE sync_events (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    client_event_id text        NOT NULL,
    driver_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    delivery_id     uuid        REFERENCES deliveries(id) ON DELETE CASCADE,
    command         text        NOT NULL,
    device_time     timestamptz,
    outcome         text        NOT NULL,
    conflict_reason text        NOT NULL DEFAULT '',
    processed_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sync_events_client_key UNIQUE (client_event_id),
    CONSTRAINT sync_events_outcome_valid CHECK (outcome IN ('APPLIED', 'CONFLICT'))
);

CREATE INDEX sync_events_driver_idx ON sync_events (driver_id, processed_at DESC);

-- Rekaman posisi driver, dipartisi menurut bulan (SRS-TRK-004).
--
-- Partisi dipakai agar pembersihan masa simpan dilakukan dengan melepas
-- partisi, bukan menghapus baris satu per satu. Melepas partisi hampir
-- seketika dan tidak mengunci tabel, sedangkan DELETE pada puluhan juta baris
-- mengunci dan membengkakkan tabel.
--
-- Kunci utamanya menyertakan device_time karena PostgreSQL mewajibkan kolom
-- partisi menjadi bagian dari setiap kekangan keunikan.
CREATE TABLE driver_locations (
    id          uuid        NOT NULL DEFAULT uuidv7(),
    delivery_id uuid        NOT NULL,
    driver_id   uuid        NOT NULL,
    latitude    numeric(9,6) NOT NULL,
    longitude   numeric(9,6) NOT NULL,
    accuracy_m  numeric(7,2),
    device_time timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (device_time, id),

    -- Satu posisi per pengiriman per cap waktu perangkat.
    --
    -- Ini kunci alami rekaman posisi, dan gunanya membuat pengiriman ulang
    -- idempoten: perangkat yang kehilangan sinyal mengirim ulang antreannya
    -- dari awal, dan antrean itu memuat posisi yang sebagian sudah diterima.
    -- Tanpa kekangan ini, satu perjalanan terekam berkali kali dan jejaknya
    -- tidak dapat dipakai menghitung jarak maupun lama perjalanan.
    --
    -- device_time wajib disertakan karena PostgreSQL mengharuskan kolom
    -- partisi menjadi bagian dari setiap kekangan keunikan.
    CONSTRAINT driver_locations_fix_key UNIQUE (device_time, delivery_id),

    -- DB-12: koordinat rusak dari perangkat tidak boleh tersimpan dan merusak
    -- perhitungan rute.
    CONSTRAINT driver_locations_lat_range CHECK (latitude  BETWEEN -90  AND 90),
    CONSTRAINT driver_locations_lng_range CHECK (longitude BETWEEN -180 AND 180),
    CONSTRAINT driver_locations_accuracy_nonneg CHECK (accuracy_m IS NULL OR accuracy_m >= 0)
) PARTITION BY RANGE (device_time);

CREATE INDEX driver_locations_delivery_idx ON driver_locations (delivery_id, device_time DESC);
CREATE INDEX driver_locations_driver_idx   ON driver_locations (driver_id, device_time DESC);

-- ensure_driver_location_partition membuat partisi satu bulan bila belum ada.
--
-- Dipanggil pekerja latar menjelang akhir bulan. Dibuat sebagai fungsi basis
-- data, bukan rangkaian DDL di kode aplikasi, agar nama dan batas partisinya
-- dihitung di satu tempat saja.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ensure_driver_location_partition(bulan date)
RETURNS text AS $$
DECLARE
    awal  date := date_trunc('month', bulan)::date;
    akhir date := (date_trunc('month', bulan) + interval '1 month')::date;
    nama  text := 'driver_locations_' || to_char(awal, 'YYYY_MM');
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = nama) THEN
        RETURN nama;
    END IF;
    EXECUTE format(
        'CREATE TABLE %I PARTITION OF driver_locations FOR VALUES FROM (%L) TO (%L)',
        nama, awal, akhir);
    RETURN nama;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Partisi bulan ini dan dua bulan ke depan disiapkan sejak awal, supaya
-- pencatatan posisi tidak gagal seandainya pekerja latar belum berjalan.
SELECT ensure_driver_location_partition(current_date);
SELECT ensure_driver_location_partition((current_date + interval '1 month')::date);
SELECT ensure_driver_location_partition((current_date + interval '2 months')::date);

-- +goose Down
DROP FUNCTION ensure_driver_location_partition(date);
DROP TABLE driver_locations;
DROP TABLE sync_events;
DROP TABLE delivery_issues;
DROP TABLE proof_of_delivery;
DROP TABLE delivery_status_history;
DROP TABLE delivery_assignments;
DROP TRIGGER deliveries_depot_match_check ON deliveries;
DROP FUNCTION deliveries_depot_match();
DROP TABLE deliveries;
DROP TABLE drivers;
DROP TYPE delivery_status;
