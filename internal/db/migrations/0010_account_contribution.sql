-- +goose Up
-- Cuánto de cada cuenta puede llenar CloudMesh: all (todo), reserve (dejar
-- contrib_value % libre) o limit (no pasar de contrib_value bytes usados).
ALTER TABLE accounts ADD COLUMN contrib_mode TEXT NOT NULL DEFAULT 'all';
ALTER TABLE accounts ADD COLUMN contrib_value INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE accounts DROP COLUMN contrib_value;
ALTER TABLE accounts DROP COLUMN contrib_mode;
