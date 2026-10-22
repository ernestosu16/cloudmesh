-- +goose Up
-- Metadatos de usuario de S3 (cabeceras x-amz-meta-*), en JSON, tal cual los
-- mandó el cliente: rclone guarda ahí la fecha con nanosegundos.
ALTER TABLE files ADD COLUMN meta TEXT NOT NULL DEFAULT '';
ALTER TABLE s3_uploads ADD COLUMN meta TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE s3_uploads DROP COLUMN meta;
ALTER TABLE files DROP COLUMN meta;
