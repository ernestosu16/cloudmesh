-- +goose Up
-- Fecha de modificación que manda el cliente (x-amz-meta-mtime de rclone) al
-- iniciar una subida multipart; 0 si no la manda.
ALTER TABLE s3_uploads ADD COLUMN mtime INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE s3_uploads DROP COLUMN mtime;
