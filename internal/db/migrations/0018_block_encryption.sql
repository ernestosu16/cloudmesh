-- +goose Up
-- Cifrado de bloques: cada archivo tiene su clave (32 bytes aleatorios) envuelta
-- con la clave maestra; vacía = sin clave todavía. Cada bloque dice si lo que
-- hay en el proveedor está cifrado, así los antiguos se convierten de uno en uno.
-- size y sha256 de chunks son siempre los del bloque tal como está en el proveedor.
ALTER TABLE files ADD COLUMN enc_key BLOB NOT NULL DEFAULT x'';
ALTER TABLE chunks ADD COLUMN encrypted INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE chunks DROP COLUMN encrypted;
ALTER TABLE files DROP COLUMN enc_key;
