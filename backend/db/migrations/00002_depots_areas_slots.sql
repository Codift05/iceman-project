-- +goose Up

-- Depo. Koordinat dan radius wajib terisi karena penentuan depo terdekat
-- bergantung pada keduanya (SRS-DEP-001, DB-11).
CREATE TABLE depots (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    code               text        NOT NULL,
    name               text        NOT NULL,
    address            text        NOT NULL DEFAULT '',
    latitude           numeric(9,6)  NOT NULL,
    longitude          numeric(9,6)  NOT NULL,
    service_radius_km  numeric(6,2)  NOT NULL,
    is_active          boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT depots_code_key        UNIQUE (code),
    CONSTRAINT depots_radius_positive CHECK (service_radius_km > 0),
    CONSTRAINT depots_lat_range       CHECK (latitude  BETWEEN -90  AND 90),
    CONSTRAINT depots_lng_range       CHECK (longitude BETWEEN -180 AND 180)
);

CREATE TRIGGER depots_set_updated_at BEFORE UPDATE ON depots
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Wilayah layanan selalu milik satu depo (SRS-DEP-003).
CREATE TABLE service_areas (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    depot_id           uuid        NOT NULL REFERENCES depots(id) ON DELETE RESTRICT,
    name               text        NOT NULL,
    delivery_fee_cents bigint      NOT NULL,
    is_active          boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT service_areas_fee_nonneg CHECK (delivery_fee_cents >= 0),
    CONSTRAINT service_areas_name_key   UNIQUE (depot_id, name)
);

CREATE INDEX service_areas_depot_idx ON service_areas (depot_id) WHERE is_active;

CREATE TRIGGER service_areas_set_updated_at BEFORE UPDATE ON service_areas
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Slot pengiriman. Kolom used adalah kuota terpakai, dijaga agar tidak pernah
-- melampaui capacity maupun turun di bawah nol (DB-01). Constraint ini adalah
-- jaring pengaman terakhir, bukan pengganti SELECT ... FOR UPDATE.
CREATE TABLE delivery_slots (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    service_area_id uuid        NOT NULL REFERENCES service_areas(id) ON DELETE RESTRICT,
    slot_date       date        NOT NULL,
    window_start    time        NOT NULL,
    window_end      time        NOT NULL,
    capacity        integer     NOT NULL,
    used            integer     NOT NULL DEFAULT 0,
    cutoff_at       timestamptz NOT NULL,
    is_holiday      boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT delivery_slots_window_valid   CHECK (window_end > window_start),
    CONSTRAINT delivery_slots_capacity_valid CHECK (capacity >= 0),
    CONSTRAINT delivery_slots_used_valid     CHECK (used >= 0 AND used <= capacity),
    CONSTRAINT delivery_slots_unique_window  UNIQUE (service_area_id, slot_date, window_start)
);

CREATE INDEX delivery_slots_lookup_idx
    ON delivery_slots (service_area_id, slot_date);

CREATE TRIGGER delivery_slots_set_updated_at BEFORE UPDATE ON delivery_slots
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS delivery_slots;
DROP TABLE IF EXISTS service_areas;
DROP TABLE IF EXISTS depots;
