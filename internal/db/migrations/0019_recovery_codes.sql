-- +goose Up
-- Códigos de recuperación de la 2FA: lista JSON con el SHA-256 de los que
-- quedan sin usar. Usar uno lo quita de la lista.
ALTER TABLE users ADD COLUMN recovery_codes TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE users DROP COLUMN recovery_codes;
