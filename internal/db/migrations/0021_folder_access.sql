-- +goose Up
-- Carpetas restringidas: una carpeta con restricted = 1 (y todo lo que
-- cuelga de ella) solo la ven los administradores y los usuarios con fila en
-- folder_access; level dice si pueden cambiar cosas (write) o solo ver (read).
-- Sin restricciones todos ven el disco como antes.
ALTER TABLE folders ADD COLUMN restricted INTEGER NOT NULL DEFAULT 0;

CREATE TABLE folder_access (
    folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level     TEXT    NOT NULL, -- read | write
    PRIMARY KEY (folder_id, user_id)
);
CREATE INDEX folder_access_user ON folder_access(user_id);

-- +goose Down
DROP TABLE folder_access;
ALTER TABLE folders DROP COLUMN restricted;
