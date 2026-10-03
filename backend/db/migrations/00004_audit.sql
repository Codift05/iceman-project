-- +goose Up

-- Jejak perubahan data kritis. Ditulis dalam transaksi yang sama dengan
-- perubahannya, sehingga membatalkan transaksi utama juga membatalkan
-- catatan auditnya (SRS-AUD-001, BR-005).
CREATE TABLE audit_trail (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity      text        NOT NULL,
    entity_id   uuid            NULL,
    action      text        NOT NULL,
    before      jsonb           NULL,
    after       jsonb           NULL,
    actor_id    uuid            NULL REFERENCES users(id) ON DELETE SET NULL,
    request_id  text            NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_trail_entity_idx ON audit_trail (entity, entity_id, occurred_at DESC);
CREATE INDEX audit_trail_actor_idx  ON audit_trail (actor_id, occurred_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_trail;
