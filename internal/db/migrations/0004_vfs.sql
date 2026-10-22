-- +goose Up
-- Árbol virtual propio de CloudMesh. parent_id = 0 es la raíz (no existe como fila).
CREATE TABLE folders (
    id         INTEGER PRIMARY KEY,
    parent_id  INTEGER NOT NULL DEFAULT 0,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (parent_id, name)
);

-- Índice de archivos: cada fila apunta a un nodo remoto de una cuenta.
CREATE TABLE files (
    id           INTEGER PRIMARY KEY,
    folder_id    INTEGER NOT NULL DEFAULT 0,
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    remote_id    TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    size         INTEGER NOT NULL,
    mime         TEXT    NOT NULL DEFAULT '',
    remote_mtime INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (account_id, remote_id)
);

CREATE INDEX files_folder_id ON files(folder_id, name);
CREATE INDEX files_name ON files(name);

-- +goose Down
DROP TABLE files;
DROP TABLE folders;
