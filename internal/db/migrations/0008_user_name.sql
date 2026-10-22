-- +goose Up
-- Nombre visible del usuario; vacío muestra el email.
ALTER TABLE users ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN name;
