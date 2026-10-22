-- +goose Up
-- Los enlaces quedan como en Mega: solo lectura y contraseña opcional. Sin
-- caducidad ni límite de descargas; el contador de descargas se conserva.
ALTER TABLE shares DROP COLUMN expires_at;
ALTER TABLE shares DROP COLUMN max_downloads;

-- +goose Down
ALTER TABLE shares ADD COLUMN max_downloads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shares ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
