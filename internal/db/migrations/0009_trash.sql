-- +goose Up
-- Papelera: un archivo con deleted_at > 0 no aparece en el disco pero conserva
-- sus bloques hasta que se vacía o caduca.
ALTER TABLE files ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX files_deleted_at ON files(deleted_at) WHERE deleted_at > 0;

-- +goose Down
DROP INDEX files_deleted_at;
ALTER TABLE files DROP COLUMN deleted_at;
