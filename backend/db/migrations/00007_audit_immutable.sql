-- +goose Up

-- Catatan audit tidak boleh diubah maupun dihapus lewat aplikasi.
--
-- Tanpa penjagaan ini, jejak yang paling penting justru yang paling mudah
-- dihapus oleh pihak yang ingin menutupi perbuatannya. Pemicu di bawah
-- berlaku untuk siapa pun, termasuk pemilik tabel.
--
-- Pembersihan karena masa simpan nanti dilakukan dengan melepas partisi,
-- bukan menghapus baris, sehingga tidak melewati pemicu ini dan tidak
-- membuka celah penghapusan satuan.
-- +goose StatementBegin
CREATE FUNCTION audit_trail_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_trail bersifat tetap: operasi % tidak diizinkan', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER audit_trail_no_update
    BEFORE UPDATE ON audit_trail
    FOR EACH ROW EXECUTE FUNCTION audit_trail_immutable();

CREATE TRIGGER audit_trail_no_delete
    BEFORE DELETE ON audit_trail
    FOR EACH ROW EXECUTE FUNCTION audit_trail_immutable();

-- Kolom tambahan agar penolakan akses juga dapat ditelusuri.
ALTER TABLE audit_trail
    ADD COLUMN outcome text NOT NULL DEFAULT 'APPLIED',
    ADD COLUMN detail  text NULL,
    ADD CONSTRAINT audit_trail_outcome_valid
        CHECK (outcome IN ('APPLIED', 'DENIED', 'FAILED'));

CREATE INDEX audit_trail_outcome_idx
    ON audit_trail (outcome, occurred_at DESC)
    WHERE outcome <> 'APPLIED';

-- +goose Down
DROP TRIGGER IF EXISTS audit_trail_no_delete ON audit_trail;
DROP TRIGGER IF EXISTS audit_trail_no_update ON audit_trail;
DROP FUNCTION IF EXISTS audit_trail_immutable();
ALTER TABLE audit_trail
    DROP CONSTRAINT IF EXISTS audit_trail_outcome_valid,
    DROP COLUMN IF EXISTS detail,
    DROP COLUMN IF EXISTS outcome;
