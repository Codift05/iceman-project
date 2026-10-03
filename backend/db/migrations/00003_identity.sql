-- +goose Up

CREATE TABLE roles (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    code       text        NOT NULL,
    name       text        NOT NULL,
    is_system  boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT roles_code_key UNIQUE (code)
);

CREATE TABLE permissions (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    code       text NOT NULL,
    resource   text NOT NULL,
    action     text NOT NULL,

    CONSTRAINT permissions_code_key UNIQUE (code)
);

CREATE TABLE role_permissions (
    role_id       uuid NOT NULL REFERENCES roles(id)       ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,

    PRIMARY KEY (role_id, permission_id)
);

-- Pengguna internal. depot_id membatasi peran pada satu depo; NULL berarti
-- berwenang atas seluruh depo (SRS-DEP-003).
CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    role_id         uuid        NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    depot_id        uuid            NULL REFERENCES depots(id) ON DELETE RESTRICT,
    email           text            NULL,
    phone           text            NULL,
    name            text        NOT NULL,
    password_hash   text            NULL,
    mfa_secret      text            NULL,
    status          text        NOT NULL DEFAULT 'ACTIVE',
    failed_attempts integer     NOT NULL DEFAULT 0,
    locked_until    timestamptz     NULL,
    last_login_at   timestamptz     NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_email_key       UNIQUE (email),
    CONSTRAINT users_phone_key       UNIQUE (phone),
    CONSTRAINT users_status_valid    CHECK (status IN ('ACTIVE', 'INACTIVE')),
    CONSTRAINT users_identity_exists CHECK (email IS NOT NULL OR phone IS NOT NULL),
    CONSTRAINT users_attempts_nonneg CHECK (failed_attempts >= 0)
);

CREATE INDEX users_role_idx  ON users (role_id);
CREATE INDEX users_depot_idx ON users (depot_id) WHERE depot_id IS NOT NULL;

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Sesi menyimpan hash token penyegar, bukan tokennya. rotated_from menautkan
-- sesi baru ke sesi yang digantikannya, sehingga pemakaian ulang token lama
-- dapat dideteksi dan seluruh sesi pengguna itu dibatalkan (SRS-AUT-003).
CREATE TABLE sessions (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id            uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    refresh_token_hash text        NOT NULL,
    rotated_from       uuid            NULL REFERENCES sessions(id) ON DELETE SET NULL,
    device_info        text        NOT NULL DEFAULT '',
    expires_at         timestamptz NOT NULL,
    used_at            timestamptz     NULL,
    revoked_at         timestamptz     NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sessions_token_key UNIQUE (refresh_token_hash)
);

CREATE INDEX sessions_user_idx ON sessions (user_id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
