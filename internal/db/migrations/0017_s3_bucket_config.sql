-- +goose Up
-- Configuración por bucket al estilo de R2: lectura pública sin firma, reglas
-- CORS y reglas de ciclo de vida. cors y lifecycle guardan JSON; vacío = sin reglas.
ALTER TABLE s3_buckets ADD COLUMN public INTEGER NOT NULL DEFAULT 0;
ALTER TABLE s3_buckets ADD COLUMN cors TEXT NOT NULL DEFAULT '';
ALTER TABLE s3_buckets ADD COLUMN lifecycle TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE s3_buckets DROP COLUMN lifecycle;
ALTER TABLE s3_buckets DROP COLUMN cors;
ALTER TABLE s3_buckets DROP COLUMN public;
