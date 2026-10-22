-- +goose Up
-- Verificación en dos pasos (TOTP). totp_secret va cifrado con la clave
-- maestra; con totp_enabled = 0 y secreto presente, la activación está a medias.
-- totp_last_step impide reutilizar un código ya aceptado.
ALTER TABLE users ADD COLUMN totp_secret BLOB NOT NULL DEFAULT x'';
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN totp_last_step;
ALTER TABLE users DROP COLUMN totp_enabled;
ALTER TABLE users DROP COLUMN totp_secret;
