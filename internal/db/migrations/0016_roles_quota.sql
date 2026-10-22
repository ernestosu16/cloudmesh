-- +goose Up
-- Roles: admin | editor | reader. Los usuarios que ya existen eran todos
-- administradores de hecho y lo siguen siendo.
-- quota_bytes: 0 es sin límite. owner_id: quién subió el archivo (cuenta para
-- su cuota); NULL en lo importado por sync o subido antes de las cuotas.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
ALTER TABLE users ADD COLUMN quota_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN owner_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX files_owner_id ON files(owner_id) WHERE owner_id IS NOT NULL;

-- +goose Down
DROP INDEX files_owner_id;
ALTER TABLE files DROP COLUMN owner_id;
ALTER TABLE users DROP COLUMN quota_bytes;
ALTER TABLE users DROP COLUMN role;
