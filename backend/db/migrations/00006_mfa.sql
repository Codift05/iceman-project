-- +goose Up

-- Kolom pendukung verifikasi faktor kedua berbasis waktu (TOTP, RFC 6238).
--
-- mfa_last_step menyimpan langkah waktu terakhir yang sudah dipakai. Satu kode
-- hanya berlaku sekali: tanpa ini, kode yang terlihat orang lain masih dapat
-- dipakai ulang selama tiga puluh detik yang sama.
ALTER TABLE users
    ADD COLUMN mfa_enabled_at timestamptz NULL,
    ADD COLUMN mfa_last_step  bigint      NULL;

COMMENT ON COLUMN users.mfa_secret IS
    'Rahasia TOTP dalam base32. Terisi saat pendaftaran dimulai, berlaku setelah mfa_enabled_at terisi.';
COMMENT ON COLUMN users.mfa_enabled_at IS
    'Waktu faktor kedua dinyatakan aktif, yaitu setelah pengguna membuktikan satu kode yang benar.';
COMMENT ON COLUMN users.mfa_last_step IS
    'Langkah waktu TOTP terakhir yang sudah terpakai, mencegah satu kode dipakai dua kali.';

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS mfa_last_step,
    DROP COLUMN IF EXISTS mfa_enabled_at;
